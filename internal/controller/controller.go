// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"fmt"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
)

func init() { MustInitializeScheme(scheme) }

// scheme contains the necessary schemas for the AI Gateway.
var scheme = runtime.NewScheme()

// MustInitializeScheme initializes the scheme with the necessary schemas for the AI Gateway.
// This is exported for the testing purposes.
func MustInitializeScheme(scheme *runtime.Scheme) {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(aigv1a1.AddToScheme(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))
	utilruntime.Must(egv1a1.AddToScheme(scheme))
	utilruntime.Must(gwapiv1.Install(scheme))
	utilruntime.Must(gwapiv1b1.Install(scheme))
}

// Options defines the program configurable options that may be passed on the command line.
type Options struct {
	ExtProcLogLevel      string
	ExtProcImage         string
	EnableLeaderElection bool
}

type (
	// syncAIGatewayRouteFn is a function that syncs an AIGatewayRoute. This is used to cross the controller boundary
	// from AIServiceBackend to AIGatewayRoute when an AIServiceBackend is referenced by an AIGatewayRoute.
	syncAIGatewayRouteFn func(context.Context, *aigv1a1.AIGatewayRoute) error
	// syncAIServiceBackendFn is a function that syncs an AIServiceBackend. This is used to cross the controller boundary
	// from BackendSecurityPolicy to AIServiceBackend when a BackendSecurityPolicy is referenced by an AIServiceBackend.
	syncAIServiceBackendFn func(context.Context, *aigv1a1.AIServiceBackend) error
	// syncBackendSecurityPolicyFn is a function that syncs a BackendSecurityPolicy. This is used to cross the controller boundary
	// from Secret to BackendSecurityPolicy when a Secret is referenced by a BackendSecurityPolicy.
	syncBackendSecurityPolicyFn func(context.Context, *aigv1a1.BackendSecurityPolicy) error
)

// StartControllers starts the controllers for the AI Gateway.
// This blocks until the manager is stopped.
//
// Note: this is tested with envtest, hence the test exists outside of this package. See /tests/controller_test.go.
func StartControllers(ctx context.Context, config *rest.Config, logger logr.Logger, options Options) error {
	opt := ctrl.Options{
		Scheme:           scheme,
		LeaderElection:   options.EnableLeaderElection,
		LeaderElectionID: "envoy-ai-gateway-controller",
	}

	mgr, err := ctrl.NewManager(config, opt)
	if err != nil {
		return fmt.Errorf("failed to create new controller manager: %w", err)
	}

	c := mgr.GetClient()
	indexer := mgr.GetFieldIndexer()
	if err = ApplyIndexing(ctx, indexer.IndexField); err != nil {
		return fmt.Errorf("failed to apply indexing: %w", err)
	}

	routeC := NewAIGatewayRouteController(c, kubernetes.NewForConfigOrDie(config), logger.WithName("ai-gateway-route"),
		options.ExtProcImage, options.ExtProcLogLevel)
	if err = ctrl.NewControllerManagedBy(mgr).
		For(&aigv1a1.AIGatewayRoute{}).
		Owns(&egv1a1.EnvoyExtensionPolicy{}).
		Owns(&gwapiv1.HTTPRoute{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(routeC); err != nil {
		return fmt.Errorf("failed to create controller for AIGatewayRoute: %w", err)
	}

	backendC := NewAIServiceBackendController(c, kubernetes.NewForConfigOrDie(config), logger.
		WithName("ai-service-backend"), routeC.syncAIGatewayRoute)
	if err = ctrl.NewControllerManagedBy(mgr).
		For(&aigv1a1.AIServiceBackend{}).
		Complete(backendC); err != nil {
		return fmt.Errorf("failed to create controller for AIServiceBackend: %w", err)
	}

	bspController := NewBackendSecurityPolicyController(c, kubernetes.NewForConfigOrDie(config), logger.
		WithName("backend-security-policy"), backendC.syncAIServiceBackend)
	if err = ctrl.NewControllerManagedBy(mgr).
		For(&aigv1a1.BackendSecurityPolicy{}).
		Complete(bspController); err != nil {
		return fmt.Errorf("failed to create controller for BackendSecurityPolicy: %w", err)
	}

	secretC := NewSecretController(c, kubernetes.NewForConfigOrDie(config), logger.
		WithName("secret"), bspController.syncBackendSecurityPolicy)
	if err = ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}).
		Complete(secretC); err != nil {
		return fmt.Errorf("failed to create controller for Secret: %w", err)
	}

	if err = mgr.Start(ctx); err != nil { // This blocks until the manager is stopped.
		return fmt.Errorf("failed to start controller manager: %w", err)
	}
	return nil
}

const (
	// k8sClientIndexSecretToReferencingBackendSecurityPolicy is the index name that maps
	// from a Secret to the BackendSecurityPolicy that references it.
	k8sClientIndexSecretToReferencingBackendSecurityPolicy = "SecretToReferencingBackendSecurityPolicy"
	// k8sClientIndexBackendToReferencingAIGatewayRoute is the index name that maps from a Backend to the
	// AIGatewayRoute that references it.
	k8sClientIndexBackendToReferencingAIGatewayRoute = "BackendToReferencingAIGatewayRoute"
	// k8sClientIndexBackendSecurityPolicyToReferencingAIServiceBackend is the index name that maps from a BackendSecurityPolicy
	// to the AIServiceBackend that references it.
	k8sClientIndexBackendSecurityPolicyToReferencingAIServiceBackend = "BackendSecurityPolicyToReferencingAIServiceBackend"
)

// ApplyIndexing applies indexing to the given indexer. This is exported for testing purposes.
func ApplyIndexing(ctx context.Context, indexer func(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error) error {
	err := indexer(ctx, &aigv1a1.AIGatewayRoute{},
		k8sClientIndexBackendToReferencingAIGatewayRoute, aiGatewayRouteIndexFunc)
	if err != nil {
		return fmt.Errorf("failed to index field for AIGatewayRoute: %w", err)
	}
	err = indexer(ctx, &aigv1a1.AIServiceBackend{},
		k8sClientIndexBackendSecurityPolicyToReferencingAIServiceBackend, aiServiceBackendIndexFunc)
	if err != nil {
		return fmt.Errorf("failed to index field for AIServiceBackend: %w", err)
	}
	err = indexer(ctx, &aigv1a1.BackendSecurityPolicy{},
		k8sClientIndexSecretToReferencingBackendSecurityPolicy, backendSecurityPolicyIndexFunc)
	if err != nil {
		return fmt.Errorf("failed to index field for BackendSecurityPolicy: %w", err)
	}
	return nil
}

func aiGatewayRouteIndexFunc(o client.Object) []string {
	aiGatewayRoute := o.(*aigv1a1.AIGatewayRoute)
	var ret []string
	for _, rule := range aiGatewayRoute.Spec.Rules {
		for _, backend := range rule.BackendRefs {
			key := fmt.Sprintf("%s.%s", backend.Name, aiGatewayRoute.Namespace)
			ret = append(ret, key)
		}
	}
	return ret
}

func aiServiceBackendIndexFunc(o client.Object) []string {
	aiServiceBackend := o.(*aigv1a1.AIServiceBackend)
	var ret []string
	if ref := aiServiceBackend.Spec.BackendSecurityPolicyRef; ref != nil {
		ret = append(ret, fmt.Sprintf("%s.%s", ref.Name, aiServiceBackend.Namespace))
	}
	return ret
}

func backendSecurityPolicyIndexFunc(o client.Object) []string {
	backendSecurityPolicy := o.(*aigv1a1.BackendSecurityPolicy)
	var key string
	switch backendSecurityPolicy.Spec.Type {
	case aigv1a1.BackendSecurityPolicyTypeAPIKey:
		apiKey := backendSecurityPolicy.Spec.APIKey
		key = getSecretNameAndNamespace(apiKey.SecretRef, backendSecurityPolicy.Namespace)
	case aigv1a1.BackendSecurityPolicyTypeAWSCredentials:
		awsCreds := backendSecurityPolicy.Spec.AWSCredentials
		if awsCreds.CredentialsFile != nil {
			key = getSecretNameAndNamespace(awsCreds.CredentialsFile.SecretRef, backendSecurityPolicy.Namespace)
		} else if awsCreds.OIDCExchangeToken != nil {
			key = backendSecurityPolicyKey(backendSecurityPolicy.Namespace, backendSecurityPolicy.Name)
		}
	}
	return []string{key}
}

func getSecretNameAndNamespace(secretRef *gwapiv1.SecretObjectReference, namespace string) string {
	if secretRef.Namespace != nil {
		return fmt.Sprintf("%s.%s", secretRef.Name, *secretRef.Namespace)
	}
	return fmt.Sprintf("%s.%s", secretRef.Name, namespace)
}

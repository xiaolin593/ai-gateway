// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/go-logr/logr"
	"golang.org/x/oauth2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aigv1a1 "github.com/envoyproxy/ai-gateway/api/v1alpha1"
	"github.com/envoyproxy/ai-gateway/internal/controller/oauth"
	"github.com/envoyproxy/ai-gateway/internal/controller/rotators"
)

// preRotationWindow specifies how long before expiry to rotate credentials.
// Temporarily a fixed duration.
const preRotationWindow = 5 * time.Minute

// BackendSecurityPolicyController implements [reconcile.TypedReconciler] for [aigv1a1.BackendSecurityPolicy].
//
// Exported for testing purposes.
type BackendSecurityPolicyController struct {
	client               client.Client
	kube                 kubernetes.Interface
	logger               logr.Logger
	oidcTokenCache       map[string]*oauth2.Token
	oidcTokenCacheMutex  sync.RWMutex
	syncAIServiceBackend syncAIServiceBackendFn
}

func NewBackendSecurityPolicyController(client client.Client, kube kubernetes.Interface, logger logr.Logger, syncAIServiceBackend syncAIServiceBackendFn) *BackendSecurityPolicyController {
	return &BackendSecurityPolicyController{
		client:               client,
		kube:                 kube,
		logger:               logger,
		oidcTokenCache:       make(map[string]*oauth2.Token),
		syncAIServiceBackend: syncAIServiceBackend,
	}
}

// Reconcile implements the [reconcile.TypedReconciler] for [aigv1a1.BackendSecurityPolicy].
func (c *BackendSecurityPolicyController) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, err error) {
	var bsp aigv1a1.BackendSecurityPolicy
	if err = c.client.Get(ctx, req.NamespacedName, &bsp); err != nil {
		if apierrors.IsNotFound(err) {
			c.logger.Info("Deleting Backend Security Policy",
				"namespace", req.Namespace, "name", req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// create rotator to get relevant backend's credentials
	var rotator rotators.Rotator

	switch bsp.Spec.Type {
	case aigv1a1.BackendSecurityPolicyTypeAWSCredentials:
		c.logger.Info(fmt.Sprintf("creating aws rotator %s ", bsp.Spec.Type))
		region := bsp.Spec.AWSCredentials.Region
		roleArn := bsp.Spec.AWSCredentials.OIDCExchangeToken.AwsRoleArn
		rotator, err = rotators.NewAWSOIDCRotator(ctx, c.client, nil, c.kube, c.logger, bsp.Namespace, bsp.Name, preRotationWindow, roleArn, region)
		if err != nil {
			return ctrl.Result{}, err
		}

	case aigv1a1.BackendSecurityPolicyTypeAzureCredentials:
		c.logger.Info(fmt.Sprintf("creating azure rotator %s ", bsp.Spec.Type))
		clientID := bsp.Spec.AzureCredentials.ClientID
		tenantID := bsp.Spec.AzureCredentials.TenantID
		clientSecretRef := bsp.Spec.AzureCredentials.ClientSecretRef
		if clientSecretRef.Namespace == nil {
			return ctrl.Result{}, errors.New("missing client secret ref")
		}
		secretNamespace := string(*clientSecretRef.Namespace)
		secretName := string(clientSecretRef.Name)
		var secret *corev1.Secret
		secret, err = rotators.LookupSecret(ctx, c.client, secretNamespace, secretName)
		if err != nil {
			c.logger.Error(err, "failed to lookup client secret", "namespace", secretNamespace, "name", secretName)
			return ctrl.Result{}, err
		}
		azureSecretValue, exists := secret.Data[rotators.AzureAccessTokenKey]
		if !exists {
			return ctrl.Result{}, errors.New("missing azure access token")
		}
		azureClientSecret := string(azureSecretValue)
		rotator, err = rotators.NewAzureTokenRotator(c.client, c.kube, c.logger, bsp.Namespace, bsp.Name, preRotationWindow, clientID, tenantID, azureClientSecret)
		if err != nil {
			return ctrl.Result{}, err
		}

	default:
		err = fmt.Errorf("unsupported backend security type %s for creating rotator", bsp.Spec.Type)
		c.logger.Error(err, "namespace", bsp.Namespace, "name", bsp.Name)
		return ctrl.Result{}, err
	}

	var duration time.Duration
	var rotationTime time.Time
	rotationTime, err = rotator.GetPreRotationTime(ctx)
	if err != nil {
		c.logger.Error(err, "failed to get rotation time, retry in one minute")
		res = ctrl.Result{RequeueAfter: time.Minute}
		return res, c.syncBackendSecurityPolicy(ctx, &bsp)
	}
	if rotator.IsExpired(rotationTime) {
		duration, err = c.rotateCredential(ctx, &bsp, rotator)
		if err != nil {
			c.logger.Error(err, "failed to rotate OIDC exchange token, retry in one minute")
			duration = time.Minute
		}
	} else {
		duration = time.Until(rotationTime)
	}
	res = ctrl.Result{RequeueAfter: duration}
	return res, c.syncBackendSecurityPolicy(ctx, &bsp)
}

// rotateCredential rotates the credentials using the access token from OIDC provider and return the requeue time for next rotation.
func (c *BackendSecurityPolicyController) rotateCredential(ctx context.Context, policy *aigv1a1.BackendSecurityPolicy, rotator rotators.Rotator) (time.Duration, error) {
	var err error
	// bedrock-us-
	// uniqueness check
	bspKey := backendSecurityPolicyKey(policy.Namespace, policy.Name)
	oidc := getBackendSecurityPolicyAuthOIDC(policy.Spec)

	var token string
	if oidc != nil {
		c.oidcTokenCacheMutex.RLock()
		// oidc token, access token, expiration granted by BSSO? 10 hours
		// oidc token last longer than aws credentials(access key, secret, session 1 hour)
		validToken, ok := c.oidcTokenCache[bspKey]
		c.oidcTokenCacheMutex.RUnlock()
		if !ok || validToken == nil || rotators.IsBufferedTimeExpired(preRotationWindow, validToken.Expiry) {
			oidcProvider := oauth.NewOIDCProvider(c.client, *oidc)
			validToken, err = oidcProvider.FetchToken(ctx)
			if err != nil {
				return time.Minute, err
			}
			c.oidcTokenCacheMutex.Lock()
			c.oidcTokenCache[bspKey] = validToken
			c.oidcTokenCacheMutex.Unlock()
		}
		token = validToken.AccessToken
	}

	err = rotator.Rotate(ctx, token)
	if err != nil {
		return time.Minute, err
	}
	rotationTime, err := rotator.GetPreRotationTime(ctx)
	if err != nil {
		return time.Minute, err
	}
	return time.Until(rotationTime), nil
}

// getBackendSecurityPolicyAuthOIDC returns the backendSecurityPolicy's OIDC pointer or nil.
func getBackendSecurityPolicyAuthOIDC(spec aigv1a1.BackendSecurityPolicySpec) *egv1a1.OIDC {
	// Currently only supports AWS.
	switch spec.Type {
	case aigv1a1.BackendSecurityPolicyTypeAWSCredentials:
		if spec.AWSCredentials != nil && spec.AWSCredentials.OIDCExchangeToken != nil {
			return &spec.AWSCredentials.OIDCExchangeToken.OIDC
		}
	case aigv1a1.BackendSecurityPolicyTypeAzureCredentials:
		// Currently doesn't support Azure OIDC
		return nil
	default:
		return nil
	}
	return nil
}

// backendSecurityPolicyKey returns the key used for indexing and caching the backendSecurityPolicy.
func backendSecurityPolicyKey(namespace, name string) string {
	return fmt.Sprintf("%s.%s", name, namespace)
}

func (c *BackendSecurityPolicyController) syncBackendSecurityPolicy(ctx context.Context, bsp *aigv1a1.BackendSecurityPolicy) error {
	key := backendSecurityPolicyKey(bsp.Namespace, bsp.Name)
	var aiServiceBackends aigv1a1.AIServiceBackendList
	err := c.client.List(ctx, &aiServiceBackends, client.MatchingFields{k8sClientIndexBackendSecurityPolicyToReferencingAIServiceBackend: key})
	if err != nil {
		return fmt.Errorf("failed to list AIServiceBackendList: %w", err)
	}

	var errs []error
	for i := range aiServiceBackends.Items {
		aiBackend := &aiServiceBackends.Items[i]
		c.logger.Info("Syncing AIServiceBackend", "namespace", aiBackend.Namespace, "name", aiBackend.Name)
		if err = c.syncAIServiceBackend(ctx, aiBackend); err != nil {
			errs = append(errs, fmt.Errorf("%s/%s: %w", aiBackend.Namespace, aiBackend.Name, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

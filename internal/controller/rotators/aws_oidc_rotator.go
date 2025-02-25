// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package rotators

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// AWSOIDCRotator implements the Rotator interface for AWS OIDC token exchange.
// It manages the lifecycle of temporary AWS credentials obtained through OIDC token
// exchange with AWS STS.
type AWSOIDCRotator struct {
	// client is used for Kubernetes API operations.
	client client.Client
	// kube provides additional Kubernetes API capabilities.
	kube kubernetes.Interface
	// logger is used for structured logging.
	logger logr.Logger
	// stsClient provides AWS STS operations interface.
	stsClient STSClient
	// backendSecurityPolicyName provides name of backend security policy.
	backendSecurityPolicyName string
	// backendSecurityPolicyNamespace provides namespace of backend security policy.
	backendSecurityPolicyNamespace string
	// preRotationWindow specifies how long before expiry to rotate.
	preRotationWindow time.Duration
	// roleArn is the role ARN used to obtain credentials.
	roleArn string
	// region is the AWS region for the credentials.
	region string
}

// NewAWSOIDCRotator creates a new AWS OIDC rotator with the specified configuration.
// It initializes the AWS STS client and sets up the rotation channels.
func NewAWSOIDCRotator(
	ctx context.Context,
	client client.Client,
	stsClient STSClient,
	kube kubernetes.Interface,
	logger logr.Logger,
	backendSecurityPolicyNamespace string,
	backendSecurityPolicyName string,
	preRotationWindow time.Duration,
	roleArn string,
	region string,
) (*AWSOIDCRotator, error) {
	cfg, err := defaultAWSConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	cfg.Region = region

	// TODO XL - https://bbgithub.dev.bloomberg.com/cncs-ai-gateway/helm-ai-gateway-resources/blob/main/values.yaml#L18
	if proxyURL := os.Getenv("AI_GATEWAY_STS_PROXY_URL"); proxyURL != "" {
		cfg.HTTPClient = &http.Client{
			Transport: &http.Transport{
				Proxy: func(*http.Request) (*url.URL, error) {
					return url.Parse(proxyURL)
				},
			},
		}
	}
	if stsClient == nil {
		stsClient = NewSTSClient(cfg)
	}
	return &AWSOIDCRotator{
		client:                         client,
		kube:                           kube,
		logger:                         logger.WithName("aws-oidc-rotator"),
		stsClient:                      stsClient,
		backendSecurityPolicyNamespace: backendSecurityPolicyNamespace,
		backendSecurityPolicyName:      backendSecurityPolicyName,
		preRotationWindow:              preRotationWindow,
		roleArn:                        roleArn,
		region:                         region,
	}, nil
}

// IsExpired checks if the preRotation time is before the current time.
func (r *AWSOIDCRotator) IsExpired(preRotationExpirationTime time.Time) bool {
	return IsBufferedTimeExpired(0, preRotationExpirationTime)
}

// GetPreRotationTime gets the expiration time minus the preRotation interval or return zero value for time.
func (r *AWSOIDCRotator) GetPreRotationTime(ctx context.Context) (time.Time, error) {
	secret, err := LookupSecret(ctx, r.client, r.backendSecurityPolicyNamespace, GetBSPSecretName(r.backendSecurityPolicyName))
	if err != nil {
		// return zero value for time if secret has not been created.
		if apierrors.IsNotFound(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	expirationTime, err := GetExpirationSecretAnnotation(secret)
	if err != nil {
		return time.Time{}, err
	}
	preRotationTime := expirationTime.Add(-r.preRotationWindow)
	return preRotationTime, nil
}

// populateSecretWithAwsIdentity populates secret with aws identity credential info e.g. expiration time, access key, secret key and session token.
func populateSecretWithAwsIdentity(secret *corev1.Secret, awsIdentity *sts.AssumeRoleWithWebIdentityOutput, region string) {
	updateExpirationSecretAnnotation(secret, *awsIdentity.Credentials.Expiration)
	// For now have profile as default.
	const defaultProfile = "default"
	credsFile := awsCredentialsFile{awsCredentials{
		profile:         defaultProfile,
		accessKeyID:     aws.ToString(awsIdentity.Credentials.AccessKeyId),
		secretAccessKey: aws.ToString(awsIdentity.Credentials.SecretAccessKey),
		sessionToken:    aws.ToString(awsIdentity.Credentials.SessionToken),
		region:          region,
	}}
	updateAWSCredentialsInSecret(secret, &credsFile)
}

// Rotate implements aws credential secret upsert operation to k8s secret store.
//
// This implements [Rotator.Rotate].
func (r *AWSOIDCRotator) Rotate(ctx context.Context, token string) error {
	namespace := r.backendSecurityPolicyNamespace
	bspName := r.backendSecurityPolicyName
	r.logger.Info("rotating aws sts credentials", "namespace", namespace, "name", bspName)

	r.logger.Info("rotating aws credentials secret", "namespace", bspNamespace, "name", bspName)
	awsIdentity, err := r.assumeRoleWithToken(ctx, token)
	if err != nil {
		r.logger.Error(err, "failed to assume role", "role", r.roleArn, "oidc access token", token)
		return err
	}

	secret, err := LookupSecret(ctx, r.client, namespace, GetBSPSecretName(bspName))
	if err != nil {
		// If error is not `NotFound` error, return
		if !apierrors.IsNotFound(err) {
			return err
		}
		// Otherwise, it's NotFound, create secret
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      GetBSPSecretName(bspName),
				Namespace: namespace,
			},
			Type: corev1.SecretTypeOpaque,
			Data: make(map[string][]byte),
		}
	}

	updateExpirationSecretAnnotation(secret, *result.Credentials.Expiration)

	// For now have profile as default.
	// TODO XL - remove when PR, construct awsCredentials object
	const defaultProfile = "default"
	credsFile := awsCredentialsFile{awsCredentials{
		profile:         defaultProfile,
		accessKeyID:     aws.ToString(result.Credentials.AccessKeyId),
		secretAccessKey: aws.ToString(result.Credentials.SecretAccessKey),
		sessionToken:    aws.ToString(result.Credentials.SessionToken),
		region:          r.region,
	}}

	updateAWSCredentialsInSecret(secret, &credsFile)

	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return r.client.Update(ctx, secret)
		}
		r.logger.Error(err, "failed to lookup aws credentials secret", "namespace", bspNamespace, "name", bspName)
		return err
	}
	r.logger.Info("updating existing aws credential secret", "namespace", bspNamespace, "name", bspName)
	populateSecretWithAwsIdentity(secret, awsIdentity, r.region)
	return r.client.Update(ctx, secret)
}

// assumeRoleWithToken exchanges an OIDC token for AWS credentials.
func (r *AWSOIDCRotator) assumeRoleWithToken(ctx context.Context, token string) (*sts.AssumeRoleWithWebIdentityOutput, error) {
	return r.stsClient.AssumeRoleWithWebIdentity(ctx, &sts.AssumeRoleWithWebIdentityInput{
		RoleArn:          aws.String(r.roleArn),
		WebIdentityToken: aws.String(token),
		RoleSessionName:  aws.String(fmt.Sprintf(awsSessionNameFormat, r.backendSecurityPolicyName)),
	})
}

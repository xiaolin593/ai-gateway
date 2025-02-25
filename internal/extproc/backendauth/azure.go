// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package backendauth

import (
	"context"
	"fmt"
	"github.com/envoyproxy/ai-gateway/filterapi"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"os"
	"strings"
)

type azureHandler struct {
	// TODO XL - see below comments
	// credentials       azidentity.ClientSecretCredential
	// azureResourceName string //  https://<azure_resource_name>.openai.azure.com e.g. https://llmgw-openai-azure-eastus-prod.openai.azure.com/
	// below is required in path
	// apiVersion   string // need a mapping between deployment
	// deploymentId string // Azure model deployment, essential canonical model name

	// do I need to modify request path because Azure endpoint/path is different from OpenAI's /v1/chat/completion
	// vs /openai/deployments/<deployment-id>/chat/completion?api-version=<api_version>
	azureAccessToken string
}

func newAzureHandler(azureAuth *filterapi.AzureAuth) (Handler, error) {
	secret, err := os.ReadFile(azureAuth.Filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read azure access token file: %w", err)
	}
	// TODO XL - need to inspect the secret value's field `azure_access_token`
	// for now, just read the raw value
	return &azureHandler{azureAccessToken: strings.TrimSpace(string(secret))}, nil
}

func (a *azureHandler) Do(ctx context.Context, requestHeaders map[string]string, headerMut *extprocv3.HeaderMutation, bodyMut *extprocv3.BodyMutation) error {

	requestHeaders["Authorization"] = fmt.Sprintf("Bearer %s", a.azureAccessToken)
	headerMut.SetHeaders = append(headerMut.SetHeaders, &corev3.HeaderValueOption{
		Header: &corev3.HeaderValue{Key: "Authorization", RawValue: []byte(requestHeaders["Authorization"])},
	})
	return nil
}

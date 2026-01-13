// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

// ProcessorFactory is the factory function used to create new instances of a processor.
type ProcessorFactory func(_ *filterapi.RuntimeConfig, _ map[string]string, _ *slog.Logger, isUpstreamFilter bool) (Processor, error)

// Processor is the interface for the processor which corresponds to a single gRPC stream per the external processor filter.
// This decouples the processor implementation detail from the server implementation.
//
// This can be either a router filter level processor or an upstream filter level processor.
type Processor interface {
	// ProcessRequestHeaders processes the request headers message.
	ProcessRequestHeaders(context.Context, *corev3.HeaderMap) (*extprocv3.ProcessingResponse, error)
	// ProcessRequestBody processes the request body message.
	ProcessRequestBody(context.Context, *extprocv3.HttpBody) (*extprocv3.ProcessingResponse, error)
	// ProcessResponseHeaders processes the response headers message.
	ProcessResponseHeaders(context.Context, *corev3.HeaderMap) (*extprocv3.ProcessingResponse, error)
	// ProcessResponseBody processes the response body message.
	ProcessResponseBody(context.Context, *extprocv3.HttpBody) (*extprocv3.ProcessingResponse, error)
	// SetBackend instructs the processor to set the backend to use for the request. This is only called
	// when the processor is used in the upstream filter.
	//
	// routerProcessor is the processor that is the "parent" which was used to determine the route at the
	// router level. It holds the additional state that can be used to determine the backend to use.
	SetBackend(ctx context.Context, backend *filterapi.Backend, handler filterapi.BackendAuthHandler, routerProcessor Processor) error
}

// passThroughProcessor implements the Processor interface.
type passThroughProcessor struct{}

// ProcessRequestHeaders implements [Processor.ProcessRequestHeaders].
func (p passThroughProcessor) ProcessRequestHeaders(context.Context, *corev3.HeaderMap) (*extprocv3.ProcessingResponse, error) {
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestHeaders{}}, nil
}

// ProcessRequestBody implements [Processor.ProcessRequestBody].
func (p passThroughProcessor) ProcessRequestBody(context.Context, *extprocv3.HttpBody) (*extprocv3.ProcessingResponse, error) {
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestBody{}}, nil
}

// ProcessResponseHeaders implements [Processor.ProcessResponseHeaders].
func (p passThroughProcessor) ProcessResponseHeaders(context.Context, *corev3.HeaderMap) (*extprocv3.ProcessingResponse, error) {
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseHeaders{}}, nil
}

// ProcessResponseBody implements [Processor.ProcessResponseBody].
func (p passThroughProcessor) ProcessResponseBody(context.Context, *extprocv3.HttpBody) (*extprocv3.ProcessingResponse, error) {
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseBody{}}, nil
}

// SetBackend implements [Processor.SetBackend].
func (p passThroughProcessor) SetBackend(context.Context, *filterapi.Backend, filterapi.BackendAuthHandler, Processor) error {
	return nil
}

// authPassThroughProcessor passes through requests but injects backend authentication.
type authPassThroughProcessor struct {
	backendHandler filterapi.BackendAuthHandler
	requestHeaders map[string]string
	isUpstream     bool
}

// ProcessRequestHeaders implements [Processor.ProcessRequestHeaders].
func (p *authPassThroughProcessor) ProcessRequestHeaders(ctx context.Context, headers *corev3.HeaderMap) (*extprocv3.ProcessingResponse, error) {
	// For router filter, just pass through
	if !p.isUpstream {
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestHeaders{}}, nil
	}

	// For upstream filter, inject backend auth
	resp := &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestHeaders{
			RequestHeaders: &extprocv3.HeadersResponse{},
		},
	}

	if p.backendHandler != nil {
		fmt.Fprintf(os.Stderr, "DEBUG: Calling backend auth handler\n")
		authHeaders, err := p.backendHandler.Do(ctx, p.requestHeaders, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "DEBUG: Backend auth failed: %v\n", err)
			return nil, fmt.Errorf("failed to do backend auth: %w", err)
		}
		fmt.Fprintf(os.Stderr, "DEBUG: Backend auth returned %d headers\n", len(authHeaders))

		// Add auth headers to the response
		for _, h := range authHeaders {
			fmt.Fprintf(os.Stderr, "DEBUG: Adding auth header: %s\n", h.Key())
			resp.GetRequestHeaders().Response = &extprocv3.CommonResponse{
				HeaderMutation: &extprocv3.HeaderMutation{
					SetHeaders: append(resp.GetRequestHeaders().GetResponse().GetHeaderMutation().GetSetHeaders(),
						&corev3.HeaderValueOption{
							AppendAction: corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD,
							Header:       &corev3.HeaderValue{Key: h.Key(), RawValue: []byte(h.Value())},
						}),
				},
			}
		}
	} else {
		fmt.Fprintf(os.Stderr, "DEBUG: No backend handler set - SetBackend may not have been called\n")
	}

	return resp, nil
}

// ProcessRequestBody implements [Processor.ProcessRequestBody].
func (p *authPassThroughProcessor) ProcessRequestBody(context.Context, *extprocv3.HttpBody) (*extprocv3.ProcessingResponse, error) {
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestBody{}}, nil
}

// ProcessResponseHeaders implements [Processor.ProcessResponseHeaders].
func (p *authPassThroughProcessor) ProcessResponseHeaders(context.Context, *corev3.HeaderMap) (*extprocv3.ProcessingResponse, error) {
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseHeaders{}}, nil
}

// ProcessResponseBody implements [Processor.ProcessResponseBody].
func (p *authPassThroughProcessor) ProcessResponseBody(context.Context, *extprocv3.HttpBody) (*extprocv3.ProcessingResponse, error) {
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseBody{}}, nil
}

// SetBackend implements [Processor.SetBackend].
func (p *authPassThroughProcessor) SetBackend(ctx context.Context, backend *filterapi.Backend, handler filterapi.BackendAuthHandler, routerProcessor Processor) error {
	p.backendHandler = handler
	return nil
}

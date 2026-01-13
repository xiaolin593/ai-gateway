// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"log/slog"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

// NewBatchesProcessor creates a new processor for the /v1/batches endpoint.
// It returns a pass-through processor that forwards requests to OpenAI with auth injection.
func NewBatchesProcessor(config *filterapi.RuntimeConfig, headers map[string]string, logger *slog.Logger, isUpstreamFilter bool) (Processor, error) {
	return &authPassThroughProcessor{
		requestHeaders: headers,
		isUpstream:     isUpstreamFilter,
	}, nil
}

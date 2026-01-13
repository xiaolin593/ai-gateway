// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"context"
	"log/slog"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestBatchesProcessor(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(nil, nil))
	config := &filterapi.RuntimeConfig{}

	processor, err := NewBatchesProcessor(config, nil, logger, false)
	if err != nil {
		t.Fatalf("Failed to create batches processor: %v", err)
	}

	bp, ok := processor.(*batchesProcessor)
	if !ok {
		t.Fatalf("Expected batchesProcessor, got %T", processor)
	}

	tests := []struct {
		name           string
		method         string
		path           string
		expectedStatus int
		checkResponse  func(t *testing.T, resp *extprocv3.ProcessingResponse)
	}{
		{
			name:           "List batches",
			method:         "GET",
			path:           "/v1/batches",
			expectedStatus: 200,
			checkResponse: func(t *testing.T, resp *extprocv3.ProcessingResponse) {
				immediate := resp.GetImmediateResponse()
				if immediate == nil {
					t.Fatal("Expected immediate response")
				}

				var listResp map[string]interface{}
				if err := json.Unmarshal(immediate.Body, &listResp); err != nil {
					t.Fatalf("Failed to unmarshal response: %v", err)
				}

				if listResp["object"] != "list" {
					t.Errorf("Expected object to be 'list', got %v", listResp["object"])
				}
			},
		},
		{
			name:           "Get non-existent batch",
			method:         "GET",
			path:           "/v1/batches/nonexistent",
			expectedStatus: 404,
			checkResponse: func(t *testing.T, resp *extprocv3.ProcessingResponse) {
				immediate := resp.GetImmediateResponse()
				if immediate == nil {
					t.Fatal("Expected immediate response")
				}

				var errorResp map[string]interface{}
				if err := json.Unmarshal(immediate.Body, &errorResp); err != nil {
					t.Fatalf("Failed to unmarshal response: %v", err)
				}

				errorObj, ok := errorResp["error"].(map[string]interface{})
				if !ok {
					t.Fatal("Expected error object in response")
				}

				if errorObj["code"] != "not_found" {
					t.Errorf("Expected error code 'not_found', got %v", errorObj["code"])
				}
			},
		},
		{
			name:           "Unsupported method",
			method:         "PUT",
			path:           "/v1/batches",
			expectedStatus: 405,
			checkResponse: func(t *testing.T, resp *extprocv3.ProcessingResponse) {
				immediate := resp.GetImmediateResponse()
				if immediate == nil {
					t.Fatal("Expected immediate response")
				}

				var errorResp map[string]interface{}
				if err := json.Unmarshal(immediate.Body, &errorResp); err != nil {
					t.Fatalf("Failed to unmarshal response: %v", err)
				}

				errorObj, ok := errorResp["error"].(map[string]interface{})
				if !ok {
					t.Fatal("Expected error object in response")
				}

				if errorObj["code"] != "method_not_allowed" {
					t.Errorf("Expected error code 'method_not_allowed', got %v", errorObj["code"])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := &corev3.HeaderMap{
				Headers: []*corev3.HeaderValue{
					{Key: ":method", RawValue: []byte(tt.method)},
					{Key: ":path", RawValue: []byte(tt.path)},
				},
			}

			resp, err := bp.ProcessRequestHeaders(context.Background(), headers)
			if err != nil {
				t.Fatalf("ProcessRequestHeaders failed: %v", err)
			}

			immediate := resp.GetImmediateResponse()
			if immediate == nil {
				t.Fatal("Expected immediate response")
			}

			if int32(immediate.Status.Code) != int32(tt.expectedStatus) {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, immediate.Status.Code)
			}

			if tt.checkResponse != nil {
				tt.checkResponse(t, resp)
			}
		})
	}
}

func TestBatchesProcessorBatchCreation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(nil, nil))
	config := &filterapi.RuntimeConfig{}

	processor, err := NewBatchesProcessor(config, nil, logger, false)
	if err != nil {
		t.Fatalf("Failed to create batches processor: %v", err)
	}

	bp, ok := processor.(*batchesProcessor)
	if !ok {
		t.Fatalf("Expected batchesProcessor, got %T", processor)
	}

	// Test batch creation workflow
	headers := &corev3.HeaderMap{
		Headers: []*corev3.HeaderValue{
			{Key: ":method", RawValue: []byte("POST")},
			{Key: ":path", RawValue: []byte("/v1/batches")},
			{Key: "content-type", RawValue: []byte("application/json")},
		},
	}

	// First, ProcessRequestHeaders should pass through to ProcessRequestBody
	resp, err := bp.ProcessRequestHeaders(context.Background(), headers)
	if err != nil {
		t.Fatalf("ProcessRequestHeaders failed: %v", err)
	}

	if resp.GetRequestBody() == nil {
		t.Fatalf("Expected request body processing for POST request, got response: %+v", resp.Response)
	}

	// Then, ProcessRequestBody should create a batch
	batchRequest := map[string]interface{}{
		"input_file_id":      "file-test123",
		"endpoint":           "/v1/chat/completions",
		"completion_window":  "24h",
	}

	requestBody, err := json.Marshal(batchRequest)
	if err != nil {
		t.Fatalf("Failed to marshal batch request: %v", err)
	}

	body := &extprocv3.HttpBody{
		Body: requestBody,
	}

	resp, err = bp.ProcessRequestBody(context.Background(), body)
	if err != nil {
		t.Fatalf("ProcessRequestBody failed: %v", err)
	}

	immediate := resp.GetImmediateResponse()
	if immediate == nil {
		t.Fatal("Expected immediate response")
	}

	if int32(immediate.Status.Code) != 200 {
		t.Errorf("Expected status 200, got %d", immediate.Status.Code)
	}

	var batchResp map[string]interface{}
	if err := json.Unmarshal(immediate.Body, &batchResp); err != nil {
		t.Fatalf("Failed to unmarshal batch response: %v", err)
	}

	if batchResp["object"] != "batch" {
		t.Errorf("Expected object to be 'batch', got %v", batchResp["object"])
	}

	if batchResp["status"] != "validating" {
		t.Errorf("Expected status to be 'validating', got %v", batchResp["status"])
	}

	if batchResp["input_file_id"] != "file-test123" {
		t.Errorf("Expected input_file_id to be 'file-test123', got %v", batchResp["input_file_id"])
	}

	batchID, ok := batchResp["id"].(string)
	if !ok {
		t.Fatal("Expected batch ID to be string")
	}

	// Now test getting the created batch
	headers = &corev3.HeaderMap{
		Headers: []*corev3.HeaderValue{
			{Key: ":method", RawValue: []byte("GET")},
			{Key: ":path", RawValue: []byte("/v1/batches/" + batchID)},
		},
	}

	resp, err = bp.ProcessRequestHeaders(context.Background(), headers)
	if err != nil {
		t.Fatalf("ProcessRequestHeaders failed: %v", err)
	}

	immediate = resp.GetImmediateResponse()
	if immediate == nil {
		t.Fatal("Expected immediate response")
	}

	if int32(immediate.Status.Code) != 200 {
		t.Errorf("Expected status 200, got %d", immediate.Status.Code)
	}

	var getBatchResp map[string]interface{}
	if err := json.Unmarshal(immediate.Body, &getBatchResp); err != nil {
		t.Fatalf("Failed to unmarshal get batch response: %v", err)
	}

	if getBatchResp["id"] != batchID {
		t.Errorf("Expected batch ID %s, got %v", batchID, getBatchResp["id"])
	}
}

func TestBatchesProcessorInvalidRequests(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(nil, nil))
	config := &filterapi.RuntimeConfig{}

	processor, err := NewBatchesProcessor(config, nil, logger, false)
	if err != nil {
		t.Fatalf("Failed to create batches processor: %v", err)
	}

	bp, ok := processor.(*batchesProcessor)
	if !ok {
		t.Fatalf("Expected batchesProcessor, got %T", processor)
	}

	tests := []struct {
		name        string
		requestBody map[string]interface{}
		expectError string
	}{
		{
			name: "Missing input_file_id",
			requestBody: map[string]interface{}{
				"endpoint":          "/v1/chat/completions",
				"completion_window": "24h",
			},
			expectError: "input_file_id is required",
		},
		{
			name: "Missing endpoint",
			requestBody: map[string]interface{}{
				"input_file_id":     "file-test123",
				"completion_window": "24h",
			},
			expectError: "endpoint is required",
		},
		{
			name: "Missing completion_window",
			requestBody: map[string]interface{}{
				"input_file_id": "file-test123",
				"endpoint":      "/v1/chat/completions",
			},
			expectError: "completion_window is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestBody, err := json.Marshal(tt.requestBody)
			if err != nil {
				t.Fatalf("Failed to marshal batch request: %v", err)
			}

			body := &extprocv3.HttpBody{
				Body: requestBody,
			}

			resp, err := bp.ProcessRequestBody(context.Background(), body)
			if err != nil {
				t.Fatalf("ProcessRequestBody failed: %v", err)
			}

			immediate := resp.GetImmediateResponse()
			if immediate == nil {
				t.Fatal("Expected immediate response")
			}

			if int32(immediate.Status.Code) != 400 {
				t.Errorf("Expected status 400, got %d", immediate.Status.Code)
			}

			var errorResp map[string]interface{}
			if err := json.Unmarshal(immediate.Body, &errorResp); err != nil {
				t.Fatalf("Failed to unmarshal error response: %v", err)
			}

			errorObj, ok := errorResp["error"].(map[string]interface{})
			if !ok {
				t.Fatal("Expected error object in response")
			}

			if errorObj["message"] != tt.expectError {
				t.Errorf("Expected error message '%s', got %v", tt.expectError, errorObj["message"])
			}
		})
	}
}
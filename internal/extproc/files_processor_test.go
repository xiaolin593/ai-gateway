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

func TestFilesProcessor(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(nil, nil))
	config := &filterapi.RuntimeConfig{}

	processor, err := NewFilesProcessor(config, nil, logger, false)
	if err != nil {
		t.Fatalf("Failed to create files processor: %v", err)
	}

	fp, ok := processor.(*filesProcessor)
	if !ok {
		t.Fatalf("Expected filesProcessor, got %T", processor)
	}

	tests := []struct {
		name           string
		method         string
		path           string
		expectedStatus int
		checkResponse  func(t *testing.T, resp *extprocv3.ProcessingResponse)
	}{
		{
			name:           "List files",
			method:         "GET",
			path:           "/v1/files",
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
			name:           "Get non-existent file",
			method:         "GET",
			path:           "/v1/files/nonexistent",
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
			name:           "Delete non-existent file",
			method:         "DELETE",
			path:           "/v1/files/nonexistent",
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
			path:           "/v1/files",
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

			resp, err := fp.ProcessRequestHeaders(context.Background(), headers)
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

func TestFilesProcessorFileUpload(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(nil, nil))
	config := &filterapi.RuntimeConfig{}

	processor, err := NewFilesProcessor(config, nil, logger, false)
	if err != nil {
		t.Fatalf("Failed to create files processor: %v", err)
	}

	fp, ok := processor.(*filesProcessor)
	if !ok {
		t.Fatalf("Expected filesProcessor, got %T", processor)
	}

	// Test file upload workflow
	headers := &corev3.HeaderMap{
		Headers: []*corev3.HeaderValue{
			{Key: ":method", RawValue: []byte("POST")},
			{Key: ":path", RawValue: []byte("/v1/files")},
			{Key: "content-type", RawValue: []byte("multipart/form-data")},
		},
	}

	// First, ProcessRequestHeaders should pass through to ProcessRequestBody
	resp, err := fp.ProcessRequestHeaders(context.Background(), headers)
	if err != nil {
		t.Fatalf("ProcessRequestHeaders failed: %v", err)
	}

	if resp.GetRequestBody() == nil {
		t.Fatalf("Expected request body processing for POST request, got response: %+v", resp.Response)
	}

	// Then, ProcessRequestBody should create a file
	mockFileContent := `{"custom_id": "test", "method": "POST", "url": "/v1/chat/completions", "body": {}}`
	body := &extprocv3.HttpBody{
		Body: []byte(mockFileContent),
	}

	resp, err = fp.ProcessRequestBody(context.Background(), body)
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

	var fileResp map[string]interface{}
	if err := json.Unmarshal(immediate.Body, &fileResp); err != nil {
		t.Fatalf("Failed to unmarshal file response: %v", err)
	}

	if fileResp["object"] != "file" {
		t.Errorf("Expected object to be 'file', got %v", fileResp["object"])
	}

	fileID, ok := fileResp["id"].(string)
	if !ok {
		t.Fatal("Expected file ID to be string")
	}

	// Now test getting the created file
	headers = &corev3.HeaderMap{
		Headers: []*corev3.HeaderValue{
			{Key: ":method", RawValue: []byte("GET")},
			{Key: ":path", RawValue: []byte("/v1/files/" + fileID)},
		},
	}

	resp, err = fp.ProcessRequestHeaders(context.Background(), headers)
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

	var getFileResp map[string]interface{}
	if err := json.Unmarshal(immediate.Body, &getFileResp); err != nil {
		t.Fatalf("Failed to unmarshal get file response: %v", err)
	}

	if getFileResp["id"] != fileID {
		t.Errorf("Expected file ID %s, got %v", fileID, getFileResp["id"])
	}
}
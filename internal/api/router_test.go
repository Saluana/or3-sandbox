package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"or3-sandbox/internal/model"
)

func TestDecodeJSONLimitedRejectsOversizeBody(t *testing.T) {
	request := httptest.NewRequest(http.MethodPut, "/v1/sandboxes/sbx/files/test.txt", strings.NewReader(`{"content":"hello world"}`))
	recorder := httptest.NewRecorder()
	var payload map[string]any
	err := decodeJSONLimited(recorder, request, 8, &payload)
	if err == nil {
		t.Fatal("expected oversize body error")
	}
	if _, ok := err.(*http.MaxBytesError); !ok {
		t.Fatalf("expected MaxBytesError, got %T", err)
	}
}

func TestDecodeJSONRejectsTrailingPayload(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes", strings.NewReader(`{"name":"one"}{"name":"two"}`))
	var payload map[string]any
	err := decodeJSON(request, &payload)
	if err == nil || !strings.Contains(err.Error(), "single JSON value") {
		t.Fatalf("expected trailing payload rejection, got %v", err)
	}
}

func TestClassifyErrorMapsOversizeFileTransfer(t *testing.T) {
	status, code, message := classifyError(model.FileTransferTooLargeError(model.DefaultWorkspaceFileTransferMaxBytes))
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("unexpected status %d", status)
	}
	if code != "payload_too_large" {
		t.Fatalf("unexpected code %q", code)
	}
	if !strings.Contains(message, "maximum transfer size") {
		t.Fatalf("unexpected message %q", message)
	}
}

func TestWorkspaceFileUploadBodyBytesTracksDecodedLimit(t *testing.T) {
	got := workspaceFileUploadBodyBytes(64 * 1024 * 1024)
	if got <= 64*1024*1024 {
		t.Fatalf("expected upload body cap to exceed decoded payload, got %d", got)
	}
}

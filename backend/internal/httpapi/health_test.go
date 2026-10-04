package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthz(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	newHandlerWithVerifiedRepositories().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}

	if contentType := response.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
		t.Fatalf("expected JSON content type, got %q", contentType)
	}

	var body healthResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body.Status != "ok" {
		t.Errorf("expected status ok, got %q", body.Status)
	}
	if body.Service != "agent-platform-api" {
		t.Errorf("expected service agent-platform-api, got %q", body.Service)
	}
}

func TestHealthzRejectsNonGetRequests(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	response := httptest.NewRecorder()

	newHandlerWithVerifiedRepositories().ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, response.Code)
	}
	if allow := response.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("expected Allow header %q, got %q", http.MethodGet, allow)
	}
}

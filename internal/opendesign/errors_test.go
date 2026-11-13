package opendesign

import (
	"net/http"
	"testing"
)

func TestUpstreamErrorShapes(t *testing.T) {
	for _, tc := range []struct {
		status        int
		body          string
		code, message string
		mapped        int
	}{
		{404, `{"error":{"code":"model_not_found","message":"[code=model_not_found] The requested model was not found.","type":"model_not_found"}}`, "model_not_found", "opendesign: The requested model was not found.", 400},
		{404, `{"type":"error","error":{"type":"not_found_error","message":"The requested model was not found.","code":"model_not_found"}}`, "model_not_found", "opendesign: The requested model was not found.", 400},
		{401, `{"error":{"code":"missing_api_key","message":"API key required"}}`, "missing_api_key", "opendesign: API key required", 401},
		{429, `{"error":{"type":"rate_limit_error","message":"5-hour limit reached"}}`, "rate_limit_error", "opendesign: 5-hour limit reached", 429},
		{502, `bad gateway`, "upstream_502", "opendesign: bad gateway", 502},
	} {
		perr := UpstreamError(tc.status, http.Header{"Retry-After": {"30"}}, []byte(tc.body), EndpointChat)
		if perr.Code != tc.code || perr.Message != tc.message || perr.HTTPStatus != tc.mapped {
			t.Fatalf("%d %s: got %+v", tc.status, tc.body, perr)
		}
		if tc.status == 429 && (perr.RetryAfterSeconds != 30 || !perr.Retryable) {
			t.Fatalf("429: got %+v", perr)
		}
	}
}

func TestEndpointForFormat(t *testing.T) {
	for format, want := range map[string]string{
		"chat-completions": EndpointChat,
		"openai":           EndpointChat,
		"responses":        EndpointResponses,
		"anthropic":        EndpointMessages,
		"claude":           EndpointMessages,
	} {
		if got, ok := EndpointForFormat(format); !ok || got != want {
			t.Fatalf("EndpointForFormat(%q) = %q, %t", format, got, ok)
		}
	}
	if _, ok := EndpointForFormat("gemini"); ok {
		t.Fatal("gemini must be unsupported")
	}
}

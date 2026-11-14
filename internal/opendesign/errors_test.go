package opendesign

import (
	"net/http"
	"testing"

	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/berry-shake/cpa-provider-core/upstream"
)

func TestUpstreamErrorShapes(t *testing.T) {
	errs := upstream.Errors{Prefix: "opendesign: ", Remap: New().RemapError}
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
		{400, `{"error":"[code=bad] "}`, "upstream_400", "opendesign: [code=bad]", 400},
	} {
		perr := errs.Upstream(tc.status, http.Header{"Retry-After": {"30"}}, []byte(tc.body))
		if perr.Code != tc.code || perr.Message != tc.message || perr.HTTPStatus != tc.mapped {
			t.Fatalf("%d %s: got %+v", tc.status, tc.body, perr)
		}
		if tc.status == 429 && (perr.RetryAfterSeconds != 30 || !perr.Retryable) {
			t.Fatalf("429: got %+v", perr)
		}
	}
}

func TestEndpoint(t *testing.T) {
	a := New()
	for protocol, want := range map[provider.Protocol]string{
		provider.Chat:      EndpointChat,
		provider.Responses: EndpointResponses,
		provider.Messages:  EndpointMessages,
	} {
		if got := a.Endpoint(protocol); got != want {
			t.Errorf("Endpoint(%s) = %q, want %q", protocol, got, want)
		}
	}
}

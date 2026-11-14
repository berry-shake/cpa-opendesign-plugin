package opendesign

import (
	"errors"
	"testing"

	"github.com/berry-shake/cpa-provider-core/config"
	"github.com/berry-shake/cpa-provider-core/payload"
	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestConfigProfile(t *testing.T) {
	profile := New().Spec().Config
	cfg := config.Default(profile)
	if cfg.BaseURL != "https://amr-link.open-design.ai" || cfg.ModelPrefix != "opendesign" {
		t.Fatalf("defaults = %+v", cfg)
	}
	if profile.DataDir != "plugins/opendesign-data" || profile.Database != "opendesign.db" {
		t.Fatalf("profile = %+v", profile)
	}
	for raw, want := range map[string]string{
		"https://amr-link.open-design.ai":       "https://amr-link.open-design.ai",
		"https://amr-link.open-design.ai/":      "https://amr-link.open-design.ai",
		"https://amr-link.open-design.ai/v1":    "https://amr-link.open-design.ai",
		" https://amr-link.open-design.ai/v1/ ": "https://amr-link.open-design.ai",
		"http://127.0.0.1:8080/proxy/v1?x=1":    "http://127.0.0.1:8080/proxy",
	} {
		got, errNormalize := config.NormalizeBaseURL(raw, profile.BaseURLSuffixes)
		if errNormalize != nil || got != want {
			t.Fatalf("NormalizeBaseURL(%q) = %q, %v; want %q", raw, got, errNormalize, want)
		}
	}
	for _, raw := range []string{"amr-link.open-design.ai", "ftp://amr-link.open-design.ai", "https://"} {
		if _, errNormalize := config.NormalizeBaseURL(raw, profile.BaseURLSuffixes); errNormalize == nil {
			t.Fatalf("NormalizeBaseURL(%q) must fail", raw)
		}
	}
}

// StreamFix repairs Messages streams only, and reads max_tokens from the
// request when its scan met no malformed JSON.
func TestStreamFixReadsMaxTokens(t *testing.T) {
	a := New()
	input := func(body string, scanErr error) provider.CallInput {
		members, _ := payload.Members([]byte(body), a.RequestMembers()...)
		return provider.CallInput{Request: pluginapi.ExecutorRequest{Payload: []byte(body)}, Members: members, ScanErr: scanErr}
	}
	if fix := a.StreamFix(provider.Chat, input(`{"max_tokens":64}`, nil)); fix != nil {
		t.Fatalf("chat fix = %#v", fix)
	}
	for body, want := range map[string]int64{
		`{"max_tokens":64}`:   64,
		`{"max_tokens":0}`:    0,
		`{"max_tokens":-3}`:   0,
		`{"max_tokens":"64"}`: 0,
		`{"model":"m"}`:       0,
	} {
		fix, ok := a.StreamFix(provider.Messages, input(body, nil)).(*messagesFix)
		if !ok || fix.maxTokens != want {
			t.Errorf("%s: fix = %#v, want max tokens %d", body, fix, want)
		}
	}
	fix, _ := a.StreamFix(provider.Messages, input(`{"max_tokens":64}`, errors.New("malformed"))).(*messagesFix)
	if fix == nil || fix.maxTokens != 0 {
		t.Fatalf("malformed body: fix = %#v", fix)
	}
}

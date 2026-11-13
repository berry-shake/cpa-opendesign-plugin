package config

import "testing"

func TestNormalizeBaseURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://amr-link.open-design.ai":       "https://amr-link.open-design.ai",
		"https://amr-link.open-design.ai/":      "https://amr-link.open-design.ai",
		"https://amr-link.open-design.ai/v1":    "https://amr-link.open-design.ai",
		" https://amr-link.open-design.ai/v1/ ": "https://amr-link.open-design.ai",
		"http://127.0.0.1:8080/proxy/v1?x=1":    "http://127.0.0.1:8080/proxy",
	} {
		got, errNormalize := NormalizeBaseURL(raw)
		if errNormalize != nil || got != want {
			t.Fatalf("NormalizeBaseURL(%q) = %q, %v; want %q", raw, got, errNormalize, want)
		}
	}
	for _, raw := range []string{"amr-link.open-design.ai", "ftp://amr-link.open-design.ai", "https://"} {
		if _, errNormalize := NormalizeBaseURL(raw); errNormalize == nil {
			t.Fatalf("NormalizeBaseURL(%q) must fail", raw)
		}
	}
}

func TestDefaults(t *testing.T) {
	cfg := Default()
	if cfg.BaseURL != "https://amr-link.open-design.ai" || cfg.ModelPrefix != "opendesign" {
		t.Fatalf("defaults = %+v", cfg)
	}
}

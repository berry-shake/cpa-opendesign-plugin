package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const goList = `{"object":"list","data":[
 {"id":"deepseek-v4-pro","object":"model","created":1760000000,"owned_by":"vela",
  "architecture":{"input_modalities":["text","image","file"],"output_modalities":["text"]},
  "metadata":{"cost":"Low","capability":"standard","context_limit":1000000,"context_budget":228000,"output_budget":128000},
  "default":true,"enabled":true},
 {"id":"glm-5.3-flash","created":1760000001,"architecture":{"input_modalities":["text"],"output_modalities":["text"]},"enabled":true},
 {"id":"glm-5.3-flash","created":1},
 {"id":"retired-model","architecture":{"output_modalities":["text"]},"enabled":false},
 {"id":"image-model","architecture":{"input_modalities":["text"],"output_modalities":["image"]},"enabled":true},
 {"id":" "}
]}`

const proList = `{"data":[
 {"id":"deepseek-v4-pro","created":1760000000,"architecture":{"input_modalities":["text","image","file"],"output_modalities":["text"]},
  "metadata":{"context_limit":1000000,"context_budget":228000,"output_budget":128000},"enabled":true},
 {"id":"claude-top-5","created":1760000002,"architecture":{"input_modalities":["text","image"],"output_modalities":["text"]},"enabled":true}
]}`

func TestParseModels(t *testing.T) {
	models, errParse := ParseModels([]byte(goList))
	if errParse != nil {
		t.Fatal(errParse)
	}
	if len(models) != 4 {
		t.Fatalf("got %d models, want 4 (duplicates and blank IDs dropped)", len(models))
	}
	pro := models[0]
	if pro.ID != "deepseek-v4-pro" || pro.ContextLength != 1000000 || pro.ContextBudget != 228000 || pro.MaxOutput != 128000 ||
		pro.CostTier != "low" || !pro.Default || !pro.Enabled || strings.Join(pro.Inputs, ",") != "text,image,file" || !pro.Chat() {
		t.Fatalf("model = %+v", pro)
	}
	if models[1].Created != 1760000001 {
		t.Fatalf("the first of two duplicate entries must win, got %+v", models[1])
	}
	if models[2].Chat() {
		t.Fatal("a disabled model must not count as a chat model")
	}
	if models[3].Chat() {
		t.Fatal("an image-only model must not count as a chat model")
	}
	if _, errParse := ParseModels([]byte(`{"data":[]}`)); errParse == nil {
		t.Fatal("an empty list must fail")
	}
	if _, errParse := ParseModels([]byte(`<html>`)); errParse == nil {
		t.Fatal("HTML must fail")
	}
}

func TestPerKeyLists(t *testing.T) {
	c := New()
	now := time.Unix(1_800_000_000, 0)
	if _, _, errStore := c.Store("go", []byte(goList), now); errStore != nil {
		t.Fatal(errStore)
	}
	goOnly := c.Current()
	goRevision := goOnly.RevisionFor("go")
	if goRevision == "" || !goOnly.KnowsKey("go") || goOnly.KnowsKey("pro") {
		t.Fatalf("revision %q, keys %d", goRevision, goOnly.Keys())
	}
	// A key whose list is not loaded yet registers the whole catalog.
	if goOnly.RevisionFor("pro") != goOnly.Revision {
		t.Fatal("an unknown key must follow the catalog revision")
	}
	if got := ids(goOnly.ModelInfos(true, nil, "pro")); got != "deepseek-v4-pro,glm-5.3-flash" {
		t.Fatalf("unknown key registers %s", got)
	}

	snapshot, changed, errStore := c.Store("pro", []byte(proList), now.Add(time.Minute))
	if errStore != nil || !changed {
		t.Fatalf("store pro: changed=%t err=%v", changed, errStore)
	}
	if len(snapshot.Models) != 5 || snapshot.Keys() != 2 {
		t.Fatalf("union has %d models and %d keys", len(snapshot.Models), snapshot.Keys())
	}
	shared, _ := snapshot.Lookup("deepseek-v4-pro")
	if strings.Join(shared.Keys, ",") != "go,pro" || !shared.ListedBy("pro") {
		t.Fatalf("shared model keys = %v", shared.Keys)
	}
	if top, _ := snapshot.Lookup("claude-top-5"); top.ListedBy("go") {
		t.Fatal("the Go key must not list the Pro-only model")
	}
	if got := ids(snapshot.ModelInfos(true, nil, "go")); got != "deepseek-v4-pro,glm-5.3-flash" {
		t.Fatalf("go key registers %s", got)
	}
	if got := ids(snapshot.ModelInfos(true, nil, "pro")); got != "claude-top-5,deepseek-v4-pro" {
		t.Fatalf("pro key registers %s", got)
	}
	// Selected models are registered as chosen, even when not listed.
	if got := ids(snapshot.ModelInfos(false, []string{"glm-5.3-flash", "unknown-x"}, "pro")); got != "glm-5.3-flash,unknown-x" {
		t.Fatalf("selected models register %s", got)
	}
	// Adding the Pro key leaves the Go key's revision alone.
	if snapshot.RevisionFor("go") != goRevision {
		t.Fatal("another key's list changed the Go key's revision")
	}

	snapshot = c.Retain(map[string]bool{"go": true})
	if snapshot.KnowsKey("pro") || len(snapshot.Models) != 4 {
		t.Fatalf("after retain: %d models, pro known %t", len(snapshot.Models), snapshot.KnowsKey("pro"))
	}
	if _, ok := snapshot.Lookup("claude-top-5"); ok {
		t.Fatal("the dropped key's models must leave the catalog")
	}
}

func TestFetchSendsTheKey(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(rw, r)
			return
		}
		auth = r.Header.Get("Authorization")
		if auth != "Bearer od-good" {
			rw.WriteHeader(http.StatusUnauthorized)
			_, _ = rw.Write([]byte(`{"error":{"code":"missing_api_key","message":"API key required"}}`))
			return
		}
		_, _ = rw.Write([]byte(goList))
	}))
	defer server.Close()
	c := New()
	now := time.Unix(1_800_000_000, 0)
	snapshot, _, errFetch := c.Fetch(context.Background(), server.Client(), server.URL, "od-good", "k1", "test", now)
	if errFetch != nil || !snapshot.KnowsKey("k1") {
		t.Fatalf("fetch: %v", errFetch)
	}
	snapshot, _, errFetch = c.Fetch(context.Background(), server.Client(), server.URL, "od-bad", "k2", "test", now)
	if errFetch == nil || !strings.Contains(errFetch.Error(), "401") || snapshot.KnowsKey("k2") || snapshot.LastError == "" {
		t.Fatalf("bad key: %v, last error %q", errFetch, snapshot.LastError)
	}
	if !snapshot.KnowsKey("k1") {
		t.Fatal("a failed fetch must keep the other lists")
	}
}

func TestUpstreamID(t *testing.T) {
	c := New()
	if _, _, errStore := c.Store("go", []byte(goList), time.Now()); errStore != nil {
		t.Fatal(errStore)
	}
	snapshot := c.Current()
	for model, want := range map[string]string{
		"opendesign/deepseek-v4-pro":     "deepseek-v4-pro",
		"deepseek-v4-pro":                "deepseek-v4-pro",
		"other/deepseek-v4-pro":          "deepseek-v4-pro",
		"opendesign/glm-5.3-flash(high)": "glm-5.3-flash",
		"vendor/not-listed":              "vendor/not-listed",
	} {
		if got := snapshot.UpstreamID(model, "opendesign"); got != want {
			t.Fatalf("UpstreamID(%q) = %q, want %q", model, got, want)
		}
	}
}

func ids(infos []pluginapi.ModelInfo) string {
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.ID)
	}
	return strings.Join(out, ",")
}

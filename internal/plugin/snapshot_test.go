package plugin

// TestSnapshots locks the plugin's behaviour at its RPC boundary before the
// provider plugins move onto a shared core: it replays every fixture in
// testdata/snapshots through Service.Call (the path CPA uses via main.go) and
// compares every upstream HTTP request, every host callback and every RPC
// response with the recorded output.
//
//	go test ./internal/plugin -run TestSnapshots           compare
//	go test ./internal/plugin -run TestSnapshots -update   rewrite steps[].output
//
// The fixtures are the source of truth. With -update, cases of snapCases that
// have no fixture file yet are first written as input skeletons; only the
// steps[].output parts of existing files are rewritten.
//
// Inputs:
//   - setup.config is the plugin's config subtree. It reaches plugin.register
//     as config_yaml (JSON is YAML) with schema_version 10, as CPA v8 sends it.
//     The background catalog refresh the registration starts runs before the
//     credentials exist, as at CPA start-up.
//   - setup.credentials are auth files already in CPA's auth dir; they get
//     the auth indexes idx-1, idx-2, ... in order. "read_only": true makes
//     host.auth.save fail for that file, as a failed write does in CPA.
//   - upstream_script answers the plugin's requests (inference and the vela
//     account API share one origin), matched in order per method + path. An
//     unscripted request and an unused entry fail the case.
//   - rpc.request is shaped as CPA v8 sends it, except that the byte fields
//     CPA base64-encodes (snapByteKeys) are written decoded: a string holds the
//     exact bytes, an object or array stands for its compact JSON.
//   - Placeholders in config, credentials and requests: {{UPSTREAM}} (the fake upstream
//     origin), {{DATA_DIR}} (the case's data dir), {{login_id}} and
//     {{proxy_id}} (the latest generated ID of that kind).
//
// Outputs are canonical JSON (object keys sorted). They are what the plugin
// produced, except for these normalisation rules:
//  1. The fake upstream origin becomes {{UPSTREAM}}, its host:port
//     {{UPSTREAM_HOST}} and the data dir {{DATA_DIR}}.
//  2. Byte fields in responses and host callbacks are shown decoded: Body,
//     StorageJSON, RawJSON, storage_json and config_yaml as JSON when they
//     parse as an object or array, the others (Payload, payload,
//     OriginalRequest) always as the exact string.
//  3. Volatile values by field name: time, duration_ms, ttft_ms,
//     last_frame_ms, log_since and created_at become "<volatile>";
//     output_tokens_per_second and e2e_tokens_per_second are removed (derived
//     from timings, and absent below 50 ms).
//  4. Generated IDs, the "id" of sign-in views and of listed proxies, become
//     "<volatile>" wherever they appear.
//
// The clock (Service.now) is pinned and the host name fixed instead of being
// normalised; see snapshot_seams_test.go. After each step the runner waits
// until all background work of the plugin (stream pumps, catalog refresh,
// sign-in polling) has finished or is parked in a wait that cannot end while
// the case runs.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

var snapUpdate = flag.Bool("update", false, "rewrite steps[].output of the RPC snapshot fixtures")

const (
	snapDir = "testdata/snapshots"
	// snapSchemaVersion is pluginabi.SchemaVersion of CPA v8.0.58.
	snapSchemaVersion = 10
	snapVolatile      = "<volatile>"
)

// snapByteKeys are the []byte fields CPA and the plugin exchange base64-encoded.
var snapByteKeys = map[string]bool{
	"Payload": true, "payload": true, "OriginalRequest": true, "Body": true,
	"StorageJSON": true, "RawJSON": true, "storage_json": true, "config_yaml": true,
}

// snapJSONByteKeys are the byte fields shown as JSON when they parse.
var snapJSONByteKeys = map[string]bool{"Body": true, "StorageJSON": true, "RawJSON": true, "storage_json": true, "config_yaml": true}

var snapVolatileKeys = map[string]bool{"time": true, "duration_ms": true, "ttft_ms": true, "last_frame_ms": true, "log_since": true, "created_at": true}

var snapDroppedKeys = map[string]bool{"output_tokens_per_second": true, "e2e_tokens_per_second": true}

type snapFixture struct {
	Case           string      `json:"case"`
	Description    string      `json:"description"`
	Setup          snapSetup   `json:"setup"`
	UpstreamScript []snapReply `json:"upstream_script"`
	Steps          []snapStep  `json:"steps"`
}

type snapSetup struct {
	Version     string           `json:"version"`
	Config      json.RawMessage  `json:"config"`
	Credentials []snapCredential `json:"credentials"`
}

type snapCredential struct {
	Name     string          `json:"name"`
	Fields   json.RawMessage `json:"fields"`
	ReadOnly bool            `json:"read_only,omitempty"`
}

type snapReply struct {
	Method string              `json:"method"`
	Path   string              `json:"path"`
	Status int                 `json:"status"`
	Header map[string][]string `json:"header,omitempty"`
	Body   string              `json:"body"`
	Chunks []string            `json:"chunks,omitempty"`
}

type snapStep struct {
	RPC    snapRPC     `json:"rpc"`
	Output *snapOutput `json:"output,omitempty"`
}

type snapRPC struct {
	Method  string          `json:"method"`
	Request json.RawMessage `json:"request"`
}

type snapOutput struct {
	UpstreamRequests []snapUpstreamRequest `json:"upstream_requests"`
	HostCalls        []snapHostCall        `json:"host_calls"`
	Response         json.RawMessage       `json:"response"`
}

type snapUpstreamRequest struct {
	Method string              `json:"method"`
	Path   string              `json:"path"`
	Query  string              `json:"query"`
	Header map[string][]string `json:"header"`
	Body   string              `json:"body"`
}

type snapHostCall struct {
	Method  string          `json:"method"`
	Request json.RawMessage `json:"request"`
}

func TestSnapshots(t *testing.T) {
	if *snapUpdate {
		snapWriteSkeletons(t)
	}
	paths, _ := filepath.Glob(filepath.Join(snapDir, "*.json"))
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Fatal("no snapshot fixtures; run with -update")
	}
	have := map[string]bool{}
	for _, path := range paths {
		have[strings.TrimSuffix(filepath.Base(path), ".json")] = true
	}
	for _, fixture := range snapCases() {
		if !have[fixture.Case] {
			t.Errorf("case %s has no fixture; run with -update", fixture.Case)
		}
	}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		t.Run(name, func(t *testing.T) { snapRun(t, path, name) })
	}
}

func snapWriteSkeletons(t *testing.T) {
	t.Helper()
	if errDir := os.MkdirAll(snapDir, 0o755); errDir != nil {
		t.Fatal(errDir)
	}
	for _, fixture := range snapCases() {
		path := filepath.Join(snapDir, fixture.Case+".json")
		if _, errStat := os.Stat(path); errStat == nil {
			continue
		}
		if errWrite := os.WriteFile(path, snapEncodeFixture(t, fixture), 0o644); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
}

func snapRun(t *testing.T, path, name string) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var fixture snapFixture
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(&fixture); errDecode != nil {
		t.Fatalf("decode %s: %v", path, errDecode)
	}
	if fixture.Case != name {
		t.Fatalf("fixture %s names case %q", path, fixture.Case)
	}

	env := snapStart(t, fixture)
	for i := range fixture.Steps {
		step := &fixture.Steps[i]
		got := env.step(t, step.RPC)
		if *snapUpdate {
			step.Output = got
			continue
		}
		if step.Output == nil {
			t.Errorf("step %d (%s) has no recorded output; run with -update", i, step.RPC.Method)
			continue
		}
		if want, have := snapCanonical(t, step.Output), snapCanonical(t, got); want != have {
			t.Errorf("step %d (%s) differs from the fixture:\n%s", i, step.RPC.Method, snapDiff(want, have))
		}
	}
	env.stop(t)
	if left := env.up.unused(); len(left) > 0 {
		t.Errorf("upstream script entries never requested: %s", strings.Join(left, ", "))
	}
	if *snapUpdate && !t.Failed() {
		if errWrite := os.WriteFile(path, snapEncodeFixture(t, fixture), 0o644); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
}

// snapEnv is one case's service with its fake host and upstream.
type snapEnv struct {
	up   *snapUpstream
	host *snapHost
	svc  *Service
	// vars are the placeholders substituted in config and requests.
	vars map[string]string
	// generated are the generated IDs normalised to <volatile>.
	generated []string
}

func snapStart(t *testing.T, fixture snapFixture) *snapEnv {
	t.Helper()
	up := newSnapUpstream(t, fixture.UpstreamScript)
	host := &snapHost{}
	svc := New(fixture.Setup.Version, host.call)
	host.svc = svc
	snapInstallSeams(svc, up.url())
	env := &snapEnv{up: up, host: host, svc: svc, vars: map[string]string{
		"{{UPSTREAM}}": up.url(),
		"{{DATA_DIR}}": t.TempDir(),
	}}

	config := env.substitute(string(fixture.Setup.Config))
	var fields map[string]any
	if errConfig := json.Unmarshal([]byte(config), &fields); errConfig != nil || fields["data-dir"] == nil {
		t.Fatalf("setup.config must be an object with data-dir (use {{DATA_DIR}}): %s", config)
	}
	register, _ := json.Marshal(map[string]any{"config_yaml": []byte(config), "schema_version": snapSchemaVersion})
	var envelope pluginabi.Envelope
	if errEnvelope := json.Unmarshal(svc.Call(pluginabi.MethodPluginRegister, register), &envelope); errEnvelope != nil || !envelope.OK {
		t.Fatalf("plugin.register failed: %+v", envelope.Error)
	}
	snapSettle(t, svc)
	if n := up.mark(); n != 0 {
		t.Fatalf("registration made %d upstream requests", n)
	}
	creds := make([]snapCredential, 0, len(fixture.Setup.Credentials))
	for _, cred := range fixture.Setup.Credentials {
		cred.Fields = json.RawMessage(env.substitute(string(cred.Fields)))
		creds = append(creds, cred)
	}
	host.seed(t, creds)
	return env
}

// stop shuts the plugin down the way CPA does.
func (e *snapEnv) stop(t *testing.T) {
	t.Helper()
	e.svc.Call(pluginabi.MethodPluginShutdown, []byte(`{}`))
}

// snapSettle waits until the plugin's background work is done (see snapBusy).
func snapSettle(t *testing.T, svc *Service) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		reason := snapBusy(svc)
		if reason == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("background work did not finish:\n%s", reason)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (e *snapEnv) substitute(text string) string {
	keys := make([]string, 0, len(e.vars))
	for key := range e.vars {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		text = strings.ReplaceAll(text, key, e.vars[key])
	}
	return text
}

// step runs one RPC and returns its normalised output.
func (e *snapEnv) step(t *testing.T, rpc snapRPC) *snapOutput {
	t.Helper()
	request := snapEncodeRequest(t, []byte(e.substitute(string(rpc.Request))))
	hostMark, upMark := e.host.mark(), e.up.mark()
	response := e.svc.Call(rpc.Method, request)
	snapSettle(t, e.svc)
	e.capture(response)

	out := &snapOutput{UpstreamRequests: []snapUpstreamRequest{}, HostCalls: []snapHostCall{}}
	for _, req := range e.up.since(upMark) {
		header := map[string][]string{}
		for name, values := range req.Header {
			for _, value := range values {
				header[name] = append(header[name], e.replace(value))
			}
		}
		out.UpstreamRequests = append(out.UpstreamRequests, snapUpstreamRequest{
			Method: req.Method, Path: req.Path, Query: e.replace(req.Query), Header: header, Body: e.replace(req.Body),
		})
	}
	for _, call := range e.host.since(hostMark) {
		out.HostCalls = append(out.HostCalls, snapHostCall{Method: call.method, Request: e.normalizeRaw(t, call.request)})
	}
	out.Response = e.normalizeRaw(t, response)
	return out
}

// capture remembers the IDs a response generated: sign-in views and proxies.
func (e *snapEnv) capture(response []byte) {
	var envelope struct {
		Result struct {
			Body []byte
		} `json:"result"`
	}
	if json.Unmarshal(response, &envelope) != nil || len(envelope.Result.Body) == 0 {
		return
	}
	var body map[string]any
	if json.Unmarshal(envelope.Result.Body, &body) != nil {
		return
	}
	remember := func(placeholder string, value any) {
		id, _ := value.(string)
		if id == "" {
			return
		}
		e.vars[placeholder] = id
		for _, known := range e.generated {
			if known == id {
				return
			}
		}
		e.generated = append(e.generated, id)
	}
	if _, isLogin := body["state"]; isLogin && body["auth_index"] != nil {
		remember("{{login_id}}", body["id"])
	}
	if proxy, ok := body["proxy"].(map[string]any); ok {
		remember("{{proxy_id}}", proxy["id"])
	}
	if proxies, ok := body["proxies"].([]any); ok {
		for _, item := range proxies {
			if proxy, ok := item.(map[string]any); ok {
				remember("{{proxy_id}}", proxy["id"])
			}
		}
	}
}

// replace applies normalisation rules 1 and 4 to a string.
func (e *snapEnv) replace(text string) string {
	origin := e.vars["{{UPSTREAM}}"]
	text = strings.ReplaceAll(text, origin, "{{UPSTREAM}}")
	if parsed, errParse := url.Parse(origin); errParse == nil && parsed.Host != "" {
		text = strings.ReplaceAll(text, parsed.Host, "{{UPSTREAM_HOST}}")
	}
	text = strings.ReplaceAll(text, e.vars["{{DATA_DIR}}"], "{{DATA_DIR}}")
	for _, id := range e.generated {
		text = strings.ReplaceAll(text, id, snapVolatile)
	}
	return text
}

// normalizeRaw normalises a JSON document, or a raw string that is not JSON.
func (e *snapEnv) normalizeRaw(t *testing.T, raw []byte) json.RawMessage {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if len(bytes.TrimSpace(raw)) == 0 || decoder.Decode(&value) != nil {
		return snapJSON(e.replace(string(raw)))
	}
	return snapJSON(e.normalize(value))
}

func (e *snapEnv) normalize(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			switch {
			case snapDroppedKeys[key]:
				delete(typed, key)
			case snapVolatileKeys[key] && item != nil:
				typed[key] = snapVolatile
			case snapByteKeys[key]:
				if text, ok := item.(string); ok {
					item = snapDecodeBytes(key, text)
				}
				typed[key] = e.normalize(item)
			default:
				typed[key] = e.normalize(item)
			}
		}
		return typed
	case []any:
		for i := range typed {
			typed[i] = e.normalize(typed[i])
		}
		return typed
	case string:
		return e.replace(typed)
	}
	return value
}

// snapDecodeBytes shows a base64 byte field decoded (rule 2). Bytes that are
// not valid UTF-8 stay base64.
func snapDecodeBytes(key, encoded string) any {
	raw, errDecode := base64.StdEncoding.DecodeString(encoded)
	if errDecode != nil || !utf8.Valid(raw) {
		return encoded
	}
	if snapJSONByteKeys[key] {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) == nil && !decoder.More() {
			switch value.(type) {
			case map[string]any, []any:
				return value
			}
		}
	}
	return string(raw)
}

// snapEncodeRequest turns a fixture request into what CPA sends: byte fields
// base64-encoded.
func snapEncodeRequest(t *testing.T, text []byte) []byte {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(text))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		t.Fatalf("decode rpc.request: %v\n%s", errDecode, text)
	}
	var encode func(any) any
	encode = func(value any) any {
		switch typed := value.(type) {
		case map[string]any:
			for key, item := range typed {
				if !snapByteKeys[key] || item == nil {
					typed[key] = encode(item)
					continue
				}
				var raw []byte
				if s, ok := item.(string); ok {
					raw = []byte(s)
				} else {
					raw = snapJSON(item)
				}
				typed[key] = base64.StdEncoding.EncodeToString(raw)
			}
			return typed
		case []any:
			for i := range typed {
				typed[i] = encode(typed[i])
			}
			return typed
		}
		return value
	}
	return snapJSON(encode(value))
}

// snapDecodeRequest is the inverse of snapEncodeRequest; the case table uses it
// to write requests marshalled from the SDK types.
func snapDecodeRequest(raw []byte) json.RawMessage {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		panic(fmt.Sprintf("snapshot: decode request: %v", errDecode))
	}
	var decode func(any) any
	decode = func(value any) any {
		switch typed := value.(type) {
		case map[string]any:
			for key, item := range typed {
				if text, ok := item.(string); ok && snapByteKeys[key] {
					typed[key] = snapDecodeBytes(key, text)
					continue
				}
				typed[key] = decode(item)
			}
			return typed
		case []any:
			for i := range typed {
				typed[i] = decode(typed[i])
			}
			return typed
		}
		return value
	}
	return snapJSON(decode(value))
}

// snapJSON encodes compact JSON without HTML escaping. It only sees values
// the tests built or decoded themselves, so a failure is a bug in the test.
func snapJSON(value any) json.RawMessage {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if errEncode := encoder.Encode(value); errEncode != nil {
		panic(fmt.Sprintf("snapshot: encode %T: %v", value, errEncode))
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// snapCanonical renders a value as indented JSON with sorted object keys.
func snapCanonical(t *testing.T, value any) string {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(snapJSON(value)))
	decoder.UseNumber()
	var generic any
	if errDecode := decoder.Decode(&generic); errDecode != nil {
		t.Fatalf("canonical: %v", errDecode)
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(generic)
	return buf.String()
}

func snapEncodeFixture(t *testing.T, fixture snapFixture) []byte {
	t.Helper()
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if errEncode := encoder.Encode(fixture); errEncode != nil {
		t.Fatalf("encode fixture: %v", errEncode)
	}
	return buf.Bytes()
}

// snapDiff shows the first lines where two renderings differ.
func snapDiff(want, have string) string {
	wantLines, haveLines := strings.Split(want, "\n"), strings.Split(have, "\n")
	first := 0
	for first < len(wantLines) && first < len(haveLines) && wantLines[first] == haveLines[first] {
		first++
	}
	window := func(lines []string) string {
		from, to := max(first-4, 0), min(first+8, len(lines))
		var b strings.Builder
		for i := from; i < to; i++ {
			marker := "  "
			if i == first {
				marker = "> "
			}
			fmt.Fprintf(&b, "%s%4d %s\n", marker, i+1, lines[i])
		}
		return b.String()
	}
	return "--- fixture\n" + window(wantLines) + "+++ actual\n" + window(haveLines)
}

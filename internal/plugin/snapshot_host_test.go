package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/berry-shake/cpa-opendesign-plugin/internal/credential"
	"github.com/berry-shake/cpa-opendesign-plugin/internal/jsonfield"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// The fake CPA host and the fake upstream of the RPC snapshot tests.

// snapAuthDir is the fake CPA auth dir.
const snapAuthDir = "/auths"

// snapHostSummary is the host config summary the fake host sends along with
// the RPCs it makes itself.
var snapHostSummary = pluginapi.HostConfigSummary{AuthDir: snapAuthDir}

// snapAuthFile is one auth file in the fake auth dir.
type snapAuthFile struct {
	index string
	name  string
	raw   []byte
	// readOnly makes host.auth.save fail, as a failed write does in CPA.
	readOnly bool
	// models are the IDs CPA registered for the file (host.auth.models).
	models []string
}

// snapRecorded is one host callback the plugin made.
type snapRecorded struct {
	method  string
	request []byte
}

// snapHost is the fake CPA host. It keeps auth files in memory and records
// every callback. Saving a file runs it through the plugin the way CPA does:
// auth.parse, then model.for_auth, with CPA's prefix rules for the IDs it
// registers.
type snapHost struct {
	svc *Service

	mu    sync.Mutex
	files []*snapAuthFile
	next  int
	calls []snapRecorded
}

// seed puts auth files in the auth dir, as if CPA had them on disk.
func (h *snapHost) seed(t *testing.T, creds []snapCredential) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, cred := range creds {
		var compact bytes.Buffer
		if errCompact := json.Compact(&compact, cred.Fields); errCompact != nil {
			t.Fatalf("credential %s: %v", cred.Name, errCompact)
		}
		h.next++
		h.files = append(h.files, &snapAuthFile{index: fmt.Sprintf("idx-%d", h.next), name: cred.Name, raw: compact.Bytes(), readOnly: cred.ReadOnly, models: []string{}})
	}
}

func (h *snapHost) mark() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

func (h *snapHost) since(mark int) []snapRecorded {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]snapRecorded(nil), h.calls[mark:]...)
}

func (h *snapHost) call(method string, request []byte) ([]byte, error) {
	h.mu.Lock()
	h.calls = append(h.calls, snapRecorded{method: method, request: bytes.Clone(request)})
	h.mu.Unlock()
	switch method {
	case pluginabi.MethodHostLog, pluginabi.MethodHostStreamEmit, pluginabi.MethodHostStreamClose:
		return okEnvelope(struct{}{}), nil
	case pluginabi.MethodHostAuthList:
		return h.list(), nil
	case pluginabi.MethodHostAuthGet:
		var req pluginapi.HostAuthGetRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return ErrorEnvelope("host_error", "decode host auth get request: "+errUnmarshal.Error()), nil
		}
		return h.get(req.AuthIndex), nil
	case pluginabi.MethodHostAuthSave:
		var req pluginapi.HostAuthSaveRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return ErrorEnvelope("host_error", "decode host auth save request: "+errUnmarshal.Error()), nil
		}
		return h.save(req), nil
	case pluginabi.MethodHostAuthModels:
		return h.models(), nil
	}
	return ErrorEnvelope("host_error", "unexpected host method "+method), nil
}

func snapFields(raw []byte) map[string]any {
	fields, errDecode := credential.DecodeFields(raw)
	if errDecode != nil {
		return map[string]any{}
	}
	return fields
}

func (h *snapHost) list() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	files := make([]pluginapi.HostAuthFileEntry, 0, len(h.files))
	for _, file := range h.files {
		fields := snapFields(file.raw)
		provider := jsonfield.String(fields, credential.KeyType)
		files = append(files, pluginapi.HostAuthFileEntry{
			ID:        file.name,
			AuthIndex: file.index,
			Name:      file.name,
			Type:      provider,
			Provider:  provider,
			Label:     jsonfield.String(fields, credential.KeyLabel),
			Disabled:  jsonfield.Bool(fields, credential.KeyDisabled),
			Source:    "file",
			Path:      snapAuthDir + "/" + file.name,
			Priority:  jsonfield.Int(fields, credential.KeyPriority),
			Note:      jsonfield.String(fields, credential.KeyNote),
		})
	}
	return okEnvelope(rpcHostAuthListResponse{Files: files})
}

func (h *snapHost) get(index string) []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, file := range h.files {
		if file.index == index {
			return okEnvelope(pluginapi.HostAuthGetResponse{AuthIndex: file.index, Name: file.name, Path: snapAuthDir + "/" + file.name, JSON: file.raw})
		}
	}
	return ErrorEnvelope("host_error", "auth not found for auth_index "+index)
}

func (h *snapHost) save(req pluginapi.HostAuthSaveRequest) []byte {
	name := filepath.Base(strings.TrimSpace(req.Name))
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		return ErrorEnvelope("host_error", "auth file name must end with .json")
	}
	h.mu.Lock()
	for _, file := range h.files {
		if file.name == name && file.readOnly {
			h.mu.Unlock()
			return ErrorEnvelope("host_error", "failed to write auth file: open "+snapAuthDir+"/"+name+": permission denied")
		}
	}
	h.mu.Unlock()

	// CPA parses the file through the plugin before it writes it, then asks
	// for the credential's models. The lock is not held: the plugin may call
	// back into the host.
	fields := snapFields(req.JSON)
	parseReq, _ := json.Marshal(pluginapi.AuthParseRequest{
		Provider: jsonfield.String(fields, credential.KeyType),
		Path:     snapAuthDir + "/" + name,
		FileName: name,
		RawJSON:  req.JSON,
		Host:     snapHostSummary,
	})
	var envelope pluginabi.Envelope
	_ = json.Unmarshal(h.svc.Call(pluginabi.MethodAuthParse, parseReq), &envelope)
	if !envelope.OK {
		message := "auth.parse failed"
		if envelope.Error != nil {
			message = envelope.Error.Message
		}
		return ErrorEnvelope("host_error", "invalid auth file: "+message)
	}
	var parsed pluginapi.AuthParseResponse
	_ = json.Unmarshal(envelope.Result, &parsed)
	models := []string{}
	if parsed.Handled && !parsed.Auth.Disabled {
		modelReq, _ := json.Marshal(rpcAuthModelRequest{AuthModelRequest: pluginapi.AuthModelRequest{
			AuthID:       name,
			AuthProvider: parsed.Auth.Provider,
			StorageJSON:  req.JSON,
			Metadata:     parsed.Auth.Metadata,
			Host:         snapHostSummary,
		}})
		var modelEnvelope pluginabi.Envelope
		_ = json.Unmarshal(h.svc.Call(pluginabi.MethodModelForAuth, modelReq), &modelEnvelope)
		var resp pluginapi.ModelResponse
		_ = json.Unmarshal(modelEnvelope.Result, &resp)
		for _, model := range resp.Models {
			// Without routing.force-model-prefix CPA registers the bare ID
			// next to the prefixed one.
			models = append(models, model.ID)
			if prefix := strings.TrimSpace(parsed.Auth.Prefix); prefix != "" {
				models = append(models, prefix+"/"+model.ID)
			}
		}
		sort.Strings(models)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, file := range h.files {
		if file.name == name {
			file.raw = bytes.Clone(req.JSON)
			file.models = models
			return okEnvelope(pluginapi.HostAuthSaveResponse{Name: name, Path: snapAuthDir + "/" + name})
		}
	}
	h.next++
	h.files = append(h.files, &snapAuthFile{index: fmt.Sprintf("idx-%d", h.next), name: name, raw: bytes.Clone(req.JSON), models: models})
	return okEnvelope(pluginapi.HostAuthSaveResponse{Name: name, Path: snapAuthDir + "/" + name})
}

// models answers host.auth.models for every auth, sorted by ID as CPA does.
func (h *snapHost) models() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := pluginapi.HostAuthModelsResponse{Credentials: []pluginapi.HostAuthModels{}}
	for _, file := range h.files {
		provider := jsonfield.String(snapFields(file.raw), credential.KeyType)
		out.Credentials = append(out.Credentials, pluginapi.HostAuthModels{ID: file.name, AuthIndex: file.index, Provider: provider, Models: append([]string{}, file.models...)})
	}
	sort.SliceStable(out.Credentials, func(i, j int) bool {
		return strings.ToLower(out.Credentials[i].ID) < strings.ToLower(out.Credentials[j].ID)
	})
	return okEnvelope(out)
}

// snapUpstream is the fake OpenDesign API (inference and the vela account
// API on one origin). It answers from the upstream script and records every
// request exactly as it arrived.
type snapUpstream struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	script   []snapReply
	used     []bool
	requests []snapUpstreamRequest
}

func newSnapUpstream(t *testing.T, script []snapReply) *snapUpstream {
	t.Helper()
	u := &snapUpstream{t: t, script: script, used: make([]bool, len(script))}
	u.srv = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *snapUpstream) url() string { return u.srv.URL }

func (u *snapUpstream) mark() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.requests)
}

func (u *snapUpstream) since(mark int) []snapUpstreamRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]snapUpstreamRequest(nil), u.requests[mark:]...)
}

// unused lists the script entries no request matched.
func (u *snapUpstream) unused() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []string
	for i, reply := range u.script {
		if !u.used[i] {
			out = append(out, fmt.Sprintf("#%d %s %s", i, reply.Method, reply.Path))
		}
	}
	return out
}

func (u *snapUpstream) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.requests = append(u.requests, snapUpstreamRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: string(body)})
	match := -1
	for i, reply := range u.script {
		if !u.used[i] && reply.Method == r.Method && reply.Path == r.URL.Path {
			match = i
			u.used[i] = true
			break
		}
	}
	u.mu.Unlock()
	if match < 0 {
		u.t.Errorf("unscripted upstream request %s %s", r.Method, r.URL.RequestURI())
		http.Error(w, "unscripted upstream request", http.StatusTeapot)
		return
	}
	reply := u.script[match]
	for name, values := range reply.Header {
		w.Header().Del(name)
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	status := reply.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if len(reply.Chunks) == 0 {
		_, _ = io.WriteString(w, reply.Body)
		return
	}
	flusher, _ := w.(http.Flusher)
	for _, chunk := range reply.Chunks {
		_, _ = io.WriteString(w, chunk)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

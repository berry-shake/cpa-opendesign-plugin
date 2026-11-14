package opendesign

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// The case table of the RPC snapshot tests. It only writes the input
// skeleton of a fixture that does not exist yet (go test -run TestSnapshots
// -update); the fixture files are the source of truth from then on.

// snapshotCases returns the case table with the byte fields of every step
// request decoded, as fixtures show them.
func snapshotCases(t *testing.T) []snapshotFixture {
	cases := snapCases()
	for i := range cases {
		for j := range cases[i].Steps {
			cases[i].Steps[j].RPC.Request = snapshotCaseJSON(t, cases[i].Steps[j].RPC.Request)
		}
	}
	return cases
}

// snapJSON encodes compact JSON without HTML escaping, the bytes the case
// table puts in auth files, configs and request bodies.
func snapJSON(value any) json.RawMessage {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if errEncode := encoder.Encode(value); errEncode != nil {
		panic(fmt.Sprintf("snapshot: encode %T: %v", value, errEncode))
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

const (
	snapVersion = "9.9.9-snapshot"

	// Fake OpenDesign API keys.
	snapKeyOne = "sk-amr-snap-key-one-0001"
	snapKeyTwo = "sk-amr-snap-key-two-0002"
	snapKeyNew = "sk-amr-snap-key-new-0003"
	snapKeyBad = "sk-amr-snap-key-bad-0004"

	pathChat      = "/v1/chat/completions"
	pathResponses = "/v1/responses"
	pathMessages  = "/v1/messages"
	pathModels    = "/v1/models"

	pathDevice     = "/api/v1/cli/device-authorizations"
	pathLogout     = "/api/v1/cli/logout"
	pathWorkspaces = "/api/v1/workspaces"
	pathFunding    = "/api/v1/workspaces/ws-me/billing/funding-check"
	pathSnapshot   = "/api/v1/workspaces/ws-me/billing/snapshot"
)

// The plugin's management and resource routes.
const (
	managementBase = "/v0/management/plugins/" + ProviderID
	resourceBase   = "/v0/resource/plugins/" + ProviderID
	logoPath       = resourceBase + "/logo.svg"
	flagPath       = resourceBase + "/flag.svg"
)

// snapshotAuthDir is the auth directory the fake host reports, and
// snapshotHostSummary the host config summary CPA sends with auth requests.
const snapshotAuthDir = "/auths"

var snapshotHostSummary = pluginapi.HostConfigSummary{AuthDir: snapshotAuthDir}

// cpaRequestIDHeader is the header the after-auth interceptor puts CPA's
// request ID in ("X-<DisplayName>-Cpa-Request-Id").
const cpaRequestIDHeader = "X-OpenDesign-Cpa-Request-Id"

// The wire types CPA v8 (internal/pluginhost) sends.
type (
	rpcExecutorRequest struct {
		pluginapi.ExecutorRequest
		StreamID       string `json:"stream_id,omitempty"`
		HostCallbackID string `json:"host_callback_id,omitempty"`
	}
	rpcManagementRequest struct {
		pluginapi.ManagementRequest
		HostCallbackID string `json:"host_callback_id,omitempty"`
	}
	rpcAuthModelRequest struct {
		pluginapi.AuthModelRequest
		HostCallbackID string `json:"host_callback_id,omitempty"`
	}
	rpcAuthRefreshRequest struct {
		pluginapi.AuthRefreshRequest
		HostCallbackID string `json:"host_callback_id,omitempty"`
	}
	rpcQuotaFetchRequest struct {
		pluginapi.QuotaFetchRequest
		HostCallbackID string `json:"host_callback_id,omitempty"`
	}
	rpcRequestInterceptRequest struct {
		pluginapi.RequestInterceptRequest
		HostCallbackID string `json:"host_callback_id,omitempty"`
	}
)

// snapSeed is an auth file of a case together with its auth index.
type snapSeed struct {
	name     string
	index    string
	fields   map[string]any
	readOnly bool
}

func (s snapSeed) raw() []byte { return snapJSON(s.fields) }

func (s snapSeed) with(extra map[string]any) snapSeed {
	fields := make(map[string]any, len(s.fields)+len(extra))
	for key, value := range s.fields {
		fields[key] = value
	}
	for key, value := range extra {
		if value == nil {
			delete(fields, key)
			continue
		}
		fields[key] = value
	}
	s.fields = fields
	return s
}

func snapSeedOne() snapSeed {
	return snapSeed{name: "opendesign-k1.json", index: "idx-1", fields: map[string]any{
		"type": ProviderID, "api_key": snapKeyOne, "label": "Key One", "proxy_url": "direct",
	}}
}

func snapSeedTwo(index string) snapSeed {
	return snapSeed{name: "opendesign-k2.json", index: index, fields: map[string]any{
		"type": ProviderID, "api_key": snapKeyTwo, "models": []string{"model-b", "model-z"}, "prefix": "od2", "note": "second key", "priority": 2,
	}}
}

// snapAccount is an OpenDesign account signed in on a key card.
func snapAccount(workspace bool) map[string]any {
	fields := map[string]any{
		"account_control_key": "ck-snap-old", "account_control_key_id": "ck-snap-old-id", "account_user_id": "user-1",
		"account_email": "me@example.com", "account_name": "Me", "account_logged_in_at": "2025-12-31T00:00:00Z",
		// The API origin the login reported (without it the adapter's default,
		// which the test seam points at the fake upstream too).
		"account_api_url": "{{UPSTREAM}}",
	}
	if workspace {
		fields["account_workspace_id"] = "ws-me"
		fields["account_workspace_name"] = "Personal"
	}
	return fields
}

// TestSnapshotAccountAPIPinned keeps every case off the production account
// API. The seam points the adapter's default origin at the fake upstream,
// and account calls go to the origin an auth file names (account_api_url)
// or a sign-in reports (apiUrl) when there is one. So every origin a fixture
// seeds, sends or approves is the fake upstream.
func TestSnapshotAccountAPIPinned(t *testing.T) {
	paths, errGlob := filepath.Glob(filepath.Join(snapshotDir, "*.json"))
	if errGlob != nil {
		t.Fatal(errGlob)
	}
	for _, file := range paths {
		raw, errRead := os.ReadFile(file)
		if errRead != nil {
			t.Fatal(errRead)
		}
		var fixture struct {
			Setup          any             `json:"setup"`
			UpstreamScript []snapshotReply `json:"upstream_script"`
			Steps          []struct {
				RPC any `json:"rpc"`
			} `json:"steps"`
		}
		if errUnmarshal := json.Unmarshal(raw, &fixture); errUnmarshal != nil {
			t.Fatalf("%s: %v", file, errUnmarshal)
		}
		var check func(where string, value any)
		check = func(where string, value any) {
			switch typed := value.(type) {
			case string:
				if !strings.Contains(typed, "account_control_key") {
					return
				}
				var decoded any
				if json.Unmarshal([]byte(typed), &decoded) != nil {
					t.Errorf("%s: %s holds account fields that are not JSON", file, where)
					return
				}
				check(where, decoded)
			case map[string]any:
				if origin, named := typed["account_api_url"]; named && origin != "{{UPSTREAM}}" {
					t.Errorf("%s: %s names account_api_url %v, not {{UPSTREAM}}", file, where, origin)
				}
				for _, item := range typed {
					check(where, item)
				}
			case []any:
				for _, item := range typed {
					check(where, item)
				}
			}
		}
		check("setup", fixture.Setup)
		for i, step := range fixture.Steps {
			check(fmt.Sprintf("steps[%d].rpc", i), step.RPC)
		}
		for i, reply := range fixture.UpstreamScript {
			var token struct {
				Status string `json:"status"`
				APIURL string `json:"apiUrl"`
			}
			if json.Unmarshal([]byte(reply.Body), &token) != nil || token.Status != "approved" {
				continue
			}
			if token.APIURL != "" && token.APIURL != "{{UPSTREAM}}" {
				t.Errorf("%s: upstream_script[%d] approves a sign-in at %s, not {{UPSTREAM}}", file, i, token.APIURL)
			}
		}
	}
}

func snapConfig(extra map[string]any) json.RawMessage {
	config := map[string]any{"base-url": "{{UPSTREAM}}", "data-dir": "{{DATA_DIR}}", "model-prefix": "opendesign"}
	for key, value := range extra {
		config[key] = value
	}
	return snapJSON(config)
}

func snapCase(name, description string, seeds []snapSeed, script []snapshotReply, steps ...snapshotRPC) snapshotFixture {
	creds := []snapshotCredential{}
	for _, seed := range seeds {
		creds = append(creds, snapshotCredential{Name: seed.name, Fields: snapJSON(seed.fields), ReadOnly: seed.readOnly})
	}
	if script == nil {
		script = []snapshotReply{}
	}
	for i := range script {
		if script[i].Chunks == nil {
			script[i].Chunks = []string{}
		}
	}
	fixture := snapshotFixture{
		Case:           name,
		Description:    description,
		Setup:          snapshotSetup{Version: snapVersion, Config: snapConfig(nil), Credentials: creds},
		UpstreamScript: script,
	}
	for _, step := range steps {
		fixture.Steps = append(fixture.Steps, snapshotStep{RPC: step})
	}
	return fixture
}

// snapCall writes an RPC request marshalled from the SDK types, so it has
// the shape CPA sends.
func snapCall(method string, request any) snapshotRPC {
	return snapshotRPC{Method: method, Request: snapJSON(request)}
}

// snapExec describes one executor call as CPA makes it.
type snapExec struct {
	seed   snapSeed
	format string // openai, openai-response or claude
	// model is the model CPA hands the executor (prefix stripped); client is
	// what the client asked for ("opendesign/<model>" when empty).
	model     string
	client    string
	payload   string
	stream    bool
	streamID  string
	header    http.Header
	requestID string
}

func snapExecute(e snapExec) snapshotRPC {
	method := pluginabi.MethodExecutorExecute
	if e.stream {
		method = pluginabi.MethodExecutorExecuteStream
	}
	path := map[string]string{"openai": pathChat, "openai-response": pathResponses, "claude": pathMessages}[e.format]
	header := http.Header{
		"User-Agent":    {"snapshot-client/1.0"},
		"Authorization": {"Bearer sk-client-snapshot"},
		"Content-Type":  {"application/json"},
	}
	for name, values := range e.header {
		header[http.CanonicalHeaderKey(name)] = values
	}
	if e.requestID != "" {
		header.Set(cpaRequestIDHeader, e.requestID)
	}
	client := e.client
	if client == "" {
		client = "opendesign/" + e.model
	}
	original := strings.Replace(e.payload, `"model":"`+e.model+`"`, `"model":"`+client+`"`, 1)
	return snapCall(method, rpcExecutorRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			AuthID:          e.seed.name,
			AuthProvider:    ProviderID,
			Model:           e.model,
			Format:          e.format,
			Stream:          e.stream,
			Headers:         header,
			OriginalRequest: []byte(original),
			SourceFormat:    e.format,
			Payload:         []byte(e.payload),
			Metadata: map[string]any{
				"request_path":        path,
				"requested_model":     client,
				"selected_auth_id":    e.seed.name,
				"selected_auth_index": e.seed.index,
			},
			StorageJSON:    e.seed.raw(),
			AuthMetadata:   e.seed.fields,
			AuthAttributes: map[string]string{"path": snapshotAuthDir + "/" + e.seed.name},
		},
		StreamID:       e.streamID,
		HostCallbackID: "cb-exec",
	})
}

func snapManage(method, path string, query url.Values, body any) snapshotRPC {
	var raw []byte
	switch typed := body.(type) {
	case nil:
	case string:
		raw = []byte(typed)
	default:
		raw = snapJSON(typed)
	}
	if !strings.HasPrefix(path, "/v0/") {
		path = managementBase + path
	}
	return snapCall(pluginabi.MethodManagementHandle, rpcManagementRequest{
		ManagementRequest: pluginapi.ManagementRequest{Method: method, Path: path, Headers: http.Header{"Accept": {"application/json"}}, Query: query, Body: raw},
		HostCallbackID:    "cb-mgmt",
	})
}

func snapQuery(pairs ...string) url.Values {
	out := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out.Add(pairs[i], pairs[i+1])
	}
	return out
}

func snapModelsForAuth(seed snapSeed) snapshotRPC {
	return snapCall(pluginabi.MethodModelForAuth, rpcAuthModelRequest{
		AuthModelRequest: pluginapi.AuthModelRequest{
			AuthID: seed.name, AuthProvider: ProviderID, StorageJSON: seed.raw(), Metadata: seed.fields,
			Attributes: map[string]string{"path": snapshotAuthDir + "/" + seed.name}, Host: snapshotHostSummary,
		},
		HostCallbackID: "cb-models",
	})
}

func snapQuotaFetch(seed snapSeed, provider string) snapshotRPC {
	return snapCall(pluginabi.MethodQuotaFetch, rpcQuotaFetchRequest{
		QuotaFetchRequest: pluginapi.QuotaFetchRequest{
			AuthIndex: seed.index, AuthID: seed.name, Provider: provider, StorageJSON: seed.raw(), Metadata: seed.fields, Host: snapshotHostSummary,
		},
		HostCallbackID: "cb-quota",
	})
}

func snapAuthParse(seed snapSeed, host pluginapi.HostConfigSummary) snapshotRPC {
	provider, _ := seed.fields["type"].(string)
	return snapCall(pluginabi.MethodAuthParse, pluginapi.AuthParseRequest{
		Provider: provider, Path: snapshotAuthDir + "/" + seed.name, FileName: seed.name, RawJSON: seed.raw(), Host: host,
	})
}

func snapEmpty(method string) snapshotRPC {
	return snapshotRPC{Method: method, Request: json.RawMessage(`{}`)}
}

// Upstream answers.

func snapJSONReply(method, path string, status int, body string, header ...string) snapshotReply {
	reply := snapshotReply{Method: method, Path: path, Status: status, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: body}
	for i := 0; i+1 < len(header); i += 2 {
		reply.Header[http.CanonicalHeaderKey(header[i])] = []string{header[i+1]}
	}
	return reply
}

func snapSSEReply(path string, requestID string, events ...string) snapshotReply {
	chunks := make([]string, 0, len(events))
	for _, event := range events {
		chunks = append(chunks, event+"\n\n")
	}
	return snapshotReply{Method: http.MethodPost, Path: path, Status: http.StatusOK, Header: map[string][]string{
		"Content-Type": {"text/event-stream; charset=utf-8"}, "Cache-Control": {"no-cache"}, "X-Request-Id": {requestID},
	}, Chunks: chunks}
}

func snapModelsReply(body string) snapshotReply {
	return snapJSONReply(http.MethodGet, pathModels, http.StatusOK, body)
}

const (
	snapModelA = `{"id":"model-a","object":"model","created":1767000000,"owned_by":"opendesign","architecture":{"input_modalities":["text","image"],"output_modalities":["text"]},"metadata":{"cost":"Low","capability":"standard","context_limit":200000,"context_budget":160000,"output_budget":32000},"default":true,"enabled":true}`
	snapModelB = `{"id":"model-b","object":"model","created":1767000001,"owned_by":"opendesign","architecture":{"input_modalities":["text"],"output_modalities":["text"]},"metadata":{"cost":"high","capability":"reasoning","context_limit":128000,"context_budget":100000,"output_budget":16000},"enabled":true}`
	snapModelC = `{"id":"model-c","object":"model","created":1767000003,"owned_by":"opendesign","architecture":{"input_modalities":["text","application/pdf"],"output_modalities":["text"]},"metadata":{"cost":"medium","capability":"standard","context_limit":64000,"context_budget":60000,"output_budget":8000},"enabled":false}`
	snapImage  = `{"id":"image-gen","object":"model","created":1767000002,"owned_by":"opendesign","architecture":{"input_modalities":["text"],"output_modalities":["image"]},"metadata":{"cost":"medium"},"enabled":true}`

	// snapModelsOne is the model list of key one's plan, snapModelsTwo that
	// of key two's.
	snapModelsOne = `{"object":"list","data":[` + snapModelA + `,` + snapModelB + `,` + snapImage + `]}`
	snapModelsTwo = `{"object":"list","data":[` + snapModelB + `,` + snapModelC + `]}`

	snapChatPayload  = `{"model":"model-a","messages":[{"role":"user","content":"Hello"}],"reasoning_effort":"low"}`
	snapChatResponse = `{"id":"chatcmpl-1","object":"chat.completion","created":1767225600,"model":"model-a-2026","choices":[{"index":0,"message":{"role":"assistant","content":"Hi there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":1}}}`

	snapChatStreamPayload = `{"model":"model-a","messages":[{"role":"user","content":"Hello"}],"stream":true}`
	snapChatChunk         = `data: {"id":"chatcmpl-2","object":"chat.completion.chunk","created":1767225600,"model":"model-a",`

	snapResponsesPayload  = `{"model":"model-a","input":"Hello","reasoning":{"effort":"medium"}}`
	snapResponsesResponse = `{"id":"resp_1","object":"response","created_at":1767225600,"status":"completed","model":"model-a","output":[{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hi"}]}],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":2},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":3},"total_tokens":15}}`

	snapMessagesPayload  = `{"model":"model-a","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":"Hello"}]}`
	snapMessagesResponse = `{"id":"msg_1","type":"message","role":"assistant","model":"model-a","content":[{"type":"thinking","thinking":"Greet back.","signature":"sig-1"},{"type":"text","text":"Hi"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":2,"cache_read_input_tokens":5,"output_tokens":7,"output_tokens_details":{"thinking_tokens":4}}}`

	snapMessagesStreamPayload = `{"model":"model-a","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"Hello"}]}`
	snapMessageStart          = "event: message_start\n" + `data: {"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","content":[],"model":"model-a","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":5,"output_tokens":1}}}`
	snapPing                  = "event: ping\n" + `data: {"type":"ping"}`
	snapMessageStop           = "event: message_stop\n" + `data: {"type":"message_stop"}`

	snapCloudflare = "<!DOCTYPE html>\n<html><head><title>Attention Required! | Cloudflare</title></head><body>Sorry, you have been blocked</body></html>"

	snapWorkspaces = `{"items":[{"workspaceId":"ws-team","workspaceName":"Team","workspaceType":"team","workspaceMemberId":"m1","role":"owner","memberStatus":"active","lifecycleState":"active","isDefault":true},{"workspaceId":"ws-me","workspaceName":"Personal","workspaceType":"personal","workspaceMemberId":"m2","role":"owner","memberStatus":"active","lifecycleState":"active","isDefault":false}]}`
	snapFunding    = `{"schemaVersion":1,"workspaceId":"ws-me","workspaceMemberId":"m2","generatedAt":"2026-01-01T00:00:00Z","balanceUsd":"1.25","codingPlan":{"workspaceId":"ws-me","generatedAt":"2026-01-01T00:00:00Z","eligible":true,"tier":"go","windows":[{"policyId":"p5","durationSeconds":18000,"resetMode":"activity_triggered","usedCredits":"250","limitCredits":"1000","remainingCredits":"750","windowStart":"2025-12-31T21:00:00Z","resetsAt":"2026-01-01T02:00:00Z"},{"policyId":"p7","durationSeconds":604800,"resetMode":"anchored_recurring","usedCredits":"4000","limitCredits":"5000","remainingCredits":"1000","windowStart":null,"resetsAt":null},{"policyId":"pm","durationSeconds":2592000,"resetMode":"anchored_recurring","usedCredits":"0","limitCredits":"0","remainingCredits":"0","windowStart":null,"resetsAt":null}]},"modelCovered":true,"funding":"coding_plan","fundingCheck":null}`
	snapSnapshot   = `{"schemaVersion":1,"workspaceId":"ws-me","plan":{"tier":"go","interval":"month","currentPeriodStart":"2025-12-13T16:06:05Z","currentPeriodEnd":"2026-01-13T16:06:05Z","cancelAtPeriodEnd":false,"status":"active"},"wallet":{"balanceUsd":"1.25","expiresAt":null},"designPlanWindows":[]}`
)

func snapChatEvent(rest string) string { return snapChatChunk + rest }

func snapChatStream(id string) snapshotReply {
	return snapSSEReply(pathChat, id,
		snapChatEvent(`"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`),
		": keepalive",
		snapChatEvent(`"choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`),
		snapChatEvent(`"choices":[{"index":0,"delta":{"content":" there"},"finish_reason":null}]}`),
		snapChatEvent(`"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
		snapChatEvent(`"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":2,"total_tokens":14}}`),
		"data: [DONE]",
	)
}

func snapBlock(kind string, index int, body string) string {
	return "event: " + kind + "\n" + `data: {"type":"` + kind + `","index":` + strconv.Itoa(index) + body + `}`
}

func snapMessageDelta(reason string, output int) string {
	return "event: message_delta\n" + `data: {"type":"message_delta","delta":{"stop_reason":"` + reason + `","stop_sequence":null},"usage":{"output_tokens":` + strconv.Itoa(output) + `}}`
}

func snapMessagesStream(id string) snapshotReply {
	return snapSSEReply(pathMessages, id,
		snapMessageStart,
		snapPing,
		snapBlock("content_block_start", 0, `,"content_block":{"type":"text","text":""}`),
		snapBlock("content_block_delta", 0, `,"delta":{"type":"text_delta","text":"Hi"}`),
		snapBlock("content_block_stop", 0, ``),
		snapMessageDelta("end_turn", 3),
		snapMessageStop,
	)
}

// snapDeviceExpiry is when a device authorization expires: ten minutes after
// snapshotClock, within the plugin's 15-minute cap.
var snapDeviceExpiry = snapshotClock.Add(10 * time.Minute).Format(time.RFC3339)

func snapDevice(id string, interval int) snapshotReply {
	return snapJSONReply(http.MethodPost, pathDevice, http.StatusCreated, `{"deviceId":"`+id+`","userCode":"WXYZ-1234","deviceSecret":"secret-`+id+`","activationUrl":"https://open-design.ai/cli/activate?deviceId=`+id+`","pollIntervalSeconds":`+strconv.Itoa(interval)+`,"status":"pending","expiresAt":"`+snapDeviceExpiry+`"}`)
}

func snapToken(id, body string) snapshotReply {
	return snapJSONReply(http.MethodPost, pathDevice+"/"+id+"/token", http.StatusOK, body)
}

func snapApproved(controlKey string) string {
	return `{"status":"approved","controlKey":"` + controlKey + `","controlKeyId":"` + controlKey + `-id","runtimeKey":"rk-snap","user":{"id":"user-1","authUserId":"auth-1","email":"me@example.com","name":"Me"},"apiUrl":"","linkUrl":""}`
}

func snapDeviceCall(id, action string, status int) snapshotReply {
	body := `{"status":"completed"}`
	if action == "cancel" {
		body = `{"status":"cancelled"}`
	}
	if status >= 400 {
		body = `{"error":{"code":"internal_error","message":"device authorization store unavailable"}}`
	}
	return snapJSONReply(http.MethodPost, pathDevice+"/"+id+"/"+action, status, body)
}

func snapRevoke(status int) snapshotReply {
	body := `{"ok":true}`
	if status == http.StatusUnauthorized {
		body = `{"error":{"code":"invalid_control_key","message":"control key revoked"}}`
	}
	return snapJSONReply(http.MethodPost, pathLogout, status, body)
}

func snapFundingReply() snapshotReply {
	return snapJSONReply(http.MethodGet, pathFunding, http.StatusOK, snapFunding)
}

func snapSnapshotReply() snapshotReply {
	return snapJSONReply(http.MethodGet, pathSnapshot, http.StatusOK, snapSnapshot)
}

func snapBlocked(method, path string) snapshotReply {
	return snapshotReply{Method: method, Path: path, Status: http.StatusForbidden, Header: map[string][]string{"Content-Type": {"text/html; charset=UTF-8"}, "Server": {"cloudflare"}}, Body: snapCloudflare}
}

// snapCases lists every case; see TestSnapshots.
func snapCases() []snapshotFixture {
	one := snapSeedOne()
	two := snapSeedTwo("idx-2")
	signedIn := one.with(snapAccount(true))
	signedInNoWorkspace := one.with(snapAccount(false))
	chatOne := snapExec{seed: one, format: "openai", model: "model-a", payload: snapChatPayload, requestID: "req-chat-1"}
	anthropicHeaders := http.Header{"Anthropic-Version": {"2023-06-01"}, "Anthropic-Beta": {"interleaved-thinking-2025-05-14"}}

	var cases []snapshotFixture
	add := func(fixture snapshotFixture) {
		// Executor cases end by reading the request log, so the rows the
		// calls wrote are locked too.
		for _, prefix := range []string{"exec_", "stream_fix_", "error_"} {
			if strings.HasPrefix(fixture.Case, prefix) {
				fixture.Steps = append(fixture.Steps, snapshotStep{RPC: snapManage(http.MethodGet, "/logs", nil, nil)})
				break
			}
		}
		cases = append(cases, fixture)
	}

	// ---- executor: the three protocols ----

	add(snapCase("exec_chat_nonstream", "OpenAI chat completion, buffered: the key goes out as a bearer token, client credentials stay behind, usage and reasoning effort reach the request log",
		[]snapSeed{one},
		[]snapshotReply{snapJSONReply(http.MethodPost, pathChat, http.StatusOK, snapChatResponse, "X-Request-Id", "up-chat-1", "X-Other", "dropped")},
		snapExecute(chatOne)))

	add(snapCase("exec_chat_stream", "OpenAI chat completion streamed through host.stream.emit; stream_options.include_usage is added, keepalives dropped, the role-only prelude held until output and [DONE] kept",
		[]snapSeed{one},
		[]snapshotReply{snapChatStream("up-chat-2")},
		snapExecute(snapExec{seed: one, format: "openai", model: "model-a", payload: snapChatStreamPayload, stream: true, streamID: "stream-chat-1", requestID: "req-chat-2"})))

	add(snapCase("exec_chat_stream_buffered", "A stream requested without stream_id (hosts without a stream bridge) comes back whole in the RPC response",
		[]snapSeed{one},
		[]snapshotReply{snapChatStream("up-chat-3")},
		snapExecute(snapExec{seed: one, format: "openai", model: "model-a", payload: snapChatStreamPayload, stream: true, requestID: "req-chat-3"})))

	add(snapCase("exec_chat_model_rewrite", "A model that still carries the auth prefix and a thinking suffix is sent upstream as the bare ID, edited in place",
		[]snapSeed{one},
		[]snapshotReply{snapJSONReply(http.MethodPost, pathChat, http.StatusOK, snapChatResponse)},
		snapExecute(snapExec{seed: one, format: "openai", model: "opendesign/model-a(high)", client: "opendesign/model-a(high)",
			payload: `{"messages":[{"role":"user","content":"Hello"}],  "model" : "opendesign/model-a(high)", "stream_options":{"include_usage":false}}`})))

	add(snapCase("exec_responses_nonstream", "OpenAI Responses, buffered, with cached and reasoning tokens",
		[]snapSeed{one},
		[]snapshotReply{snapJSONReply(http.MethodPost, pathResponses, http.StatusOK, snapResponsesResponse, "Request-Id", "up-resp-1")},
		snapExecute(snapExec{seed: one, format: "openai-response", model: "model-a", payload: snapResponsesPayload, requestID: "req-resp-1"})))

	add(snapCase("exec_responses_stream", "OpenAI Responses streamed: the created/in_progress prelude is held until output and [DONE] is dropped",
		[]snapSeed{one},
		[]snapshotReply{snapSSEReply(pathResponses, "up-resp-2",
			"event: response.created\n"+`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_2","object":"response","status":"in_progress","model":"model-a","output":[]}}`,
			"event: response.in_progress\n"+`data: {"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_2","object":"response","status":"in_progress","model":"model-a","output":[]}}`,
			"event: response.output_item.added\n"+`data: {"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"type":"message","id":"msg_2","role":"assistant","content":[]}}`,
			"event: response.output_text.delta\n"+`data: {"type":"response.output_text.delta","sequence_number":3,"output_index":0,"content_index":0,"delta":"Hi"}`,
			"event: response.completed\n"+`data: {"type":"response.completed","sequence_number":4,"response":{"id":"resp_2","object":"response","status":"completed","model":"model-a","output":[{"type":"message","id":"msg_2","role":"assistant","content":[{"type":"output_text","text":"Hi"}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`,
			"data: [DONE]",
		)},
		snapExecute(snapExec{seed: one, format: "openai-response", model: "model-a", payload: `{"model":"model-a","input":"Hello","stream":true}`, stream: true, streamID: "stream-resp-1", requestID: "req-resp-2"})))

	add(snapCase("exec_messages_nonstream", "Anthropic Messages, buffered: Anthropic-Version and Anthropic-Beta are forwarded, the thinking budget is logged as the effort, cache tokens are counted",
		[]snapSeed{one},
		[]snapshotReply{snapJSONReply(http.MethodPost, pathMessages, http.StatusOK, snapMessagesResponse, "Request-Id", "up-msg-1")},
		snapExecute(snapExec{seed: one, format: "claude", model: "model-a", payload: snapMessagesPayload, header: anthropicHeaders, requestID: "req-msg-1"})))

	add(snapCase("exec_messages_stream", "Anthropic Messages streamed without client headers: the default Anthropic-Version is added",
		[]snapSeed{one},
		[]snapshotReply{snapMessagesStream("up-msg-2")},
		snapExecute(snapExec{seed: one, format: "claude", model: "model-a", payload: snapMessagesStreamPayload, stream: true, streamID: "stream-msg-1", requestID: "req-msg-2"})))

	// ---- stream gate repairs ----

	add(snapCase("stream_fix_redacted_thinking", "A redacted_thinking block start followed by thinking_delta and signature_delta is rewritten to a thinking block; a real redacted_thinking block passes as sent",
		[]snapSeed{one},
		[]snapshotReply{snapSSEReply(pathMessages, "up-fix-1",
			snapMessageStart,
			snapBlock("content_block_start", 0, `,"content_block":{"type":"redacted_thinking","data":""}`),
			snapBlock("content_block_delta", 0, `,"delta":{"type":"thinking_delta","thinking":"Let me think."}`),
			snapBlock("content_block_delta", 0, `,"delta":{"type":"signature_delta","signature":"sig-abc"}`),
			snapBlock("content_block_stop", 0, ``),
			snapBlock("content_block_start", 1, `,"content_block":{"type":"redacted_thinking","data":"opaque"}`),
			snapBlock("content_block_stop", 1, ``),
			snapBlock("content_block_start", 2, `,"content_block":{"type":"text","text":""}`),
			snapBlock("content_block_delta", 2, `,"delta":{"type":"text_delta","text":"Done."}`),
			snapBlock("content_block_stop", 2, ``),
			snapMessageDelta("end_turn", 9),
			snapMessageStop,
		)},
		snapExecute(snapExec{seed: one, format: "claude", model: "model-a", payload: snapMessagesStreamPayload, stream: true, streamID: "stream-fix-1", header: anthropicHeaders})))

	add(snapCase("stream_fix_tool_use_stop", "A stream that started a tool_use block and ends with stop_reason end_turn reports tool_use",
		[]snapSeed{one},
		[]snapshotReply{snapSSEReply(pathMessages, "up-fix-2",
			snapMessageStart,
			snapBlock("content_block_start", 0, `,"content_block":{"type":"text","text":""}`),
			snapBlock("content_block_delta", 0, `,"delta":{"type":"text_delta","text":"Checking."}`),
			snapBlock("content_block_stop", 0, ``),
			snapBlock("content_block_start", 1, `,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}`),
			snapBlock("content_block_delta", 1, `,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Paris\"}"}`),
			snapBlock("content_block_stop", 1, ``),
			snapMessageDelta("end_turn", 21),
			snapMessageStop,
		)},
		snapExecute(snapExec{seed: one, format: "claude", model: "model-a", stream: true, streamID: "stream-fix-2",
			payload: `{"model":"model-a","max_tokens":1024,"stream":true,"tools":[{"name":"get_weather","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"Weather in Paris?"}]}`})))

	add(snapCase("stream_fix_max_tokens", "A Messages stream that stops after the last content_block_stop without message_delta/message_stop, for a request with max_tokens, is closed with stop_reason max_tokens",
		[]snapSeed{one},
		[]snapshotReply{snapSSEReply(pathMessages, "up-fix-3",
			snapMessageStart,
			snapBlock("content_block_start", 0, `,"content_block":{"type":"text","text":""}`),
			snapBlock("content_block_delta", 0, `,"delta":{"type":"text_delta","text":"A long ans"}`),
			snapBlock("content_block_stop", 0, ``),
		)},
		snapExecute(snapExec{seed: one, format: "claude", model: "model-a", stream: true, streamID: "stream-fix-3",
			payload: `{"model":"model-a","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"Write an essay"}]}`})))

	// ---- upstream errors ----

	add(snapCase("error_400", "HTTP 400 with an OpenAI-style error body fails the call with the upstream code",
		[]snapSeed{one},
		[]snapshotReply{snapJSONReply(http.MethodPost, pathChat, http.StatusBadRequest, `{"error":{"message":"messages: field required","type":"invalid_request_error","code":"invalid_request"}}`, "X-Request-Id", "up-err-400")},
		snapExecute(chatOne)))

	add(snapCase("error_401", "HTTP 401 on a stream request fails the call before the stream opens",
		[]snapSeed{one},
		[]snapshotReply{snapJSONReply(http.MethodPost, pathChat, http.StatusUnauthorized, `{"error":{"message":"Invalid API key","type":"authentication_error","code":"invalid_api_key"}}`)},
		snapExecute(snapExec{seed: one, format: "openai", model: "model-a", payload: snapChatStreamPayload, stream: true, streamID: "stream-err-401"})))

	add(snapCase("error_403_json", "HTTP 403 with an Anthropic-style error body on Messages keeps its status",
		[]snapSeed{one},
		[]snapshotReply{snapJSONReply(http.MethodPost, pathMessages, http.StatusForbidden, `{"type":"error","error":{"type":"permission_error","message":"model is blocked by the workspace privacy settings"}}`)},
		snapExecute(snapExec{seed: one, format: "claude", model: "model-a", payload: snapMessagesPayload})))

	add(snapCase("error_403_cloudflare", "HTTP 403 with a Cloudflare HTML page is reported with the page text",
		[]snapSeed{one},
		[]snapshotReply{snapBlocked(http.MethodPost, pathChat)},
		snapExecute(chatOne)))

	add(snapCase("error_429_retry_after", "HTTP 429 with Retry-After on a stream request is retryable and carries retry_after_seconds",
		[]snapSeed{one},
		[]snapshotReply{snapJSONReply(http.MethodPost, pathChat, http.StatusTooManyRequests, `{"error":{"message":"5-hour limit reached","type":"rate_limit_error","code":"usage_limit"}}`, "Retry-After", "30")},
		snapExecute(snapExec{seed: one, format: "openai", model: "model-a", payload: snapChatStreamPayload, stream: true, streamID: "stream-err-429", requestID: "req-429"})))

	add(snapCase("error_500", "HTTP 500 on Responses is retryable",
		[]snapSeed{one},
		[]snapshotReply{snapJSONReply(http.MethodPost, pathResponses, http.StatusInternalServerError, `{"error":{"message":"internal error"}}`)},
		snapExecute(snapExec{seed: one, format: "openai-response", model: "model-a", payload: snapResponsesPayload})))

	add(snapCase("error_plain_text", "A plain-text error body becomes the message under an upstream_<status> code",
		[]snapSeed{one},
		[]snapshotReply{{Method: http.MethodPost, Path: pathChat, Status: http.StatusBadGateway, Header: map[string][]string{"Content-Type": {"text/plain; charset=utf-8"}}, Body: "upstream connect error or disconnect/reset before headers\n"}},
		snapExecute(chatOne)))

	add(snapCase("error_string_body", `A {"error":"msg"} body (HTTP 402) is read as the message`,
		[]snapSeed{one},
		[]snapshotReply{snapJSONReply(http.MethodPost, pathChat, http.StatusPaymentRequired, `{"error":"insufficient credits"}`)},
		snapExecute(chatOne)))

	add(snapCase("error_code_prefix", "A [code=...]-prefixed message loses the prefix, and HTTP 404 (unknown model) becomes 400",
		[]snapSeed{one},
		[]snapshotReply{snapJSONReply(http.MethodPost, pathChat, http.StatusNotFound, `{"error":{"message":"[code=model_not_found] The requested model was not found.","type":"invalid_request_error","code":"model_not_found"}}`)},
		snapExecute(chatOne)))

	add(snapCase("error_stream_prelude", "An error event inside the stream before any output closes the stream with the mapped error (overloaded -> 503)",
		[]snapSeed{one},
		[]snapshotReply{snapSSEReply(pathMessages, "up-err-prelude",
			snapMessageStart,
			snapPing,
			"event: error\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
		)},
		snapExecute(snapExec{seed: one, format: "claude", model: "model-a", payload: snapMessagesStreamPayload, stream: true, streamID: "stream-err-prelude"})))

	add(snapCase("error_stream_midway", "An error event after output has started is passed to the client as is",
		[]snapSeed{one},
		[]snapshotReply{snapSSEReply(pathChat, "up-err-midway",
			snapChatEvent(`"choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"},"finish_reason":null}]}`),
			`data: {"error":{"message":"upstream connection reset","type":"server_error","code":"stream_reset"}}`,
		)},
		snapExecute(snapExec{seed: one, format: "openai", model: "model-a", payload: snapChatStreamPayload, stream: true, streamID: "stream-err-midway"})))

	add(snapCase("error_stream_truncated", "A stream that stops inside a content block is closed as stream_truncated",
		[]snapSeed{one},
		[]snapshotReply{snapSSEReply(pathMessages, "up-err-trunc",
			snapMessageStart,
			snapBlock("content_block_start", 0, `,"content_block":{"type":"text","text":""}`),
			snapBlock("content_block_delta", 0, `,"delta":{"type":"text_delta","text":"Hal"}`),
		)},
		snapExecute(snapExec{seed: one, format: "claude", model: "model-a", payload: snapMessagesStreamPayload, stream: true, streamID: "stream-err-trunc"})))

	noKey := snapSeed{name: "opendesign-nokey.json", index: "idx-1", fields: map[string]any{"type": ProviderID, "label": "no key"}}
	add(snapCase("error_request_rejected", "Requests rejected before calling OpenDesign: no api_key, an unsupported format, a payload that is not an object",
		[]snapSeed{one},
		nil,
		snapExecute(snapExec{seed: noKey, format: "openai", model: "model-a", payload: snapChatPayload}),
		snapExecute(snapExec{seed: one, format: "gemini", model: "model-a", payload: snapChatPayload}),
		snapExecute(snapExec{seed: one, format: "openai", model: "model-a", payload: `["not","an","object"]`})))

	add(snapCase("count_tokens", "Token counting and raw HTTP requests are not supported (501)",
		[]snapSeed{one},
		nil,
		snapAs(snapExecute(snapExec{seed: one, format: "claude", model: "model-a", payload: snapMessagesPayload}), pluginabi.MethodExecutorCountTokens),
		snapCall(pluginabi.MethodExecutorHTTPRequest, map[string]any{"AuthID": one.name, "Method": "GET", "URL": "https://example.invalid/v1/models"})))

	// ---- model catalog ----

	add(snapCase("catalog_models_for_auth", "model.for_auth loads the key's /v1/models with its bearer key once; all-models keys get the chat models, selected models keep IDs the plan does not list",
		[]snapSeed{one, two},
		[]snapshotReply{snapModelsReply(snapModelsOne), snapModelsReply(snapModelsTwo)},
		snapEmpty(pluginabi.MethodModelStatic),
		snapModelsForAuth(one),
		snapModelsForAuth(one),
		snapModelsForAuth(two)))

	add(snapCase("catalog_two_keys_merged", "Two keys with different plans: the models view merges both lists and names the keys that list each model",
		[]snapSeed{one, two},
		[]snapshotReply{snapModelsReply(snapModelsOne), snapModelsReply(snapModelsTwo)},
		snapModelsForAuth(one),
		snapModelsForAuth(two),
		snapManage(http.MethodGet, "/models", nil, nil),
		snapManage(http.MethodGet, "/status", nil, nil)))

	add(snapCase("catalog_refresh", "The catalog refresh (the background loop's work): every key's list is reloaded, files whose models predate it are re-saved; a refresh where no list loads fails",
		[]snapSeed{one, two},
		[]snapshotReply{
			snapModelsReply(snapModelsOne), snapModelsReply(snapModelsTwo),
			snapModelsReply(snapModelsOne), snapModelsReply(snapModelsTwo),
			snapJSONReply(http.MethodGet, pathModels, http.StatusInternalServerError, `{"error":{"message":"catalog unavailable"}}`),
			snapJSONReply(http.MethodGet, pathModels, http.StatusServiceUnavailable, `{"error":{"message":"maintenance"}}`),
		},
		snapManage(http.MethodGet, "/models", nil, nil),
		snapManage(http.MethodPost, "/models/refresh", nil, nil),
		snapManage(http.MethodPost, "/models/refresh", nil, nil)))

	// ---- credentials ----

	add(snapCase("credential_add", "Adding a key checks it by listing its models, saves it under its fingerprint and shows it in the status view",
		[]snapSeed{one},
		[]snapshotReply{snapModelsReply(snapModelsOne)},
		snapManage(http.MethodPost, "/credentials", nil, map[string]any{"api_key": snapKeyNew, "label": "New key", "note": "added by the test", "models": []string{"model-a", "model-x"}, "prefix": "/team/"}),
		snapManage(http.MethodGet, "/status", nil, nil)))

	add(snapCase("credential_add_rejected", "A key the model list rejects is not saved; malformed input is refused before any request",
		[]snapSeed{one},
		[]snapshotReply{snapJSONReply(http.MethodGet, pathModels, http.StatusUnauthorized, `{"error":{"message":"Invalid API key","code":"invalid_api_key"}}`)},
		snapManage(http.MethodPost, "/credentials", nil, map[string]any{"api_key": snapKeyBad}),
		snapManage(http.MethodPost, "/credentials", nil, map[string]any{"api_key": "has space"}),
		snapManage(http.MethodPost, "/credentials", nil, map[string]any{"api_key": snapKeyNew, "proxy_url": "ftp://proxy.invalid"}),
		snapManage(http.MethodPost, "/credentials", nil, map[string]any{"api_key": snapKeyNew, "proxy_id": "missing"}),
		snapManage(http.MethodPost, "/credentials", nil, "not json")))

	add(snapCase("credential_add_duplicate", "A key already added is refused; skip_verify saves without the check, loading the model list for registration",
		[]snapSeed{one},
		[]snapshotReply{snapModelsReply(snapModelsTwo)},
		snapManage(http.MethodPost, "/credentials", nil, map[string]any{"api_key": snapKeyOne}),
		snapManage(http.MethodPost, "/credentials", nil, map[string]any{"api_key": snapKeyNew, "skip_verify": true})))

	add(snapCase("credential_update", "Editing a credential rewrites its auth file (label, models, prefix, priority, proxy, base URL); the key itself is immutable",
		[]snapSeed{one, two},
		[]snapshotReply{snapModelsReply(snapModelsTwo)},
		snapManage(http.MethodPatch, "/credentials", nil, map[string]any{"auth_index": "idx-2", "label": "Renamed", "models": nil, "prefix": nil, "priority": 3, "proxy_url": "direct", "base_url": "{{UPSTREAM}}/v1/", "note": ""}),
		snapManage(http.MethodPatch, "/credentials", nil, map[string]any{"auth_index": "idx-2", "api_key": snapKeyNew}),
		snapManage(http.MethodPatch, "/credentials", nil, map[string]any{"auth_index": "idx-9", "label": "x"}),
		snapManage(http.MethodPatch, "/credentials", nil, map[string]any{"name": "opendesign-k1.json", "models": "model-a"})))

	codex := snapSeed{name: "codex-user.json", index: "idx-3", fields: map[string]any{"type": "codex", "email": "user@example.com"}}
	add(snapCase("credential_auth_parse", "The auth RPCs CPA calls for files: parse (opendesign, other providers, no key), refresh with metadata, and the unsupported login flow",
		[]snapSeed{one},
		nil,
		snapEmpty(pluginabi.MethodAuthIdentifier),
		snapAuthParse(two, snapshotHostSummary),
		snapAuthParse(codex, snapshotHostSummary),
		snapAuthParse(noKey, snapshotHostSummary),
		snapCall(pluginabi.MethodAuthRefresh, rpcAuthRefreshRequest{AuthRefreshRequest: pluginapi.AuthRefreshRequest{
			AuthID: one.name, AuthProvider: ProviderID, StorageJSON: one.raw(), Metadata: map[string]any{"label": "Relabelled", "prefix": "mine"}, Host: snapshotHostSummary,
		}}),
		snapCall(pluginabi.MethodAuthLoginStart, pluginapi.AuthLoginStartRequest{Provider: ProviderID}),
		snapCall(pluginabi.MethodAuthLoginPoll, pluginapi.AuthLoginPollRequest{Provider: ProviderID, State: "x"})))

	// ---- account sign-in (vela device authorization) ----

	add(snapCase("account_login_approve", "Signing an account in: device authorization, a pending poll, approval, workspaces, saving, /complete; then the plan windows and sign-out",
		[]snapSeed{one},
		[]snapshotReply{
			snapDevice("dev-1", 1),
			snapToken("dev-1", `{"status":"pending"}`),
			snapToken("dev-1", snapApproved("ck-snap-new")),
			snapJSONReply(http.MethodGet, pathWorkspaces, http.StatusOK, snapWorkspaces),
			snapModelsReply(snapModelsOne),
			snapDeviceCall("dev-1", "complete", http.StatusOK),
			snapFundingReply(), snapSnapshotReply(),
			snapRevoke(http.StatusOK),
		},
		snapManage(http.MethodPost, "/account/login", nil, map[string]any{"auth_index": "idx-1"}),
		snapManage(http.MethodGet, "/account/login", snapQuery("id", "{{login_id}}"), nil),
		snapManage(http.MethodGet, "/credentials/quota", snapQuery("auth_index", "idx-1", "refresh", "1"), nil),
		snapManage(http.MethodGet, "/status", nil, nil),
		snapManage(http.MethodDelete, "/account", snapQuery("auth_index", "idx-1"), nil),
		snapManage(http.MethodDelete, "/account", snapQuery("auth_index", "idx-1"), nil)))

	add(snapCase("account_login_cancel", "Cancelling a pending sign-in tells the account API to drop it",
		[]snapSeed{one},
		[]snapshotReply{snapDevice("dev-1", 600), snapDeviceCall("dev-1", "cancel", http.StatusOK)},
		snapManage(http.MethodPost, "/account/login", nil, map[string]any{"auth_index": "idx-1"}),
		snapManage(http.MethodGet, "/account/login", snapQuery("id", "{{login_id}}"), nil),
		snapManage(http.MethodDelete, "/account/login", snapQuery("id", "{{login_id}}"), nil),
		snapManage(http.MethodGet, "/account/login", snapQuery("id", "{{login_id}}"), nil)))

	add(snapCase("account_login_replace_pending", "A new sign-in on the same card cancels the pending one",
		[]snapSeed{one},
		[]snapshotReply{snapDevice("dev-1", 600), snapDevice("dev-2", 600), snapDeviceCall("dev-1", "cancel", http.StatusOK), snapDeviceCall("dev-2", "cancel", http.StatusOK)},
		snapManage(http.MethodPost, "/account/login", nil, map[string]any{"auth_index": "idx-1"}),
		snapManage(http.MethodPost, "/account/login", nil, map[string]any{"auth_index": "idx-1"}),
		snapManage(http.MethodDelete, "/account/login", snapQuery("id", "{{login_id}}"), nil)))

	add(snapCase("account_login_replace_keys", "Signing in again on a signed-in card saves the new keys and revokes the replaced ones",
		[]snapSeed{signedIn},
		[]snapshotReply{
			snapDevice("dev-1", 1),
			snapToken("dev-1", snapApproved("ck-snap-new")),
			snapJSONReply(http.MethodGet, pathWorkspaces, http.StatusOK, snapWorkspaces),
			snapModelsReply(snapModelsOne),
			snapDeviceCall("dev-1", "complete", http.StatusOK),
			snapRevoke(http.StatusOK),
		},
		snapManage(http.MethodPost, "/account/login", nil, map[string]any{"auth_index": "idx-1"}),
		snapManage(http.MethodGet, "/account/login", snapQuery("id", "{{login_id}}"), nil)))

	add(snapCase("account_login_save_failure", "Keys that cannot be saved are revoked and the sign-in fails",
		[]snapSeed{{name: one.name, index: one.index, fields: one.fields, readOnly: true}},
		[]snapshotReply{
			snapDevice("dev-1", 1),
			snapToken("dev-1", snapApproved("ck-snap-new")),
			snapJSONReply(http.MethodGet, pathWorkspaces, http.StatusInternalServerError, `{"error":"workspace service down"}`),
			snapRevoke(http.StatusOK),
		},
		snapManage(http.MethodPost, "/account/login", nil, map[string]any{"auth_index": "idx-1"}),
		snapManage(http.MethodGet, "/account/login", snapQuery("id", "{{login_id}}"), nil)))

	add(snapCase("account_login_complete_failure", "When /complete fails the auth file is restored and the new keys are revoked",
		[]snapSeed{one},
		[]snapshotReply{
			snapDevice("dev-1", 1),
			snapToken("dev-1", snapApproved("ck-snap-new")),
			snapJSONReply(http.MethodGet, pathWorkspaces, http.StatusOK, snapWorkspaces),
			snapModelsReply(snapModelsOne),
			snapDeviceCall("dev-1", "complete", http.StatusInternalServerError),
			snapRevoke(http.StatusUnauthorized),
		},
		snapManage(http.MethodPost, "/account/login", nil, map[string]any{"auth_index": "idx-1"}),
		snapManage(http.MethodGet, "/account/login", snapQuery("id", "{{login_id}}"), nil)))

	add(snapCase("account_login_denied", "A sign-in the user refused ends as denied; a 4xx from the token endpoint ends it as failed",
		[]snapSeed{one},
		[]snapshotReply{
			snapDevice("dev-1", 1),
			snapToken("dev-1", `{"status":"cancelled"}`),
			snapDevice("dev-2", 1),
			snapJSONReply(http.MethodPost, pathDevice+"/dev-2/token", http.StatusGone, `{"code":"device_authorization_expired","message":"device authorization expired"}`),
		},
		snapManage(http.MethodPost, "/account/login", nil, map[string]any{"auth_index": "idx-1"}),
		snapManage(http.MethodGet, "/account/login", snapQuery("id", "{{login_id}}"), nil),
		snapManage(http.MethodPost, "/account/login", nil, map[string]any{"auth_index": "idx-1"}),
		snapManage(http.MethodGet, "/account/login", snapQuery("id", "{{login_id}}"), nil)))

	add(snapCase("account_start_errors", "Sign-in and sign-out requests refused before or by the account API",
		[]snapSeed{one},
		[]snapshotReply{snapJSONReply(http.MethodPost, pathDevice, http.StatusInternalServerError, `{"error":{"code":"internal","message":"try again later"}}`)},
		snapManage(http.MethodPost, "/account/login", nil, map[string]any{}),
		snapManage(http.MethodPost, "/account/login", nil, map[string]any{"auth_index": "idx-9"}),
		snapManage(http.MethodPost, "/account/login", nil, map[string]any{"auth_index": "idx-1"}),
		snapManage(http.MethodGet, "/account/login", snapQuery("id", "nope"), nil),
		snapManage(http.MethodDelete, "/account/login", snapQuery("id", "nope"), nil),
		snapManage(http.MethodDelete, "/account", snapQuery("auth_index", "idx-1"), nil)))

	add(snapCase("account_blocked_cloudflare", "A Cloudflare HTML 403 is a block, not an expired sign-in: on the windows it is reported by page title; on the optional snapshot the windows stay",
		[]snapSeed{signedIn},
		[]snapshotReply{
			snapBlocked(http.MethodGet, pathFunding),
			snapFundingReply(),
			snapBlocked(http.MethodGet, pathSnapshot),
		},
		snapManage(http.MethodGet, "/credentials/quota", snapQuery("auth_index", "idx-1", "refresh", "1"), nil),
		snapQuotaFetch(signedIn, ProviderID)))

	add(snapCase("account_rejected_expired", "The account API rejecting the control key marks the sign-in expired; signing out still clears the fields",
		[]snapSeed{signedIn},
		[]snapshotReply{
			snapJSONReply(http.MethodGet, pathFunding, http.StatusUnauthorized, `{"error":{"code":"invalid_control_key","message":"control key revoked"}}`),
			snapJSONReply(http.MethodGet, pathFunding, http.StatusForbidden, `{"code":"forbidden","message":"not a member of this workspace"}`),
			snapRevoke(http.StatusUnauthorized),
			snapModelsReply(snapModelsOne),
		},
		snapManage(http.MethodGet, "/credentials/quota", snapQuery("auth_index", "idx-1", "refresh", "1"), nil),
		snapQuotaFetch(signedIn, ProviderID),
		snapManage(http.MethodDelete, "/account", snapQuery("auth_index", "idx-1"), nil)))

	// ---- quota ----

	add(snapCase("quota_fetch_signed_in", "quota.fetch for a signed-in key: the workspace is picked once, plan windows and balance are read fresh each time; quota.reset is refused",
		[]snapSeed{signedInNoWorkspace},
		[]snapshotReply{
			snapJSONReply(http.MethodGet, pathWorkspaces, http.StatusOK, snapWorkspaces),
			snapFundingReply(), snapSnapshotReply(),
			snapFundingReply(), snapSnapshotReply(),
		},
		snapEmpty(pluginabi.MethodQuotaIdentifier),
		snapEmpty(pluginabi.MethodQuotaDescribe),
		snapQuotaFetch(signedInNoWorkspace, ProviderID),
		snapQuotaFetch(signedInNoWorkspace, ""),
		snapCall(pluginabi.MethodQuotaReset, map[string]any{"auth_index": "idx-1", "provider": ProviderID})))

	add(snapCase("quota_fetch_api_key_only", "quota.fetch for a key without a signed-in account, another provider, no auth_index and an unknown one",
		[]snapSeed{one},
		nil,
		snapQuotaFetch(one, ProviderID),
		snapQuotaFetch(one, "codex"),
		snapQuotaFetch(snapSeed{name: one.name, fields: one.fields}, ProviderID),
		snapQuotaFetch(snapSeed{name: "gone.json", index: "idx-9", fields: one.fields}, ProviderID)))

	add(snapCase("quota_management_view", "The card's figures: request log usage over 5h/24h/7d/30d plus the account's windows, cached until refresh=1",
		[]snapSeed{signedIn},
		[]snapshotReply{
			snapJSONReply(http.MethodPost, pathChat, http.StatusOK, snapChatResponse),
			snapJSONReply(http.MethodPost, pathChat, http.StatusInternalServerError, `{"error":{"message":"boom"}}`),
			snapFundingReply(), snapSnapshotReply(),
			snapFundingReply(), snapSnapshotReply(),
		},
		snapExecute(snapExec{seed: signedIn, format: "openai", model: "model-a", payload: snapChatPayload}),
		snapExecute(snapExec{seed: signedIn, format: "openai", model: "model-a", payload: snapChatPayload}),
		snapManage(http.MethodGet, "/credentials/quota", snapQuery("auth_index", "idx-1"), nil),
		snapManage(http.MethodGet, "/credentials/quota", snapQuery("auth_index", "idx-1"), nil),
		snapManage(http.MethodGet, "/credentials/quota", snapQuery("auth_index", "idx-1", "refresh", "1"), nil),
		snapManage(http.MethodGet, "/credentials/quota", snapQuery("auth_index", "idx-9"), nil)))

	// ---- management views ----

	add(snapCase("management_status", "The status view with a signed-in key, a key with its own prefix and selection, and another provider's file; then with CPA's global proxy and force-model-prefix known",
		[]snapSeed{signedIn, two, codex},
		nil,
		snapManage(http.MethodGet, "/status", nil, nil),
		snapAuthParse(two, pluginapi.HostConfigSummary{AuthDir: snapshotAuthDir, ProxyURL: "socks5://cpa:secret@127.0.0.1:1080", ForceModelPrefix: true}),
		snapManage(http.MethodGet, "/status", nil, nil)))

	add(snapCase("management_logs", "The request log after successful, streamed and failed calls on two keys, its filters, the per-key 5h/24h/7d/30d usage and clearing",
		[]snapSeed{one, two},
		[]snapshotReply{
			snapJSONReply(http.MethodPost, pathChat, http.StatusOK, snapChatResponse, "X-Request-Id", "up-log-1"),
			snapMessagesStream("up-log-2"),
			snapJSONReply(http.MethodPost, pathChat, http.StatusTooManyRequests, `{"error":{"message":"weekly limit reached","code":"usage_limit"}}`, "Retry-After", "120", "X-Request-Id", "up-log-3"),
		},
		snapExecute(snapExec{seed: one, format: "openai", model: "model-a", payload: snapChatPayload, requestID: "req-log-1"}),
		snapExecute(snapExec{seed: one, format: "claude", model: "model-a", payload: snapMessagesStreamPayload, stream: true, streamID: "stream-log-2", requestID: "req-log-2", header: http.Header{"User-Agent": {"claude-cli/2.0.0 (external, cli)"}}}),
		snapExecute(snapExec{seed: two, format: "openai", model: "model-b", client: "od2/model-b", payload: `{"model":"model-b","messages":[{"role":"user","content":"Hi"}]}`, requestID: "req-log-3"}),
		snapManage(http.MethodGet, "/logs", nil, nil),
		snapManage(http.MethodGet, "/logs", snapQuery("status", "error"), nil),
		snapManage(http.MethodGet, "/logs", snapQuery("search", "model-a", "limit", "1", "offset", "1"), nil),
		snapManage(http.MethodGet, "/logs", snapQuery("request_id", "req-log-2"), nil),
		snapManage(http.MethodGet, "/logs", snapQuery("from", "yesterday"), nil),
		snapManage(http.MethodGet, "/credentials/quota", snapQuery("auth_index", "idx-1"), nil),
		snapManage(http.MethodGet, "/credentials/quota", snapQuery("auth_index", "idx-2"), nil),
		snapManage(http.MethodDelete, "/logs", nil, nil),
		snapManage(http.MethodGet, "/logs", nil, nil)))

	proxied := two.with(map[string]any{"proxy_url": "socks5://user:secret@127.0.0.1:1080"})
	add(snapCase("management_proxies", "The proxy list: empty, add (URL credentials redacted), duplicate and invalid input, rename, import the credentials' proxies, delete",
		[]snapSeed{one, proxied},
		nil,
		snapManage(http.MethodGet, "/proxies", nil, nil),
		snapManage(http.MethodPost, "/proxies", nil, map[string]any{"name": "Tokyo", "url": "http://user:secret@127.0.0.1:9"}),
		snapManage(http.MethodPost, "/proxies", nil, map[string]any{"name": "Again", "url": "http://user:secret@127.0.0.1:9"}),
		snapManage(http.MethodPost, "/proxies", nil, map[string]any{"name": "Bad", "url": "ftp://127.0.0.1:21"}),
		snapManage(http.MethodPatch, "/proxies", nil, map[string]any{"id": "{{proxy_id}}", "name": "Osaka"}),
		snapManage(http.MethodPost, "/proxies/import", nil, nil),
		snapManage(http.MethodGet, "/proxies", nil, nil),
		snapManage(http.MethodDelete, "/proxies", snapQuery("id", "{{proxy_id}}"), nil),
		snapManage(http.MethodPost, "/proxies/check", nil, map[string]any{"target": "elsewhere"}),
		snapManage(http.MethodGet, "/proxies", nil, nil)))

	add(snapCase("management_routes", "Declared management routes and resources, the logo and flag resources, unknown routes and methods",
		nil,
		nil,
		snapEmpty(pluginabi.MethodManagementRegister),
		snapManage(http.MethodGet, logoPath, nil, nil),
		snapManage(http.MethodGet, flagPath, snapQuery("cc", "jp"), nil),
		snapManage(http.MethodGet, flagPath, snapQuery("cc", "x1"), nil),
		snapManage(http.MethodGet, "/nope", nil, nil),
		snapManage(http.MethodPost, "/status", nil, nil),
		snapManage(http.MethodPut, "/logs", nil, nil)))

	add(snapCase("management_plugin_lifecycle", "plugin.reconfigure (new prefix; its catalog refresh re-saves the file), identifiers, quiesce, and the after-auth interceptor stamping CPA's request ID",
		[]snapSeed{one},
		[]snapshotReply{snapModelsReply(snapModelsOne)},
		snapCall(pluginabi.MethodPluginReconfigure, cpaLifecycleRequest{
			ConfigYAML:    snapConfig(map[string]any{"model-prefix": "od/", "models-refresh-minutes": 0, "log-retention": 500, "request-timeout-seconds": -1}),
			SchemaVersion: pluginabi.SchemaVersion,
		}),
		snapEmpty(pluginabi.MethodAuthIdentifier),
		snapEmpty(pluginabi.MethodExecutorIdentifier),
		snapEmpty(pluginabi.MethodPluginQuiesce),
		snapEmpty(pluginabi.MethodRequestInterceptBefore),
		snapCall(pluginabi.MethodRequestInterceptAfter, rpcRequestInterceptRequest{RequestInterceptRequest: pluginapi.RequestInterceptRequest{
			RequestID: "exec-1", TraceID: "trace-1", Model: "model-a", Metadata: map[string]any{"selected_auth_id": "someone-else.json"},
		}}),
		snapCall(pluginabi.MethodAuthRefresh, rpcAuthRefreshRequest{AuthRefreshRequest: pluginapi.AuthRefreshRequest{
			AuthID: one.name, AuthProvider: ProviderID, StorageJSON: one.raw(), Metadata: one.fields, Host: snapshotHostSummary,
		}}),
		snapCall(pluginabi.MethodRequestInterceptAfter, rpcRequestInterceptRequest{RequestInterceptRequest: pluginapi.RequestInterceptRequest{
			RequestID: "exec-2", TraceID: "trace-2", Model: "model-a", Headers: http.Header{cpaRequestIDHeader: {"spoofed"}}, Metadata: map[string]any{"selected_auth_id": one.name},
		}}),
		snapCall(pluginabi.MethodRequestInterceptAfter, rpcRequestInterceptRequest{RequestInterceptRequest: pluginapi.RequestInterceptRequest{
			RequestID: "exec-3", Model: "model-a", Metadata: map[string]any{"selected_auth_id": one.name},
		}}),
		snapEmpty("plugin.unknown")))

	return cases
}

// snapAs sends the same request under another method.
func snapAs(r snapshotRPC, method string) snapshotRPC {
	r.Method = method
	return r
}

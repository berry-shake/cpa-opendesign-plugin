package opendesign

import (
	"context"
	"strings"
	"testing"

	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/berry-shake/cpa-provider-core/streamgate"
	"github.com/berry-shake/cpa-provider-core/upstream"
)

// The stream repairs run in the core's stream gate (provider.StreamFix), so
// these tests read OpenDesign's Messages events through it.

func sse(events ...string) string {
	var b strings.Builder
	for _, event := range events {
		b.WriteString(event + "\n\n")
	}
	return b.String()
}

func runGate(t *testing.T, body string, maxTokens int64) (string, []streamgate.Kind, error) {
	t.Helper()
	var out strings.Builder
	var kinds []streamgate.Kind
	config := streamgate.Config{
		Protocol: provider.Messages,
		Errors:   upstream.Errors{Prefix: "opendesign: ", Remap: New().RemapError},
		Table:    provider.StreamErrorsExtended,
		Fix:      &messagesFix{maxTokens: maxTokens},
	}
	gate := streamgate.New(config, true, nil, func(payload []byte) error {
		out.Write(payload)
		return nil
	})
	gate.Tap = func(kind streamgate.Kind, _ *streamgate.Event) { kinds = append(kinds, kind) }
	failure := gate.Finish(context.Background(), streamgate.Pump(strings.NewReader(body), true, gate.Push, func() {}))
	if failure != nil {
		return out.String(), kinds, failure
	}
	return out.String(), kinds, nil
}

const (
	start      = `event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","content":[],"model":"deepseek-v4-flash","usage":{"input_tokens":0,"output_tokens":0}}}`
	blockStart = `event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
	blockDelta = `event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`
	blockStop  = `event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`
)

func TestMessagesStreamCutAtMaxTokens(t *testing.T) {
	out, kinds, err := runGate(t, sse(start, blockStart, blockDelta, blockStop), 16)
	if err != nil {
		t.Fatalf("a stream that stopped at max_tokens must end normally: %v", err)
	}
	if !strings.Contains(out, `"stop_reason":"max_tokens"`) || !strings.Contains(out, `"output_tokens":16`) ||
		!strings.HasSuffix(out, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n") {
		t.Fatalf("closing events missing:\n%s", out)
	}
	if kinds[len(kinds)-1] != streamgate.Terminal {
		t.Fatalf("the synthetic events must be tapped, got %v", kinds)
	}
}

func TestMessagesStreamTruncated(t *testing.T) {
	// A block still open: the stream really broke off.
	if _, _, err := runGate(t, sse(start, blockStart, blockDelta), 16); err == nil {
		t.Fatal("a stream cut inside a block must fail")
	}
	// Without max_tokens the cause is unknown.
	if _, _, err := runGate(t, sse(start, blockStart, blockDelta, blockStop), 0); err == nil {
		t.Fatal("a stream without max_tokens must fail")
	}
	// A complete stream is left alone.
	done := sse(start, blockStart, blockDelta, blockStop,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)
	out, _, err := runGate(t, done, 16)
	if err != nil || strings.Count(out, "event: message_stop") != 1 || strings.Contains(out, "max_tokens") {
		t.Fatalf("complete stream: %v\n%s", err, out)
	}
}

// Events as OpenDesign streams a response that thinks, writes and calls a
// tool (2026-11-14).
const (
	redactedStart = `event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","thinking":"","signature":"","data":"df1bfd87-b2f4-4828-8bbe-2be17f29f7e4-0"}}`
	thinkingDelta = `event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"The","stop_sequence":null}}`
	signature     = `event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"df1bfd87-b2f4-4828-8bbe-2be17f29f7e4-0","stop_sequence":null}}`
	thinkingStop  = `event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`
	toolStart     = `event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_00_9gzq67kraf4lf5ox917v6bsq","name":"get_weather","input":{}}}`
	toolDelta     = `event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Paris\"}","stop_sequence":null}}`
	toolStop      = `event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":1}`
	endTurn       = `event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":""},"usage":{"input_tokens":53,"output_tokens":172,"cache_read_input_tokens":256}}`
	messageStop   = `event: message_stop` + "\n" + `data: {"type":"message_stop"}`
)

// splitEvents returns the data of each event the gate sent.
func splitEvents(t *testing.T, out string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(out), "\n\n") {
		payload := streamgate.NewEvent([]byte(raw)).Payload()
		if payload == nil {
			t.Fatalf("event without JSON data: %q", raw)
		}
		events = append(events, payload)
	}
	return events
}

func TestRedactedThinkingStartBecomesThinking(t *testing.T) {
	out, _, err := runGate(t, sse(start, redactedStart, thinkingDelta, signature, thinkingStop, toolStart, toolDelta, toolStop, endTurn, messageStop), 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "redacted_thinking") {
		t.Fatalf("the thinking block still starts as redacted_thinking:\n%s", out)
	}
	events := splitEvents(t, out)
	if len(events) != 10 {
		t.Fatalf("sent %d events, want all 10:\n%s", len(events), out)
	}
	block, _ := events[1]["content_block"].(map[string]any)
	if events[1]["type"] != "content_block_start" || events[1]["index"] != float64(0) || block["type"] != "thinking" || block["thinking"] != "" || block["signature"] != "" {
		t.Fatalf("thinking block start = %v", events[1])
	}
	if !strings.Contains(out, "event: content_block_start\ndata: {") || !strings.Contains(out, thinkingDelta+"\n\n"+signature+"\n\n") {
		t.Fatalf("the deltas must follow the start as sent:\n%s", out)
	}
	delta, _ := events[8]["delta"].(map[string]any)
	if delta["stop_reason"] != "tool_use" {
		t.Fatalf("a response that called a tool must stop with tool_use, got %v", events[8])
	}
	if usage, _ := events[8]["usage"].(map[string]any); usage["output_tokens"] != float64(172) {
		t.Fatalf("usage changed: %v", events[8])
	}
}

// A redacted_thinking block that carries no thinking is passed on as sent.
func TestRedactedThinkingWithoutDeltasKept(t *testing.T) {
	out, _, err := runGate(t, sse(start, redactedStart, thinkingStop, blockStartAt(1), blockDeltaAt(1), blockStopAt(1), endTurn, messageStop), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, redactedStart+"\n\n"+thinkingStop) {
		t.Fatalf("the redacted block must go through unchanged:\n%s", out)
	}
	if !strings.Contains(out, `"stop_reason":"end_turn"`) {
		t.Fatalf("a response without a tool keeps end_turn:\n%s", out)
	}
}

func TestToolUseStopKeepsMaxTokens(t *testing.T) {
	maxTokens := strings.Replace(endTurn, `"end_turn"`, `"max_tokens"`, 1)
	out, _, err := runGate(t, sse(start, toolStart, toolDelta, toolStop, maxTokens, messageStop), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, maxTokens) {
		t.Fatalf("only end_turn becomes tool_use:\n%s", out)
	}
}

// A stream that ends right after a held start is still truncated, and the
// start is not lost.
func TestHeldStartAtEndOfStream(t *testing.T) {
	late := strings.Replace(redactedStart, `"index":0`, `"index":1`, 1)
	out, _, err := runGate(t, sse(start, blockStart, blockDelta, blockStop, late), 16)
	if err == nil {
		t.Fatal("a stream that ends inside a block must fail")
	}
	if !strings.HasSuffix(out, late+"\n\n") {
		t.Fatalf("the held start was dropped:\n%s", out)
	}
}

func blockStartAt(i int) string {
	return strings.Replace(blockStart, `"index":0`, `"index":`+string(rune('0'+i)), 1)
}

func blockDeltaAt(i int) string {
	return strings.Replace(blockDelta, `"index":0`, `"index":`+string(rune('0'+i)), 1)
}

func blockStopAt(i int) string {
	return strings.Replace(blockStop, `"index":0`, `"index":`+string(rune('0'+i)), 1)
}

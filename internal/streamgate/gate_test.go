package streamgate

import (
	"context"
	"strings"
	"testing"

	"github.com/berry-shake/cpa-opendesign-plugin/internal/opendesign"
)

func sse(events ...string) string {
	var b strings.Builder
	for _, event := range events {
		b.WriteString(event + "\n\n")
	}
	return b.String()
}

func runGate(t *testing.T, body string, maxTokens int64) (string, []Kind, error) {
	t.Helper()
	var out strings.Builder
	var kinds []Kind
	gate := New(opendesign.EndpointMessages, true, nil, func(payload []byte) error {
		out.Write(payload)
		return nil
	})
	gate.MaxTokens = maxTokens
	gate.Tap = func(kind Kind, _ *Event) { kinds = append(kinds, kind) }
	failure := gate.Finish(context.Background(), Pump(strings.NewReader(body), true, gate.Push, func() {}))
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
	if kinds[len(kinds)-1] != Terminal {
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

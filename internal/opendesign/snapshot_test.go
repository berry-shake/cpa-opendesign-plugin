package opendesign

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/berry-shake/cpa-provider-core/core"
	"github.com/berry-shake/cpa-provider-core/coretest"
)

// TestSnapshots replays testdata/snapshots through the plugin (see
// cpa-provider-core's coretest package and README).
//
//	go test ./internal/opendesign -run TestSnapshots           # compare
//	go test ./internal/opendesign -run TestSnapshots -update   # rewrite outputs
func TestSnapshots(t *testing.T) {
	coretest.RunSnapshots(t, coretest.Binding{
		ProviderID: ProviderID,
		NewService: snapshotNewService,
		Cases:      snapshotCases,
		// log_since is when the oldest request log entry was written, by
		// the wall clock.
		VolatileFields: []string{"log_since"},
		// The random IDs of account sign-ins and of proxies.
		GeneratedIDs: []coretest.GeneratedID{
			{Placeholder: "{{login_id}}", Find: snapshotLoginIDs},
			{Placeholder: "{{proxy_id}}", Find: snapshotProxyIDs},
		},
	})
}

// snapshotLoginIDs finds the ID of a sign-in view.
func snapshotLoginIDs(body any) []string {
	view, _ := body.(map[string]any)
	if _, isLogin := view["state"]; !isLogin || view["auth_index"] == nil {
		return nil
	}
	id, _ := view["id"].(string)
	return []string{id}
}

// snapshotProxyIDs finds the IDs of a proxy view or a proxy list.
func snapshotProxyIDs(body any) []string {
	view, _ := body.(map[string]any)
	var out []string
	add := func(item any) {
		if proxy, ok := item.(map[string]any); ok {
			id, _ := proxy["id"].(string)
			out = append(out, id)
		}
	}
	add(view["proxy"])
	if proxies, ok := view["proxies"].([]any); ok {
		for _, item := range proxies {
			add(item)
		}
	}
	return out
}

// snapshotHostname names the machine to the account API (Adapter.hostname).
const snapshotHostname = "cpa-snapshot-host"

// snapshotNewService builds the service the way main.go does, with the
// clock pinned, the catalog refresh loop held back and the egress check
// pointed at the fake upstream; the adapter names snapshotHostname and
// signs accounts in at the fake upstream instead of vela's production
// origin (vela.DefaultAPIURL).
func snapshotNewService(version string, host func(string, []byte) ([]byte, error), upstreamURL string) coretest.Plugin {
	adapter := New()
	adapter.velaAPIURL = upstreamURL
	adapter.hostname = func() (string, error) { return snapshotHostname, nil }
	return seamPlugin{
		Service: core.NewWithOptions(adapter, version, host, core.Options{
			Now:             func() time.Time { return coretest.Clock },
			ManualRefresh:   true,
			EgressCheckURLs: []string{upstreamURL + "/json"},
		}),
		adapter: adapter,
	}
}

// seamPlugin waits for the sign-in polls besides the stream pumps.
type seamPlugin struct {
	*core.Service
	adapter *Adapter
}

// snapshotIdleTimeout bounds the wait for background work after a step.
const snapshotIdleTimeout = 30 * time.Second

// WaitIdle blocks until the plugin's background work has finished: stream
// pumps, and sign-in polls with the calls they lead to. A sign-in that polls
// less often than snapshotLongPoll counts as idle; it cannot poll again
// while a case runs.
func (p seamPlugin) WaitIdle() {
	deadline := time.Now().Add(snapshotIdleTimeout)
	if !p.WaitIdleFor(snapshotIdleTimeout) {
		panic("snapshot: stream pumps did not finish within " + snapshotIdleTimeout.String())
	}
	for {
		reason := p.busy()
		if reason == "" {
			return
		}
		if time.Now().After(deadline) {
			panic("snapshot: background work did not finish:\n" + reason)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// snapshotLongPoll is the shortest poll interval of a pending sign-in that
// counts as idle.
const snapshotLongPoll = time.Minute

// snapshotModules prefix the plugin's own functions in goroutine stacks:
// the adapter and vela client, and the core (but not the snapshot runner,
// which drives the plugin from the test).
var snapshotModules = []string{
	"github.com/berry-shake/cpa-opendesign-plugin/internal/",
	"github.com/berry-shake/cpa-provider-core/",
}

const snapshotRunner = "github.com/berry-shake/cpa-provider-core/coretest."

// busy says what background work is still running, or "".
func (p seamPlugin) busy() string {
	if reason := p.pollingSoon(); reason != "" {
		return reason
	}
	return snapshotBusyGoroutine()
}

// pollingSoon reports a pending sign-in that will poll the account API
// again soon.
func (p seamPlugin) pollingSoon() string {
	logins := p.adapter.logins
	logins.mu.Lock()
	defer logins.mu.Unlock()
	for _, login := range logins.items {
		login.mu.Lock()
		pending, interval := login.state == loginPending, login.interval
		login.mu.Unlock()
		if pending && interval < snapshotLongPoll {
			return "a sign-in polls every " + interval.String()
		}
	}
	return ""
}

// snapshotBusyGoroutine returns the stack of a goroutine running the
// plugin's own (non-test) code, unless it is parked in the select of a
// sign-in poll.
func snapshotBusyGoroutine() string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	for _, block := range strings.Split(string(buf), "\n\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) < 2 || !strings.HasPrefix(lines[0], "goroutine ") {
			continue
		}
		state := lines[0]
		if open, end := strings.Index(state, "["), strings.LastIndex(state, "]"); open >= 0 && end > open {
			state = state[open+1 : end]
		}
		state, _, _ = strings.Cut(state, ",")
		var funcs []string
		own := false
		for i := 1; i < len(lines); i++ {
			line := lines[i]
			if strings.HasPrefix(line, "\t") {
				continue
			}
			if strings.HasPrefix(line, "created by ") {
				break
			}
			file := ""
			if i+1 < len(lines) {
				file = lines[i+1]
			}
			funcs = append(funcs, line)
			if snapshotOwnFrame(line) && !strings.Contains(file, "_test.go:") {
				own = true
			}
		}
		if !own || (state == "select" && snapshotParked(funcs)) {
			continue
		}
		return block
	}
	return ""
}

// snapshotOwnFrame reports a frame of the plugin's own code.
func snapshotOwnFrame(line string) bool {
	if strings.Contains(line, snapshotRunner) {
		return false
	}
	for _, module := range snapshotModules {
		if strings.Contains(line, module) {
			return true
		}
	}
	return false
}

// snapshotParked reports a goroutine blocked in the select of pollLogin
// itself, not in a call it makes.
func snapshotParked(funcs []string) bool {
	for _, fn := range funcs {
		if strings.HasPrefix(fn, "runtime.") {
			continue
		}
		return strings.Contains(fn, "(*Adapter).pollLogin(")
	}
	return false
}

// The case table (snapshot_cases_test.go) predates coretest; these names
// keep it as it was written.
type (
	snapshotFixture     = coretest.Fixture
	snapshotSetup       = coretest.Setup
	snapshotCredential  = coretest.Credential
	snapshotStep        = coretest.Step
	snapshotRPC         = coretest.RPC
	snapshotReply       = coretest.Reply
	cpaLifecycleRequest = coretest.LifecycleRequest
)

var snapshotCaseJSON = coretest.CaseJSON

// snapshotDir is where the fixtures live.
const snapshotDir = coretest.Dir

// snapshotClock is the time the service clock is pinned to.
var snapshotClock = coretest.Clock

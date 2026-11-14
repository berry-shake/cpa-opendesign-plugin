package plugin

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// Snapshot seams: the only place the RPC snapshot tests reach into the
// Service's unexported state. Everything else drives the plugin through
// Service.Call and the host callbacks.

// snapshotProviderID is the auth provider the fake host parses files for.
const snapshotProviderID = ProviderID

// snapshotConcurrentPaths are the upstream routes the plugin calls
// concurrently; OpenDesign has none.
var snapshotConcurrentPaths []string

// snapshotVolatileFields are OpenDesign's own volatile names: log_since is
// when the oldest request log entry was written, by the wall clock.
var snapshotVolatileFields = []string{"log_since"}

// snapshotGeneratedIDs are the random IDs of account sign-ins and of proxies.
var snapshotGeneratedIDs = []snapshotGeneratedID{
	{Placeholder: "{{login_id}}", Find: snapshotLoginIDs},
	{Placeholder: "{{proxy_id}}", Find: snapshotProxyIDs},
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

// snapshotHostname names the machine to the account API (Service.hostname).
const snapshotHostname = "cpa-snapshot-host"

// snapshotNewService builds the service the way main.go does, then pins the
// clock and the host name, keeps plugin.register from starting the catalog
// refresh loop and points the egress check and the vela account API
// (hard-coded to amr-api.open-design.ai) at the fake upstream.
func snapshotNewService(version string, host HostCaller, upstreamURL string) *Service {
	svc := New(version, host)
	svc.now = func() time.Time { return snapshotClock }
	svc.loopOnce.Do(func() {})
	svc.egressCheckURLs = []string{upstreamURL + "/json"}
	svc.velaAPIURL = upstreamURL
	svc.hostname = func() (string, error) { return snapshotHostname, nil }
	return svc
}

// snapshotDropQueuedRefresh discards a queued catalog refresh without
// running it.
func snapshotDropQueuedRefresh(svc *Service) {
	select {
	case <-svc.kick:
	default:
	}
}

// snapshotRunQueuedRefresh runs, synchronously, the refresh the background
// loop would run for a queued kick, and reports whether one was queued.
func snapshotRunQueuedRefresh(svc *Service) bool {
	select {
	case <-svc.kick:
		_, _ = svc.refreshCatalog(false)
		return true
	default:
		return false
	}
}

// snapshotWaitIdle blocks until the plugin's background work has finished:
// stream pumps, and sign-in polls with the calls they lead to. A sign-in
// that polls less often than snapshotLongPoll counts as idle; it cannot poll
// again while a case runs.
func snapshotWaitIdle(svc *Service) {
	svc.streams.Wait()
	deadline := time.Now().Add(30 * time.Second)
	for {
		reason := snapshotBusy(svc)
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

// snapshotModule prefixes the plugin's own functions in goroutine stacks.
const snapshotModule = "github.com/berry-shake/cpa-opendesign-plugin/internal/"

// snapshotBusy says what background work is still running, or "".
func snapshotBusy(svc *Service) string {
	if reason := snapshotPollingSoon(svc); reason != "" {
		return reason
	}
	return snapshotBusyGoroutine()
}

// snapshotPollingSoon reports a pending sign-in that will poll the account
// API again soon.
func snapshotPollingSoon(svc *Service) string {
	svc.logins.mu.Lock()
	defer svc.logins.mu.Unlock()
	for _, login := range svc.logins.items {
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
			if strings.Contains(line, snapshotModule) && !strings.Contains(file, "_test.go:") {
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

// snapshotParked reports a goroutine blocked in the select of pollLogin
// itself, not in a call it makes.
func snapshotParked(funcs []string) bool {
	for _, fn := range funcs {
		if strings.HasPrefix(fn, "runtime.") {
			continue
		}
		return strings.Contains(fn, "(*Service).pollLogin(")
	}
	return false
}

// snapshotSkeletons are the generated inputs of the cases.
func snapshotSkeletons(t *testing.T) []snapshotFixture {
	return snapshotCases(t)
}

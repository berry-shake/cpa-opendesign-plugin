package plugin

import (
	"runtime"
	"strings"
	"time"
)

// The seams of the RPC snapshot tests: the only place they reach into the
// plugin's unexported internals. Everything else drives Service.Call. When
// the plugin moves onto the shared core, replace these functions with the
// core's own hooks.

// snapClock is the pinned clock of every snapshot service (Service.now). It
// lies in the past: the usage windows have no upper bound, so they count the
// requests the request log stamps with the real time.
var snapClock = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

// snapHostname names the machine to the account API (Service.hostname).
const snapHostname = "cpa-snapshot-host"

// snapLongPoll is the shortest poll interval of a pending sign-in that counts
// as settled: it cannot poll again while a case runs.
const snapLongPoll = time.Minute

// snapModule prefixes the plugin's own functions in goroutine stacks.
const snapModule = "github.com/berry-shake/cpa-opendesign-plugin/internal/"

// snapInstallSeams pins the clock, points the vela account API (hard-coded to
// amr-api.open-design.ai) at the fake upstream and fixes the host name. It
// runs before the first RPC, so no goroutine reads these fields yet.
func snapInstallSeams(svc *Service, upstreamURL string) {
	svc.now = func() time.Time { return snapClock }
	svc.velaAPIURL = upstreamURL
	svc.hostname = func() (string, error) { return snapHostname, nil }
}

// snapBusy says what background work of the plugin is still running, or ""
// once everything has finished or is parked in a wait that cannot end while
// a case runs (the catalog refresh loop between kicks, a sign-in whose poll
// interval is at least snapLongPoll).
func snapBusy(svc *Service) string {
	if len(svc.kick) > 0 {
		return "a catalog refresh is queued"
	}
	if reason := snapPollingSoon(svc); reason != "" {
		return reason
	}
	return snapBusyGoroutine()
}

// snapPollingSoon reports a pending sign-in that will poll the account API
// again soon.
func snapPollingSoon(svc *Service) string {
	svc.logins.mu.Lock()
	defer svc.logins.mu.Unlock()
	for _, login := range svc.logins.items {
		login.mu.Lock()
		pending, interval := login.state == loginPending, login.interval
		login.mu.Unlock()
		if pending && interval < snapLongPoll {
			return "a sign-in polls every " + interval.String()
		}
	}
	return ""
}

// snapBusyGoroutine returns the stack of a goroutine running the plugin's
// own (non-test) code, unless it is parked in the select of the catalog
// refresh loop or of a sign-in poll.
func snapBusyGoroutine() string {
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
			if strings.Contains(line, snapModule) && !strings.Contains(file, "_test.go:") {
				own = true
			}
		}
		if !own || (state == "select" && snapParked(funcs)) {
			continue
		}
		return block
	}
	return ""
}

// snapParked reports a goroutine blocked in the select of refreshLoop or
// pollLogin itself, not in a call they make.
func snapParked(funcs []string) bool {
	for _, fn := range funcs {
		if strings.HasPrefix(fn, "runtime.") {
			continue
		}
		return strings.Contains(fn, "(*Service).refreshLoop(") || strings.Contains(fn, "(*Service).pollLogin(")
	}
	return false
}

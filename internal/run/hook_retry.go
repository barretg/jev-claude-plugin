package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A low-confidence locate does not have to end in a full read. The goal the
// hook works from is the user's last message, which is often broader than what
// the agent is looking for at this moment ("fix the flaky test" when the agent
// wants the retry loop). So instead of passing through, the hook blocks the Read
// and asks the agent to state what it wants from this file; the agent records
// that with `jev hook goal` and reads again. After maxRetries sharper goals that
// still fall under the floor, the read goes through whole, as before.
//
// Read has no free-text field, so the sharper goal travels through a small
// per-session state file rather than through the tool call itself.

const (
	maxRetries = 2
	// Entries this old are dropped: a retry that was never taken up must not
	// ambush an unrelated Read of the same file much later.
	retryTTL = 30 * time.Minute
)

type retryEntry struct {
	Attempts int       `json:"attempts"` // sharper goals requested so far
	Goal     string    `json:"goal,omitempty"`
	Updated  time.Time `json:"updated"`
}

type retryState map[string]retryEntry // keyed by absolute file path

func retryDir() string {
	if d := strings.TrimSpace(os.Getenv("JEV_STATE_DIR")); d != "" {
		return d
	}
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "jev", "hook")
}

// retryFile maps a session id to its state file. The id comes from Claude Code
// and is echoed back by the agent, so anything but a plain token is refused
// rather than turned into a path.
func retryFile(session string) (string, bool) {
	if session == "" || len(session) > 128 {
		return "", false
	}
	for _, r := range session {
		if !(r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return "", false
		}
	}
	return filepath.Join(retryDir(), session+".json"), true
}

func loadRetry(session string) retryState {
	st := retryState{}
	p, ok := retryFile(session)
	if !ok {
		return st
	}
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, &st)
	}
	for k, e := range st {
		if time.Since(e.Updated) > retryTTL {
			delete(st, k)
		}
	}
	return st
}

func saveRetry(session string, st retryState) {
	p, ok := retryFile(session)
	if !ok {
		return
	}
	if len(st) == 0 {
		os.Remove(p)
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	b, _ := json.Marshal(st)
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, p)
	}
}

func resolvePath(path, cwd string) string {
	if !filepath.IsAbs(path) && cwd != "" {
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}

// hookGoal is `jev hook goal --session <id> <file> <question...>`: record the
// sharper goal the next Read of <file> in that session should be located with.
func hookGoal(args []string) error {
	var session string
	if len(args) >= 2 && args[0] == "--session" {
		session, args = args[1], args[2:]
	}
	if len(args) < 2 {
		return fmt.Errorf("usage: jev hook goal --session <id> <file> <what you need from the file>")
	}
	if _, ok := retryFile(session); !ok {
		return fmt.Errorf("missing or malformed --session (copy it from the message that asked for this)")
	}
	cwd, _ := os.Getwd()
	path := resolvePath(args[0], cwd)
	goal := strings.TrimSpace(strings.Join(args[1:], " "))
	if len(goal) < 12 {
		return fmt.Errorf("goal too short: describe the code you need in a full sentence")
	}

	st := loadRetry(session)
	e := st[path]
	e.Goal, e.Updated = goal, time.Now()
	st[path] = e
	saveRetry(session, st)
	fmt.Printf("jev: next Read of %s will look for: %s\n", filepath.Base(path), goal)
	return nil
}

// denyForRetry blocks the Read and tells the agent how to sharpen the goal.
func denyForRetry(session, path, goal string, conf float64, attempt, lines int) error {
	reason := fmt.Sprintf(
		"jev held back this Read of %s (a large file) because it could only locate the relevant part with "+
			"confidence %.2f, under the %.2f floor, while looking for: %q. "+
			"Sharper-goal retry %d of %d: state specifically what you need from this file — the function, "+
			"behaviour or identifier, in your own words — by running\n"+
			"  jev hook goal --session %s %q \"<sharper description>\"\n"+
			"and only after it has returned (not in parallel with it), Read the file again exactly as before. "+
			"If the task genuinely needs the whole file (a review, an overview, an edit across it), skip the "+
			"retries and Read it with offset 1 and limit %d instead — jev never touches an explicit window.",
		filepath.Base(path), conf, hookMinConf(), clip(goal, 200), attempt, maxRetries, session, path, lines)
	return json.NewEncoder(os.Stdout).Encode(hookOutput{
		HookSpecificOutput: &hookSpecific{
			HookEventName:            "PreToolUse",
			PermissionDecision:       "deny",
			PermissionDecisionReason: reason,
		},
	})
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

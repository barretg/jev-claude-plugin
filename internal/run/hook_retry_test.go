package run

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHookGoalRecordsAndExpires(t *testing.T) {
	t.Setenv("JEV_STATE_DIR", t.TempDir())
	f := filepath.Join(t.TempDir(), "big.go")
	if err := hookGoal([]string{"--session", "abc-123", f, "the", "function that retries failed uploads"}); err != nil {
		t.Fatal(err)
	}
	e, ok := loadRetry("abc-123")[f]
	if !ok || e.Goal != "the function that retries failed uploads" {
		t.Fatalf("goal not recorded: %+v", e)
	}

	st := loadRetry("abc-123")
	e.Updated = time.Now().Add(-2 * retryTTL)
	st[f] = e
	saveRetry("abc-123", st)
	if _, ok := loadRetry("abc-123")[f]; ok {
		t.Fatal("stale entry survived the TTL")
	}
}

func TestHookGoalRejectsBadInput(t *testing.T) {
	t.Setenv("JEV_STATE_DIR", t.TempDir())
	for _, args := range [][]string{
		{"f.go", "a long enough goal here"},                           // no session
		{"--session", "../../etc", "f.go", "a long enough goal here"}, // path in session id
		{"--session", "abc", "f.go", "short"},                         // goal too short
	} {
		if err := hookGoal(args); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
	if entries, _ := os.ReadDir(os.Getenv("JEV_STATE_DIR")); len(entries) != 0 {
		t.Errorf("rejected input still wrote state: %v", entries)
	}
}

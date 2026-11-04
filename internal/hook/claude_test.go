package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jperdior/detour/internal/ledger"
	"github.com/jperdior/detour/internal/policy"
)

func run(t *testing.T, payload string, ledgerPath string) string {
	t.Helper()
	var out bytes.Buffer
	if err := Claude(strings.NewReader(payload), &out, policy.Config{MinLines: 350}, ledgerPath); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestClaudeBlocksLargeRead(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "big.go"), []byte(strings.Repeat("x\n", 400)), 0o644)
	lp := filepath.Join(dir, "ledger.jsonl")

	payload := `{"session_id":"s1","cwd":"` + dir + `","hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"big.go"}}`
	out := run(t, payload, lp)

	var o claudeOutput
	if err := json.Unmarshal([]byte(out), &o); err != nil {
		t.Fatalf("not JSON: %q", out)
	}
	if o.HookSpecificOutput.PermissionDecision != "deny" || !strings.Contains(o.HookSpecificOutput.PermissionDecisionReason, "detour read") {
		t.Fatalf("got %+v", o)
	}
	ev, _ := ledger.Read(lp)
	if len(ev) != 1 || ev[0].Kind != ledger.KindBlock || ev[0].Lines != 400 || ev[0].SessionID != "s1" {
		t.Fatalf("ledger: %+v", ev)
	}
}

func TestClaudePartialReadIsAllowedAndLogged(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "big.go"), []byte(strings.Repeat("x\n", 400)), 0o644)
	lp := filepath.Join(dir, "ledger.jsonl")
	payload := `{"cwd":"` + dir + `","tool_name":"Read","tool_input":{"file_path":"big.go","offset":10,"limit":40}}`
	if out := run(t, payload, lp); out != "" {
		t.Fatalf("expected no output, got %q", out)
	}
	ev, _ := ledger.Read(lp)
	if len(ev) != 1 || ev[0].Kind != ledger.KindPartial {
		t.Fatalf("ledger: %+v", ev)
	}
}

func TestClaudeFileBudget(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "big.go"), []byte(strings.Repeat("x\n", 2000)), 0o644)
	lp := filepath.Join(dir, "state", "ledger.jsonl")
	cfg := policy.Config{MinLines: 350, FileBudget: 350}
	read := func(session string, offset, limit int) string {
		var out bytes.Buffer
		payload := fmt.Sprintf(`{"session_id":%q,"cwd":%q,"tool_name":"Read","tool_input":{"file_path":"big.go","offset":%d,"limit":%d}}`, session, dir, offset, limit)
		if err := Claude(strings.NewReader(payload), &out, cfg, lp); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if out := read("s1", 0, 300); out != "" {
		t.Fatalf("first chunk should pass: %q", out)
	}
	if out := read("s1", 300, 40); out != "" {
		t.Fatalf("340 lines is within budget: %q", out)
	}
	if out := read("s1", 340, 100); !strings.Contains(out, "already read 340 lines") {
		t.Fatalf("over budget should be denied: %q", out)
	}
	if out := read("s2", 0, 300); out != "" {
		t.Fatalf("budget is per session: %q", out)
	}
	// A whole read in s2 now tells the agent only 50 chunked lines are left.
	if out := read("s2", 0, 0); !strings.Contains(out, "50 are left") {
		t.Fatalf("whole-read block should state the remaining budget: %q", out)
	}
	ev, _ := ledger.Read(lp)
	kinds := ""
	for _, e := range ev {
		kinds += e.Kind + " "
	}
	if kinds != "partial partial block partial block " {
		t.Fatalf("ledger kinds: %q", kinds)
	}
}

func TestClaudeFailsOpen(t *testing.T) {
	lp := filepath.Join(t.TempDir(), "l.jsonl")
	for _, p := range []string{"not json", `{"tool_name":"Read","tool_input":"weird"}`, `{"tool_name":"Edit","tool_input":{}}`} {
		if out := run(t, p, lp); out != "" {
			t.Fatalf("%q: expected pass-through, got %q", p, out)
		}
	}
}

func TestClaudeBash(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "big.go"), []byte(strings.Repeat("x\n", 400)), 0o644)
	payload := `{"cwd":"` + dir + `","tool_name":"Bash","tool_input":{"command":"cat big.go"}}`
	if out := run(t, payload, filepath.Join(dir, "l.jsonl")); !strings.Contains(out, `"deny"`) {
		t.Fatalf("expected deny, got %q", out)
	}
}

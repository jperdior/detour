package hook

import (
	"bytes"
	"encoding/json"
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

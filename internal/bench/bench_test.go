package bench

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A trimmed real `claude -p --output-format json` result.
const claudeJSON = `{"type":"result","subtype":"success","is_error":false,"result":"PersistentPreRun then PreRun",
"total_cost_usd":0.0123,"num_turns":4,"duration_ms":9000,
"usage":{"input_tokens":10,"cache_creation_input_tokens":2000,"cache_read_input_tokens":30000,"output_tokens":400},
"permission_denials":[{"tool_name":"Bash"}]}`

func TestParseClaude(t *testing.T) {
	var r Result
	if err := ParseClaude([]byte("some warning\n"+claudeJSON), &r); err != nil {
		t.Fatal(err)
	}
	if r.ClaudeCostUSD != 0.0123 || r.Turns != 4 || r.ContextTokens() != 32010 || r.OutputTokens != 400 || r.PermissionDenials != 1 {
		t.Fatalf("got %+v", r)
	}
	if r.Error != "" {
		t.Fatalf("unexpected error %q", r.Error)
	}
}

func TestScore(t *testing.T) {
	s := Scenario{Expect: []string{"persistentprerun", `\bPreRun\b`, "PostRun"}}
	ok, missing := Score(s, "PersistentPreRun then PreRun")
	if ok || !slices.Equal(missing, []string{"PostRun"}) {
		t.Fatalf("got %v %v", ok, missing)
	}
}

func TestClaudeArgsOnlyDetourLoadsPlugin(t *testing.T) {
	base := ClaudeArgs("q", "sonnet", Baseline, "/p")
	det := ClaudeArgs("q", "sonnet", Detour, "/p")
	if slices.Contains(base, "--plugin-dir") || !slices.Contains(det, "--plugin-dir") {
		t.Fatalf("base %v\ndet %v", base, det)
	}
	if len(det) != len(base)+2 {
		t.Fatal("variants should differ only by the plugin")
	}
}

func TestLoadScenariosFromRepo(t *testing.T) {
	ss, err := LoadScenarios(filepath.Join("..", "..", "bench", "scenarios"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) == 0 {
		t.Fatal("no scenarios")
	}
	for _, s := range ss {
		if s.Repo == "" || s.Ref == "" || s.Prompt == "" || len(s.Expect) == 0 {
			t.Errorf("incomplete scenario %+v", s)
		}
	}
}

func TestReportDelta(t *testing.T) {
	rs := []Result{
		{Scenario: "a", Variant: Baseline, NetCostUSD: 0.10, Passed: true},
		{Scenario: "a", Variant: Baseline, NetCostUSD: 0.12, Passed: true},
		{Scenario: "a", Variant: Detour, NetCostUSD: 0.05, Passed: true},
		{Scenario: "a", Variant: Detour, Error: "boom"},
	}
	var b bytes.Buffer
	WriteMarkdown(&b, rs)
	out := b.String()
	if !strings.Contains(out, "**-54.5%**") || !strings.Contains(out, "| detour | 1/2 |") {
		t.Fatalf("report:\n%s", out)
	}
}

func TestReadResultsRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.jsonl")
	if err := appendJSONL(p, Result{Scenario: "x", Blocks: 2}); err != nil {
		t.Fatal(err)
	}
	rs, err := ReadResults(p)
	if err != nil || len(rs) != 1 || rs[0].Blocks != 2 {
		t.Fatalf("got %v %v", rs, err)
	}
	_ = os.Remove(p)
}

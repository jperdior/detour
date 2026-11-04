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
		{Scenario: "a", Variant: Baseline, ClaudeCostUSD: 0.10, NetCostUSD: 0.10, Passed: true, CacheReadTokens: 1000},
		{Scenario: "a", Variant: Baseline, ClaudeCostUSD: 0.12, NetCostUSD: 0.12, Passed: true, CacheReadTokens: 3000},
		{Scenario: "a", Variant: Detour, ClaudeCostUSD: 0.04, NetCostUSD: 0.055, Passed: true, CacheReadTokens: 500},
		{Scenario: "a", Variant: Detour, Error: "boom"},
	}
	// A Haiku delegation counts against the plan.
	if (Result{ClaudeCostUSD: 0.04, DelegatePlanCostUSD: 0.01}).PlanCostUSD() != 0.05 {
		t.Fatal("plan cost should include Claude-plan delegations")
	}
	var b bytes.Buffer
	WriteMarkdown(&b, rs)
	out := b.String()
	for _, want := range []string{
		"Claude $ **-63.6%**", // 0.11 → 0.04
		"net $ **-50.0%**",    // 0.11 → 0.055
		"| detour | 1/2 |",
		"| 2000 |", // baseline mean cache read, its own column
		"| Claude $ | 0.1100 | 0.0400 | **-63.6%** |",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("report lacks %q:\n%s", want, out)
		}
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

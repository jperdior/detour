package bench

import (
	"bytes"
	"context"
	"os"
	"os/exec"
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

// Trimmed stream-json: init, one assistant turn with two tool calls, result.
const claudeStream = `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"pattern":"PreRun","path":"/r"}},{"type":"tool_use","name":"Read","input":{"file_path":"/r/command.go","offset":900,"limit":100}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","content":"..."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"detour read -q \"x\" command.go"}}]}}
{"type":"result","subtype":"success","is_error":false,"result":"done","total_cost_usd":0.5,"num_turns":3,"usage":{"input_tokens":1,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":4}}`

func TestParseClaudeStream(t *testing.T) {
	var r Result
	if err := ParseClaude([]byte(claudeStream), &r); err != nil {
		t.Fatal(err)
	}
	if r.ClaudeCostUSD != 0.5 || r.Answer != "done" || r.Tools["Grep"] != 1 || r.Tools["Read"] != 1 || r.Tools["Bash"] != 1 {
		t.Fatalf("got %+v", r)
	}
	want := []string{`Grep "PreRun" r`, "Read command.go [900+100]", `Bash: detour read -q "x" command.go`}
	if !slices.Equal(r.ToolCalls, want) {
		t.Fatalf("tool calls %q", r.ToolCalls)
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
	base := ClaudeArgs("q", "sonnet", Baseline, "/p", "project,local")
	det := ClaudeArgs("q", "sonnet", Detour, "/p", "project,local")
	if slices.Contains(base, "--plugin-dir") || !slices.Contains(det, "--plugin-dir") {
		t.Fatalf("base %v\ndet %v", base, det)
	}
	if !slices.Contains(base, "stream-json") {
		t.Fatal("bench needs stream-json to see tool calls")
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
		if (s.Repo == "" && s.Path == "") || s.Ref == "" || s.Prompt == "" || len(s.Expect) == 0 {
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
		"| detour | 1 | 0.1100 → 0.0400 | **-63.6%** |",
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

// Regression: a relative OutDir made the hook write its ledger inside the
// fixture checkout (the session's cwd), so blocks were never counted.
func TestRunUsesAbsoluteLedgerPath(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "cache", "fake@v1")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Fake claude: records DETOUR_LEDGER and prints a minimal result.
	bin := filepath.Join(tmp, "claude")
	script := "#!/bin/sh\necho \"$DETOUR_LEDGER\" > \"" + tmp + "/ledger-env\"\necho '{\"type\":\"result\",\"result\":\"ok\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(wd) })
	os.Chdir(tmp)

	_, err := Run(context.Background(), Options{
		Scenarios: []Scenario{{Name: "s", Repo: "fake", Ref: "v1", Prompt: "p"}},
		Variants:  []string{Baseline}, Reps: 1,
		OutDir: "out", CacheDir: "cache", PluginDir: "plugin", BinDir: ".", ClaudeBin: bin,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(tmp, "ledger-env"))
	if !filepath.IsAbs(strings.TrimSpace(string(got))) {
		t.Fatalf("DETOUR_LEDGER is not absolute: %q", got)
	}
}

func TestWorktreeFromLocalRepo(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	os.MkdirAll(repo, 0o755)
	git("init", "-q")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("v1\n"), 0o644)
	git("add", ".")
	git("commit", "-qm", "v1")
	ref := git("rev-parse", "HEAD")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("dirty working copy\n"), 0o644)

	dir, err := Worktree(context.Background(), filepath.Join(tmp, "cache"), repo, ref)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(got) != "v1\n" {
		t.Fatalf("worktree should hold the committed ref, got %q", got)
	}
	again, err := Worktree(context.Background(), filepath.Join(tmp, "cache"), repo, ref)
	if err != nil || again != dir {
		t.Fatalf("second call should reuse: %v %v", again, err)
	}
}

func TestSettingSourcesDefaultAndEmpty(t *testing.T) {
	empty := ""
	if (Scenario{}).settingSources() != "project,local" || (Scenario{SettingSources: &empty}).settingSources() != "" {
		t.Fatal("setting sources")
	}
}

func TestEditScenarioResetsAndChecks(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	run := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	os.MkdirAll(repo, 0o755)
	run(repo, "init", "-q")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("v1\n"), 0o644)
	run(repo, "add", ".")
	run(repo, "commit", "-qm", "v1")
	out, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	ref := strings.TrimSpace(string(out))

	// Scenario dir with a hidden file the check copies in.
	scen := filepath.Join(tmp, "scen")
	os.MkdirAll(scen, 0o755)
	os.WriteFile(filepath.Join(scen, "hidden.txt"), []byte("secret"), 0o644)
	yaml := "name: e\npath: " + repo + "\nref: " + ref + "\nprompt: p\n" +
		"check: cp \"$DETOUR_SCENARIO_DIR/hidden.txt\" . && grep -q v2 a.txt\n"
	os.WriteFile(filepath.Join(scen, "e.yaml"), []byte(yaml), 0o644)
	ss, err := LoadScenarios(scen)
	if err != nil {
		t.Fatal(err)
	}

	// Fake agent: edits a.txt to v2 and records its allowed tools.
	bin := filepath.Join(tmp, "claude")
	script := "#!/bin/sh\necho \"$@\" > \"" + tmp + "/args\"\necho v2 > a.txt\necho '{\"type\":\"result\",\"result\":\"done\"}'\n"
	os.WriteFile(bin, []byte(script), 0o755)

	rs, err := Run(context.Background(), Options{
		Scenarios: ss, Variants: []string{Baseline}, Reps: 2,
		OutDir: filepath.Join(tmp, "out"), CacheDir: filepath.Join(tmp, "cache"),
		PluginDir: tmp, BinDir: tmp, ClaudeBin: bin,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if r.Error != "" || r.CheckPassed == nil || !*r.CheckPassed || !r.Passed || !strings.Contains(r.DiffStat, "1 file changed") {
			t.Fatalf("result %+v", r)
		}
	}
	args, _ := os.ReadFile(filepath.Join(tmp, "args"))
	if !strings.Contains(string(args), "Edit,Write") {
		t.Fatalf("edit tools not allowed: %s", args)
	}
	// After the run the checkout is back at v1 with no hidden file.
	wt, _ := Worktree(context.Background(), filepath.Join(tmp, "cache"), repo, ref)
	if b, _ := os.ReadFile(filepath.Join(wt, "a.txt")); string(b) != "v1\n" {
		t.Fatalf("checkout not reset: %q", b)
	}
	if _, err := os.Stat(filepath.Join(wt, "hidden.txt")); err == nil {
		t.Fatal("hidden file left behind")
	}
}

func TestCheckRequiresLocalPath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.yaml"), []byte("name: x\nrepo: https://example.com/r\nref: v1\nprompt: p\ncheck: true\n"), 0o644)
	if _, err := LoadScenarios(dir); err == nil {
		t.Fatal("expected error")
	}
}

func TestClaudeArgsBlockVariantLoadsGivenPlugin(t *testing.T) {
	args := strings.Join(ClaudeArgs("q", "opus", Block, "/pb", ""), " ")
	if !strings.Contains(args, "--plugin-dir /pb") {
		t.Fatalf("block variant must load its plugin: %s", args)
	}
}

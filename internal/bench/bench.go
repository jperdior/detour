// Package bench runs A/B sessions of a real agent (Claude Code in print mode)
// with and without detour on fixed scenarios, and reports net cost.
package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jperdior/detour/internal/ledger"
)

// Scenario is one benchmark task.
type Scenario struct {
	Name string `yaml:"name"`
	// Where the code comes from: a git URL (cloned once into the cache), or
	// a local git repository (checked out as a detached worktree, so your
	// working copy is never touched). Ref pins the commit either way.
	Repo   string   `yaml:"repo"`
	Path   string   `yaml:"path"`
	Ref    string   `yaml:"ref"`
	Prompt string   `yaml:"prompt"`
	Expect []string `yaml:"expect"` // case-insensitive regexes the answer must match
	Notes  string   `yaml:"notes"`
	// SettingSources is passed to --setting-sources. Default "project,local";
	// "" also skips the project's own settings (plugins, hooks), which keeps
	// both variants clean when the project enables plugins of its own.
	SettingSources *string `yaml:"setting_sources"`
}

func (s Scenario) settingSources() string {
	if s.SettingSources == nil {
		return "project,local"
	}
	return *s.SettingSources
}

// LoadScenarios reads every *.yaml in dir, sorted by file name.
func LoadScenarios(dir string) ([]Scenario, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []Scenario
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var s Scenario
		if err := yaml.Unmarshal(b, &s); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if s.Name == "" {
			s.Name = strings.TrimSuffix(filepath.Base(p), ".yaml")
		}
		if (s.Repo == "") == (s.Path == "") {
			return nil, fmt.Errorf("%s: set exactly one of repo or path", p)
		}
		if strings.HasPrefix(s.Path, "~/") {
			home, _ := os.UserHomeDir()
			s.Path = filepath.Join(home, s.Path[2:])
		}
		for _, e := range s.Expect {
			if _, err := regexp.Compile("(?i)" + e); err != nil {
				return nil, fmt.Errorf("%s: bad expect %q: %w", p, e, err)
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// Variants.
const (
	Baseline = "baseline" // plain Claude Code
	Detour   = "detour"   // Claude Code + detour plugin (hook + skill)
)

// Options for a run.
type Options struct {
	Scenarios []Scenario
	Variants  []string
	Reps      int
	Model     string // Claude model alias, e.g. "sonnet"
	OutDir    string
	CacheDir  string // where fixture repos are cloned
	PluginDir string // detour's Claude Code plugin
	BinDir    string // directory holding the detour binary, put first on PATH
	ClaudeBin string
	Log       func(format string, args ...any)
}

// Result is one session.
type Result struct {
	Scenario string    `json:"scenario"`
	Variant  string    `json:"variant"`
	Rep      int       `json:"rep"`
	RunID    string    `json:"run_id"`
	Model    string    `json:"model"`
	Started  time.Time `json:"started"`
	Error    string    `json:"error,omitempty"`

	// From Claude Code's own JSON result: what the expensive model billed.
	ClaudeCostUSD       float64 `json:"claude_cost_usd"`
	InputTokens         int     `json:"input_tokens"`
	CacheCreationTokens int     `json:"cache_creation_tokens"`
	CacheReadTokens     int     `json:"cache_read_tokens"`
	OutputTokens        int     `json:"output_tokens"`
	Turns               int     `json:"turns"`
	DurationMS          int64   `json:"duration_ms"`
	PermissionDenials   int     `json:"permission_denials"`

	// What the agent did: count per tool, and a one-line summary per call.
	Tools     map[string]int `json:"tools,omitempty"`
	ToolCalls []string       `json:"tool_calls,omitempty"`

	// Quality.
	Passed  bool     `json:"passed"`
	Missing []string `json:"missing,omitempty"`
	Answer  string   `json:"answer"`

	// From detour's ledger for this run.
	Blocks              int     `json:"blocks"`
	Partials            int     `json:"partials"`
	Delegations         int     `json:"delegations"`
	DelegateErrors      int     `json:"delegate_errors"`
	DelegateCostUSD     float64 `json:"delegate_cost_usd"`
	DelegatePlanCostUSD float64 `json:"delegate_plan_cost_usd"` // part of delegate cost on the Claude plan (haiku via claude-cli)
	DelegateTokens      int     `json:"delegate_tokens"`

	NetCostUSD float64 `json:"net_cost_usd"`
}

// PlanCostUSD is what the session costs against the user's Claude usage: the
// main agent plus any delegation that ran through Claude Code.
func (r Result) PlanCostUSD() float64 { return r.ClaudeCostUSD + r.DelegatePlanCostUSD }

// ContextTokens is everything the expensive model read, cached or not.
func (r Result) ContextTokens() int {
	return r.InputTokens + r.CacheCreationTokens + r.CacheReadTokens
}

// claudeResult is the subset of `claude -p --output-format json` we read.
type claudeResult struct {
	IsError      bool    `json:"is_error"`
	Subtype      string  `json:"subtype"`
	Result       string  `json:"result"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	NumTurns     int     `json:"num_turns"`
	DurationMS   int64   `json:"duration_ms"`
	Usage        struct {
		InputTokens              int `json:"input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		OutputTokens             int `json:"output_tokens"`
	} `json:"usage"`
	PermissionDenials []json.RawMessage `json:"permission_denials"`
}

// ParseClaude fills r from Claude Code's output: either stream-json (one
// event per line, which also gives the tool calls) or a single JSON result.
func ParseClaude(raw []byte, r *Result) error {
	var c *claudeResult
	r.Tools = map[string]int{}
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type  string          `json:"type"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "result":
			var cr claudeResult
			if json.Unmarshal(line, &cr) == nil {
				c = &cr
			}
		case "assistant":
			for _, part := range ev.Message.Content {
				if part.Type == "tool_use" {
					r.Tools[part.Name]++
					r.ToolCalls = append(r.ToolCalls, describeTool(part.Name, part.Input))
				}
			}
		}
	}
	if c == nil { // plain --output-format json
		var cr claudeResult
		if err := json.Unmarshal(lastJSONObject(raw), &cr); err != nil {
			return fmt.Errorf("parse claude output: %w", err)
		}
		c = &cr
	}
	r.ClaudeCostUSD = c.TotalCostUSD
	r.InputTokens = c.Usage.InputTokens
	r.CacheCreationTokens = c.Usage.CacheCreationInputTokens
	r.CacheReadTokens = c.Usage.CacheReadInputTokens
	r.OutputTokens = c.Usage.OutputTokens
	r.Turns = c.NumTurns
	r.DurationMS = c.DurationMS
	r.PermissionDenials = len(c.PermissionDenials)
	r.Answer = c.Result
	if c.IsError {
		r.Error = "claude: " + c.Subtype
	}
	return nil
}

// describeTool gives a one-line summary of a tool call for the results file.
func describeTool(name string, input json.RawMessage) string {
	var in struct {
		FilePath string `json:"file_path"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
		Pattern  string `json:"pattern"`
		Path     string `json:"path"`
		Command  string `json:"command"`
	}
	_ = json.Unmarshal(input, &in)
	switch {
	case in.FilePath != "" && (in.Offset > 0 || in.Limit > 0):
		return fmt.Sprintf("%s %s [%d+%d]", name, filepath.Base(in.FilePath), in.Offset, in.Limit)
	case in.FilePath != "":
		return name + " " + filepath.Base(in.FilePath)
	case in.Command != "":
		return name + ": " + truncate(in.Command, 120)
	case in.Pattern != "":
		return fmt.Sprintf("%s %q %s", name, truncate(in.Pattern, 60), filepath.Base(in.Path))
	}
	return name
}

// lastJSONObject tolerates stray lines before the final JSON result.
func lastJSONObject(raw []byte) []byte {
	raw = bytes.TrimSpace(raw)
	if i := bytes.LastIndex(raw, []byte("\n{")); i >= 0 {
		return raw[i+1:]
	}
	return raw
}

// Score checks the answer against the scenario's expectations.
func Score(s Scenario, answer string) (bool, []string) {
	var missing []string
	for _, e := range s.Expect {
		if !regexp.MustCompile("(?i)" + e).MatchString(answer) {
			missing = append(missing, e)
		}
	}
	return len(missing) == 0, missing
}

// allowedTools is the same for both variants, so the only difference is the
// detour plugin. No write tools: scenarios are read-only questions.
var allowedTools = []string{
	"Read", "Grep", "Glob",
	"Bash(detour read:*)",
	"Bash(cat:*)", "Bash(head:*)", "Bash(tail:*)", "Bash(sed:*)", "Bash(wc:*)",
	"Bash(ls:*)", "Bash(grep:*)", "Bash(rg:*)", "Bash(find:*)",
}

// ClaudeArgs builds the command line for one session.
func ClaudeArgs(prompt, model, variant, pluginDir, settingSources string) []string {
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json", "--verbose",
		"--no-session-persistence",
		// Skip the user's own settings, plugins and hooks so both variants
		// run in the same clean environment; only project/local apply.
		"--setting-sources", settingSources,
		"--strict-mcp-config",
		"--allowedTools", strings.Join(allowedTools, ","),
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if variant == Detour {
		args = append(args, "--plugin-dir", pluginDir)
	}
	return args
}

// Run executes all sessions, appending results to OutDir/results.jsonl.
// Reps are interleaved (rep 1 of every scenario and variant, then rep 2...)
// so drift over time affects both variants alike.
func Run(ctx context.Context, o Options) ([]Result, error) {
	// Sessions run inside the fixture checkout, so every path handed to them
	// (the ledger above all) must be absolute.
	for _, p := range []*string{&o.OutDir, &o.PluginDir, &o.BinDir, &o.CacheDir} {
		abs, err := filepath.Abs(*p)
		if err != nil {
			return nil, err
		}
		*p = abs
	}
	if err := os.MkdirAll(filepath.Join(o.OutDir, "raw"), 0o755); err != nil {
		return nil, err
	}
	ledgerPath := filepath.Join(o.OutDir, "ledger.jsonl")
	resultsPath := filepath.Join(o.OutDir, "results.jsonl")
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}

	var results []Result
	for rep := 1; rep <= o.Reps; rep++ {
		for _, s := range o.Scenarios {
			dir, err := checkoutScenario(ctx, o.CacheDir, s)
			if err != nil {
				return results, err
			}
			for _, v := range o.Variants {
				r := runOne(ctx, o, s, v, rep, dir, ledgerPath)
				o.Log("%-28s %-8s rep %d  claude $%.4f  delegate $%.4f  blocks %d  pass %v %s",
					s.Name, v, rep, r.ClaudeCostUSD, r.DelegateCostUSD, r.Blocks, r.Passed, r.Error)
				if err := appendJSONL(resultsPath, r); err != nil {
					return results, err
				}
				results = append(results, r)
			}
		}
	}
	return results, nil
}

func runOne(ctx context.Context, o Options, s Scenario, variant string, rep int, dir, ledgerPath string) Result {
	r := Result{
		Scenario: s.Name, Variant: variant, Rep: rep, Model: o.Model, Started: time.Now().UTC(),
		RunID: fmt.Sprintf("%s-%s-%d-%d", s.Name, variant, rep, time.Now().UnixNano()),
	}
	cmd := exec.CommandContext(ctx, o.ClaudeBin, ClaudeArgs(s.Prompt, o.Model, variant, o.PluginDir, s.settingSources())...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"PATH="+o.BinDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DETOUR_RUN_ID="+r.RunID,
		"DETOUR_LEDGER="+ledgerPath,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	_ = os.WriteFile(filepath.Join(o.OutDir, "raw", r.RunID+".jsonl"), stdout.Bytes(), 0o644)

	if err := ParseClaude(stdout.Bytes(), &r); err != nil {
		r.Error = fmt.Sprintf("%v; exit: %v; stderr: %s", err, runErr, truncate(stderr.String(), 400))
		return r
	}
	r.Passed, r.Missing = Score(s, r.Answer)

	events, _ := ledger.Read(ledgerPath)
	sum := ledger.Summarize(events, func(e ledger.Event) bool { return e.RunID == r.RunID })
	r.Blocks, r.Partials = sum.Blocks, sum.Partials
	r.Delegations, r.DelegateErrors = sum.Delegations, sum.DelegateErrors
	r.DelegateCostUSD = sum.CostUSD
	r.DelegatePlanCostUSD = sum.PlanCostUSD
	r.DelegateTokens = sum.PromptTokens + sum.CompletionTokens
	r.NetCostUSD = r.ClaudeCostUSD + r.DelegateCostUSD
	return r
}

func checkoutScenario(ctx context.Context, cacheDir string, s Scenario) (string, error) {
	if s.Path != "" {
		return Worktree(ctx, cacheDir, s.Path, s.Ref)
	}
	return Checkout(ctx, cacheDir, s.Repo, s.Ref)
}

// Worktree checks out ref of the local repository at repoPath as a detached
// worktree under cacheDir, once, and returns its path. Remove it with
// `git -C <repo> worktree remove <path>` (or prune after deleting the cache).
func Worktree(ctx context.Context, cacheDir, repoPath, ref string) (string, error) {
	name := regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(filepath.Base(repoPath)+"@"+ref, "_")
	dir := filepath.Join(cacheDir, "worktrees", name)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "add", "--detach", dir, ref)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("worktree %s@%s: %v: %s", repoPath, ref, err, out)
	}
	return dir, nil
}

// Checkout clones repo at ref into cacheDir once and returns its path.
func Checkout(ctx context.Context, cacheDir, repo, ref string) (string, error) {
	name := regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(strings.TrimPrefix(strings.TrimPrefix(repo, "https://"), "github.com/"), "_")
	dir := filepath.Join(cacheDir, name+"@"+ref)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", "clone", "--quiet", "--depth", "1", "--branch", ref, repo, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("clone %s@%s: %v: %s", repo, ref, err, out)
	}
	return dir, nil
}

// ReadResults loads a results.jsonl file.
func ReadResults(path string) ([]Result, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Result
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var r Result
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func appendJSONL(path string, v any) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Command detour keeps coding agents from reading large files with an
// expensive model: a hook blocks the read, and `detour read` answers the
// question with a cheap one.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jperdior/detour/internal/bench"
	"github.com/jperdior/detour/internal/config"
	"github.com/jperdior/detour/internal/delegate"
	"github.com/jperdior/detour/internal/hook"
	"github.com/jperdior/detour/internal/ledger"
	"github.com/jperdior/detour/internal/policy"
	"github.com/jperdior/detour/internal/trial"
)

var version = "0.1.0-dev"

const usage = `detour: delegate bulk file reading from coding agents to cheap models.

Usage:
  detour read -q QUESTION FILE...      answer a question about files with a cheap model
  detour hook claude [--block-only]    Claude Code PreToolUse hook (reads JSON on stdin);
                                       --block-only blocks without offering delegation
  detour bench run [flags]             A/B benchmark: Claude Code with and without detour
  detour bench report RESULTS.jsonl    print a markdown report
  detour hook claude --trial           the same hook, applying the session's field-trial arm
  detour hook claude-session-start     SessionStart hook: assign a trial arm
  detour hook claude-session-end       SessionEnd hook: measure the session
  detour trial status|report           field-trial progress and results
  detour stats                         summarise the ledger
  detour config                        print the default config
  detour version

Environment:
  DEEPSEEK_API_KEY     key for the default bulk-reader model
  DETOUR_MODEL=haiku   delegate to Haiku through your Claude Code login instead
  DETOUR_CONFIG        config file (default ~/.config/detour/config.yaml)
  DETOUR_MODEL         override the mode's model: haiku, deepseek-v4-pro, qwen2.5-coder...
  DETOUR_MIN_LINES     line threshold for the hook (default 350)
  DETOUR_LEDGER        ledger file (default ~/.local/state/detour/ledger.jsonl)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "read":
		err = cmdRead(os.Args[2:])
	case "hook":
		err = cmdHook(os.Args[2:])
	case "bench":
		err = cmdBench(os.Args[2:])
	case "stats":
		err = cmdStats()
	case "trial":
		err = cmdTrial(os.Args[2:])
	case "config":
		_, err = os.Stdout.Write(config.Default())
	case "version", "--version":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "detour:", err)
		os.Exit(1)
	}
}

func cmdRead(args []string) error {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	q := fs.String("q", "", "question about the files (required)")
	mode := fs.String("mode", "bulk-reader", "mode to use")
	model := fs.String("model", os.Getenv("DETOUR_MODEL"), "override the mode's model")
	fs.Parse(args)
	if *q == "" || fs.NArg() == 0 {
		return fmt.Errorf(`usage: detour read -q "question" FILE...`)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// In a field-trial session the delegate is fixed by the trial config, and
	// it must run through Claude Code: trial projects may hold code that must
	// not reach third-party providers.
	session := os.Getenv("CLAUDE_CODE_SESSION_ID")
	inTrial := trial.Lookup(trial.Dir(), session).InTrial()
	if inTrial {
		*model = loadTrial().DelegateModel
		if *model == "" {
			*model = "haiku"
		}
	}
	r, err := cfg.Resolve(*mode, *model)
	if err != nil {
		return err
	}
	if inTrial && r.Provider.Type != config.TypeClaudeCLI {
		return fmt.Errorf("trial sessions may only delegate through Claude Code; %s uses provider type %q", r.ModelKey, r.Provider.Type)
	}
	files, err := delegate.LoadFiles(fs.Args())
	if err != nil {
		return err
	}
	res, err := delegate.Ask(context.Background(), r, *q, files)
	ev := ledger.Event{Kind: ledger.KindDelegate, SessionID: session, Agent: os.Getenv("DETOUR_AGENT"), Mode: *mode, Model: r.ModelKey, Paths: fs.Args()}
	if err != nil {
		ev.Error = err.Error()
		_ = ledger.Append(ledger.Path(), ev)
		return fmt.Errorf("%w\nThe delegate model failed. Fall back to targeted reads (Read with offset/limit) or Grep.", err)
	}
	ev.PromptTokens, ev.CachedTokens, ev.CompletionTokens = res.Usage.PromptTokens, res.Usage.CachedTokens, res.Usage.CompletionTokens
	ev.CostUSD, ev.LatencyMS, ev.AnswerBytes, ev.Bytes = res.CostUSD, res.Latency.Milliseconds(), len(res.Answer), int64(res.InputLen)
	ev.ClaudePlan = res.ClaudePlan
	_ = ledger.Append(ledger.Path(), ev)

	fmt.Println(res.Answer)
	fmt.Fprintf(os.Stderr, "\n[detour %s: %d files, %d→%d tokens, $%.5f, %.1fs]\n",
		r.ModelKey, res.FileCount, res.Usage.PromptTokens, res.Usage.CompletionTokens, res.CostUSD, res.Latency.Seconds())
	return nil
}

func cmdHook(args []string) error {
	const u = "usage: detour hook claude [--block-only|--trial] | claude-session-start | claude-session-end"
	if len(args) == 0 || len(args) > 2 {
		return fmt.Errorf(u)
	}
	flag := ""
	if len(args) == 2 {
		flag = args[1]
	}
	switch {
	case args[0] == "claude" && (flag == "" || flag == "--block-only"):
		cfg := policy.FromEnv()
		cfg.BlockOnly = flag == "--block-only"
		return hook.Claude(os.Stdin, os.Stdout, cfg, ledger.Path())
	case args[0] == "claude" && flag == "--trial":
		return trial.PreToolUse(os.Stdin, os.Stdout, loadTrial(), trial.Dir(), ledger.Path(), policy.FromEnv())
	case args[0] == "claude-session-start" && flag == "":
		return trial.SessionStart(os.Stdin, os.Stdout, loadTrial(), trial.Dir(), trial.NotifyMac)
	case args[0] == "claude-session-end" && flag == "":
		return trial.SessionEnd(os.Stdin, loadTrial(), trial.Dir(), trial.NotifyMac)
	}
	return fmt.Errorf(u)
}

// loadTrial returns the trial config; a broken file disables the trial
// rather than breaking sessions.
func loadTrial() trial.Config {
	c, err := trial.LoadConfig(trial.ConfigPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "detour:", err)
		return trial.Config{}
	}
	return c
}

const trialUsage = `usage:
  detour trial setup [--config-dir DIR]... REPO...   join the trial with these repositories
  detour trial status                                 progress
  detour trial report                                 your results
  detour trial share [--dry-run] [--yes]              send anonymized results as a GitHub issue
  detour trial uninstall [--config-dir DIR]...        remove the hooks and stop the trial
  detour trial aggregate                              (maintainer) combine every shared report`

// shareRepo receives shared reports.
const shareRepo = "jperdior/detour"

func cmdTrial(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf(trialUsage)
	}
	switch args[0] {
	case "setup":
		return cmdTrialSetup(args[1:])
	case "uninstall":
		return cmdTrialUninstall(args[1:])
	case "share":
		return cmdTrialShare(args[1:])
	case "aggregate":
		return cmdTrialAggregate(args[1:])
	}
	if len(args) != 1 || (args[0] != "status" && args[0] != "report") {
		return fmt.Errorf(trialUsage)
	}
	cfg, err := trial.LoadConfig(trial.ConfigPath())
	if err != nil {
		return err
	}
	dir := trial.Dir()
	trial.Sweep(dir, time.Now())
	if args[0] == "status" {
		ss, err := trial.All(dir)
		if err != nil {
			return err
		}
		arms, open, stale := map[string]int{}, 0, 0
		for _, s := range ss {
			if s.Ended.IsZero() {
				open++
			}
			if s.EndReason == "stale" {
				stale++
			}
			arms[s.Arm]++
		}
		fmt.Printf("enabled %v, %d/%d measured, %d sessions assigned (%d still open, %d ended without SessionEnd)\n", cfg.Enabled, trial.Measured(dir), cfg.Target, len(ss), open, stale)
		for _, a := range cfg.Arms {
			fmt.Printf("  %s: %d\n", a, arms[a])
		}
		return nil
	}
	events, err := ledger.Read(ledger.Path())
	if err != nil {
		return err
	}
	rows, err := trial.Rows(dir, events)
	if err != nil {
		return err
	}
	trial.WriteReport(os.Stdout, cfg, rows)
	return nil
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// configDirs returns the Claude Code config dirs to act on: the flags, or
// the current one ($CLAUDE_CONFIG_DIR, else ~/.claude).
func configDirs(flags multiFlag) []string {
	if len(flags) > 0 {
		var out []string
		for _, d := range flags {
			if strings.HasPrefix(d, "~/") {
				home, _ := os.UserHomeDir()
				d = filepath.Join(home, d[2:])
			}
			out = append(out, d)
		}
		return out
	}
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return []string{d}
	}
	home, _ := os.UserHomeDir()
	return []string{filepath.Join(home, ".claude")}
}

func cmdTrialSetup(args []string) error {
	fs := flag.NewFlagSet("trial setup", flag.ExitOnError)
	var dirs multiFlag
	fs.Var(&dirs, "config-dir", "Claude Code config dir to install the hooks in (repeatable; default $CLAUDE_CONFIG_DIR or ~/.claude)")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("name at least one git repository: detour trial setup ~/code/myapp")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		return fmt.Errorf("claude is not on PATH: the trial delegates through Claude Code")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	cfg, err := trial.AddProjects(trial.ConfigPath(), fs.Args())
	if err != nil {
		return err
	}
	for _, d := range configDirs(dirs) {
		path, err := trial.Install(d, exe)
		if err != nil {
			return err
		}
		fmt.Printf("hooks installed in %s (backup: settings.json.before-detour)\n", path)
		if !slices.Contains(cfg.ConfigDirs, d) {
			cfg.ConfigDirs = append(cfg.ConfigDirs, d)
		}
	}
	if err := trial.SaveConfig(trial.ConfigPath(), cfg); err != nil {
		return err
	}
	fmt.Printf("trial config: %s\nprojects:\n", trial.ConfigPath())
	for _, p := range cfg.Projects {
		fmt.Printf("  %-24s %s\n", p.Name, p.Repo)
	}
	fmt.Printf(`
Done. New Claude Code sessions in these projects (worktrees included) are now
randomly assigned detour on or off. Work as usual. After %d sessions you get a
notification; then run: detour trial share
Progress: detour trial status    Pause: set enabled: false in the config    Remove: detour trial uninstall
`, cfg.Target)
	return nil
}

func cmdTrialUninstall(args []string) error {
	fs := flag.NewFlagSet("trial uninstall", flag.ExitOnError)
	var dirs multiFlag
	fs.Var(&dirs, "config-dir", "Claude Code config dir to remove the hooks from (repeatable; default: every dir setup installed in)")
	fs.Parse(args)
	targets := configDirs(dirs)
	if cfg, err := trial.LoadConfig(trial.ConfigPath()); err == nil && len(dirs) == 0 && len(cfg.ConfigDirs) > 0 {
		targets = cfg.ConfigDirs
	}
	for _, d := range targets {
		path, err := trial.Uninstall(d)
		if err != nil {
			return err
		}
		fmt.Printf("hooks removed from %s\n", path)
	}
	if cfg, err := trial.LoadConfig(trial.ConfigPath()); err == nil && cfg.Enabled {
		cfg.Enabled = false
		if err := trial.SaveConfig(trial.ConfigPath(), cfg); err != nil {
			return err
		}
	}
	fmt.Printf("trial disabled. Your measurements stay in %s; detour trial share still works.\n", trial.Dir())
	return nil
}

func cmdTrialShare(args []string) error {
	fs := flag.NewFlagSet("trial share", flag.ExitOnError)
	dry := fs.Bool("dry-run", false, "print the report that would be sent, send nothing")
	yes := fs.Bool("yes", false, "send without asking (after reviewing it with --dry-run)")
	fs.Parse(args)
	cfg, err := trial.LoadConfig(trial.ConfigPath())
	if err != nil {
		return err
	}
	dir := trial.Dir()
	trial.Sweep(dir, time.Now())
	events, err := ledger.Read(ledger.Path())
	if err != nil {
		return err
	}
	rows, err := trial.Rows(dir, events)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("no measured sessions yet")
	}
	sh := trial.NewShared(cfg, rows, trial.ContributorID(trial.ConfigPath()), version)
	body, err := sh.Body()
	if err != nil {
		return err
	}
	file := filepath.Join(dir, "share-"+time.Now().UTC().Format("20060102-150405")+".md")
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		return err
	}
	if *dry {
		fmt.Printf("%s\n%s\n(saved to %s; nothing was sent)\n", sh.Title(), body, file)
		return nil
	}
	if !*yes {
		fmt.Printf("%s\nReview it first: %s\nIt contains no code, paths, prompts or project names.\n", sh.Title(), file)
		if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			return fmt.Errorf("not sent: review the file, then run detour trial share --yes")
		}
		fmt.Printf("Publish it as a public issue in github.com/%s? [y/N] ", shareRepo)
		var answer string
		fmt.Scanln(&answer)
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return fmt.Errorf("not sent")
		}
	}
	if _, err := exec.LookPath("gh"); err == nil && exec.Command("gh", "auth", "status").Run() == nil {
		base := []string{"issue", "create", "--repo", shareRepo, "--title", sh.Title(), "--body-file", file}
		out, err := exec.Command("gh", append(base, "--label", "trial-report")...).CombinedOutput()
		if err != nil { // testers cannot always set labels on someone else's repo
			out, err = exec.Command("gh", base...).CombinedOutput()
		}
		if err != nil {
			return fmt.Errorf("gh issue create: %v: %s", err, out)
		}
		trial.MarkShared(dir)
		fmt.Printf("Sent, thank you: %s", out)
		if trial.Complete(cfg, dir) {
			fmt.Println("The trial is over. To remove detour: detour trial uninstall")
		}
		return nil
	}
	u := trial.NewIssueURL(shareRepo, sh.Title(), body)
	if len(u) > 7000 {
		u = trial.NewIssueURL(shareRepo, sh.Title(), "Paste the contents of "+file+" here.")
		fmt.Printf("The report is too long for a link. Paste the contents of %s into the issue that opens.\n", file)
	}
	fmt.Printf("gh is not set up, so open this link and press Submit:\n%s\n", u)
	openBrowser(u)
	trial.MarkShared(dir)
	return nil
}

func openBrowser(u string) {
	for _, c := range [][]string{{"open", u}, {"xdg-open", u}} {
		if _, err := exec.LookPath(c[0]); err == nil {
			_ = exec.Command(c[0], c[1:]...).Start()
			return
		}
	}
}

func cmdTrialAggregate(args []string) error {
	out, err := exec.Command("gh", "issue", "list", "--repo", shareRepo, "--state", "all", "--limit", "500",
		"--search", "trial-report in:title", "--json", "number,author,body,createdAt").Output()
	if err != nil {
		return fmt.Errorf("gh issue list: %w", err)
	}
	var issues []struct {
		Number int `json:"number"`
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"createdAt"`
	}
	if err := json.Unmarshal(out, &issues); err != nil {
		return err
	}
	// Each report holds all of a tester's sessions so far: keep the newest
	// per contributor.
	latest := map[string]trial.Shared{}
	who := map[string]string{}
	for _, is := range issues {
		sh, err := trial.ParseBody(is.Body)
		if err != nil {
			continue
		}
		key := sh.Contributor
		if prev, ok := latest[key]; ok && !sh.Generated.After(prev.Generated) {
			continue
		}
		latest[key], who[key] = sh, is.Author.Login
	}
	if len(latest) == 0 {
		return fmt.Errorf("no trial reports in github.com/%s yet", shareRepo)
	}
	var rows []trial.Row
	var arms []string
	fmt.Printf("# detour field trial, all testers\n\n| tester | sessions | detour | OS | sent |\n|---|---|---|---|---|\n")
	for key, sh := range latest {
		name := who[key] + " (" + key[:6] + ")"
		fmt.Printf("| %s | %d | %s | %s | %s |\n", name, len(sh.Sessions), sh.Version, sh.OS, sh.Generated.Format("2006-01-02"))
		for _, r := range sh.Sessions {
			r.Contributor = name
			r.Project = name + "/" + r.Project
			r.Profile = name + "/" + r.Profile
			rows = append(rows, r)
		}
		if arms == nil {
			arms = sh.Arms
		}
	}
	fmt.Printf("\n%d sessions in total.\n\n", len(rows))
	trial.WriteSummary(os.Stdout, arms, rows)
	return nil
}

func cmdStats() error {
	events, err := ledger.Read(ledger.Path())
	if err != nil {
		return err
	}
	s := ledger.Summarize(events, nil)
	fmt.Printf("ledger: %s (%d events)\n", ledger.Path(), len(events))
	fmt.Printf("blocked reads:     %d (~%d tokens kept out of the agent's context)\n", s.Blocks, s.BlockedEstTokens)
	fmt.Printf("targeted reads:    %d\n", s.Partials)
	fmt.Printf("delegations:       %d ok, %d failed\n", s.Delegations, s.DelegateErrors)
	fmt.Printf("delegate tokens:   %d in (%d cached), %d out\n", s.PromptTokens, s.CachedTokens, s.CompletionTokens)
	fmt.Printf("delegate cost:     $%.4f ($%.4f of it on your Claude plan)\n", s.CostUSD, s.PlanCostUSD)
	fmt.Printf("answers returned:  ~%d tokens back into the agent\n", s.AnswerBytes/4)
	fmt.Println("\nThese are estimates. For real net savings run `detour bench run`.")
	return nil
}

func cmdBench(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: detour bench run|report")
	}
	switch args[0] {
	case "report":
		if len(args) != 2 {
			return fmt.Errorf("usage: detour bench report RESULTS.jsonl")
		}
		rs, err := bench.ReadResults(args[1])
		if err != nil {
			return err
		}
		bench.WriteMarkdown(os.Stdout, rs)
		return nil
	case "run":
	default:
		return fmt.Errorf("usage: detour bench run|report")
	}

	fs := flag.NewFlagSet("bench run", flag.ExitOnError)
	repoRoot := fs.String("root", ".", "detour repository root (holds bench/ and plugin/)")
	scenarios := fs.String("scenarios", "", "scenario directory (default ROOT/bench/scenarios)")
	only := fs.String("only", "", "comma-separated scenario names to run")
	variants := fs.String("variants", "baseline,detour", "variants to run")
	reps := fs.Int("reps", 3, "repetitions per scenario and variant")
	model := fs.String("model", "sonnet", "Claude model for the agent")
	out := fs.String("out", "", "output directory (default ROOT/bench/results/<timestamp>)")
	claudeBin := fs.String("claude", "claude", "Claude Code binary")
	fs.Parse(args[1:])

	root, err := filepath.Abs(*repoRoot)
	if err != nil {
		return err
	}
	if *scenarios == "" {
		*scenarios = filepath.Join(root, "bench", "scenarios")
	}
	if *out == "" {
		*out = filepath.Join(root, "bench", "results", time.Now().Format("20060102-150405"))
	}
	if *out, err = filepath.Abs(*out); err != nil {
		return err
	}
	ss, err := bench.LoadScenarios(*scenarios)
	if err != nil {
		return err
	}
	if *only != "" {
		want := map[string]bool{}
		for _, n := range strings.Split(*only, ",") {
			want[strings.TrimSpace(n)] = true
		}
		var kept []bench.Scenario
		for _, s := range ss {
			if want[s.Name] {
				kept = append(kept, s)
			}
		}
		ss = kept
	}
	if len(ss) == 0 {
		return fmt.Errorf("no scenarios to run")
	}
	vs := strings.Split(*variants, ",")
	for _, v := range vs {
		if v != bench.Baseline && v != bench.Detour && v != bench.Block {
			return fmt.Errorf("unknown variant %q", v)
		}
		if v == bench.Detour && os.Getenv("DEEPSEEK_API_KEY") == "" && os.Getenv("DETOUR_MODEL") == "" && os.Getenv("DETOUR_CONFIG") == "" {
			fmt.Fprintln(os.Stderr, "warning: DEEPSEEK_API_KEY is not set; delegations in the detour variant will fail")
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if _, err := exec.LookPath(*claudeBin); err != nil {
		return fmt.Errorf("claude binary not found: %w", err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "running %d scenarios × %d variants × %d reps with model %s → %s\n", len(ss), len(vs), *reps, *model, *out)
	rs, err := bench.Run(context.Background(), bench.Options{
		Scenarios:      ss,
		Variants:       vs,
		Reps:           *reps,
		Model:          *model,
		OutDir:         *out,
		CacheDir:       filepath.Join(cache, "detour", "bench"),
		PluginDir:      filepath.Join(root, "plugin"),
		BlockPluginDir: filepath.Join(root, "plugin-block"),
		BinDir:         filepath.Dir(exe),
		ClaudeBin:      *claudeBin,
		Log:            func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) },
	})
	if len(rs) > 0 {
		f, ferr := os.Create(filepath.Join(*out, "report.md"))
		if ferr == nil {
			bench.WriteMarkdown(f, rs)
			f.Close()
		}
		bench.WriteMarkdown(os.Stdout, rs)
	}
	return err
}

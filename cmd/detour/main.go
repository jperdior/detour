// Command detour keeps coding agents from reading large files with an
// expensive model: a hook blocks the read, and `detour read` answers the
// question with a cheap one.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jperdior/detour/internal/bench"
	"github.com/jperdior/detour/internal/config"
	"github.com/jperdior/detour/internal/delegate"
	"github.com/jperdior/detour/internal/hook"
	"github.com/jperdior/detour/internal/ledger"
	"github.com/jperdior/detour/internal/policy"
)

var version = "0.1.0-dev"

const usage = `detour: delegate bulk file reading from coding agents to cheap models.

Usage:
  detour read -q QUESTION FILE...      answer a question about files with a cheap model
  detour hook claude [--block-only]    Claude Code PreToolUse hook (reads JSON on stdin);
                                       --block-only blocks without offering delegation
  detour bench run [flags]             A/B benchmark: Claude Code with and without detour
  detour bench report RESULTS.jsonl    print a markdown report
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
	r, err := cfg.Resolve(*mode, *model)
	if err != nil {
		return err
	}
	files, err := delegate.LoadFiles(fs.Args())
	if err != nil {
		return err
	}
	res, err := delegate.Ask(context.Background(), r, *q, files)
	ev := ledger.Event{Kind: ledger.KindDelegate, Agent: os.Getenv("DETOUR_AGENT"), Mode: *mode, Model: r.ModelKey, Paths: fs.Args()}
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
	if len(args) < 1 || args[0] != "claude" || (len(args) == 2 && args[1] != "--block-only") || len(args) > 2 {
		return fmt.Errorf("usage: detour hook claude [--block-only]   (only Claude Code is supported for now)")
	}
	cfg := policy.FromEnv()
	cfg.BlockOnly = len(args) == 2
	return hook.Claude(os.Stdin, os.Stdout, cfg, ledger.Path())
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

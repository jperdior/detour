package bench

import (
	"fmt"
	"io"
	"math"
	"sort"
)

// Stats aggregates one scenario × variant cell.
type Stats struct {
	N, Errors, Passed int

	ClaudeCost, DelegateCost, NetCost []float64

	// Claude tokens by type: each type has a different price, so they are
	// never summed.
	InputTokens, CacheWriteTokens, CacheReadTokens, OutputTokens []float64
	DelegateTokens                                               []float64

	Turns, DurationS, Blocks []float64
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func sd(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	m := mean(xs)
	s := 0.0
	for _, x := range xs {
		s += (x - m) * (x - m)
	}
	return math.Sqrt(s / float64(len(xs)-1))
}

// Aggregate groups results by scenario and variant. Errored sessions are
// counted but left out of the averages.
func Aggregate(rs []Result) map[string]map[string]*Stats {
	out := map[string]map[string]*Stats{}
	for _, r := range rs {
		if out[r.Scenario] == nil {
			out[r.Scenario] = map[string]*Stats{}
		}
		st := out[r.Scenario][r.Variant]
		if st == nil {
			st = &Stats{}
			out[r.Scenario][r.Variant] = st
		}
		st.N++
		if r.Error != "" {
			st.Errors++
			continue
		}
		if r.Passed {
			st.Passed++
		}
		st.ClaudeCost = append(st.ClaudeCost, r.ClaudeCostUSD)
		st.DelegateCost = append(st.DelegateCost, r.DelegateCostUSD)
		st.NetCost = append(st.NetCost, r.NetCostUSD)
		st.InputTokens = append(st.InputTokens, float64(r.InputTokens))
		st.CacheWriteTokens = append(st.CacheWriteTokens, float64(r.CacheCreationTokens))
		st.CacheReadTokens = append(st.CacheReadTokens, float64(r.CacheReadTokens))
		st.OutputTokens = append(st.OutputTokens, float64(r.OutputTokens))
		st.DelegateTokens = append(st.DelegateTokens, float64(r.DelegateTokens))
		st.Turns = append(st.Turns, float64(r.Turns))
		st.DurationS = append(st.DurationS, float64(r.DurationMS)/1000)
		st.Blocks = append(st.Blocks, float64(r.Blocks))
	}
	return out
}

func pct(before, after float64) float64 { return 100 * (after - before) / before }

// WriteMarkdown prints a report: one table per scenario plus an overall delta.
func WriteMarkdown(w io.Writer, rs []Result) {
	agg := Aggregate(rs)
	var names []string
	for n := range agg {
		names = append(names, n)
	}
	sort.Strings(names)

	fmt.Fprintf(w, "# detour benchmark\n\n")
	fmt.Fprintf(w, "%d sessions. Mean ± sd over successful sessions.\n\n", len(rs))
	fmt.Fprintf(w, "- **Claude $**: `total_cost_usd` from Claude Code at list price. On a subscription you don't pay it, but it is the best public proxy for plan usage, which the delegate model doesn't consume.\n")
	fmt.Fprintf(w, "- **Net $**: Claude $ + delegate model cost. What you pay with an API key.\n")
	fmt.Fprintf(w, "- Token columns are Claude tokens by type, which are priced differently (cache reads are much cheaper than input, output much dearer), so they are not summed. Delegate tokens are the cheap model's, in + out.\n\n")

	var baseClaude, detClaude, baseNet, detNet float64
	var paired int
	for _, n := range names {
		fmt.Fprintf(w, "## %s\n\n", n)
		fmt.Fprintf(w, "| variant | ok/n | pass | Claude $ | net $ | input | cache write | cache read | output | delegate tok | turns | time s | blocks |\n")
		fmt.Fprintf(w, "|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
		for _, v := range []string{Baseline, Detour} {
			st := agg[n][v]
			if st == nil {
				continue
			}
			ok := st.N - st.Errors
			fmt.Fprintf(w, "| %s | %d/%d | %d/%d | %.4f ± %.4f | %.4f ± %.4f | %.0f | %.0f | %.0f | %.0f | %.0f | %.1f | %.0f | %.1f |\n",
				v, ok, st.N, st.Passed, ok,
				mean(st.ClaudeCost), sd(st.ClaudeCost), mean(st.NetCost), sd(st.NetCost),
				mean(st.InputTokens), mean(st.CacheWriteTokens), mean(st.CacheReadTokens), mean(st.OutputTokens),
				mean(st.DelegateTokens), mean(st.Turns), mean(st.DurationS), mean(st.Blocks))
		}
		b, d := agg[n][Baseline], agg[n][Detour]
		if b != nil && d != nil && len(b.NetCost) > 0 && len(d.NetCost) > 0 {
			bc, dc, bn, dn := mean(b.ClaudeCost), mean(d.ClaudeCost), mean(b.NetCost), mean(d.NetCost)
			fmt.Fprintf(w, "\nWith detour: Claude $ **%+.1f%%** (%.4f → %.4f), net $ **%+.1f%%** (%.4f → %.4f).\n",
				pct(bc, dc), bc, dc, pct(bn, dn), bn, dn)
			baseClaude += bc
			detClaude += dc
			baseNet += bn
			detNet += dn
			paired++
		}
		fmt.Fprintln(w)
	}
	if paired > 0 {
		fmt.Fprintf(w, "## Overall\n\nSum of per-scenario means across %d scenarios:\n\n", paired)
		fmt.Fprintf(w, "| | baseline | detour | change |\n|---|---|---|---|\n")
		fmt.Fprintf(w, "| Claude $ | %.4f | %.4f | **%+.1f%%** |\n", baseClaude, detClaude, pct(baseClaude, detClaude))
		fmt.Fprintf(w, "| net $ | %.4f | %.4f | **%+.1f%%** |\n", baseNet, detNet, pct(baseNet, detNet))
		fmt.Fprintf(w, "\nWith few repetitions, treat changes smaller than the sd as noise.\n")
	}
}

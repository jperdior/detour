package bench

import (
	"fmt"
	"io"
	"math"
	"sort"
)

// Stats aggregates one scenario × variant cell.
type Stats struct {
	N, Errors, Passed                 int
	NetCost, ClaudeCost, DelegateCost []float64
	ContextTokens, OutputTokens       []float64
	Turns, DurationS, Blocks          []float64
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
		st.NetCost = append(st.NetCost, r.NetCostUSD)
		st.ClaudeCost = append(st.ClaudeCost, r.ClaudeCostUSD)
		st.DelegateCost = append(st.DelegateCost, r.DelegateCostUSD)
		st.ContextTokens = append(st.ContextTokens, float64(r.ContextTokens()))
		st.OutputTokens = append(st.OutputTokens, float64(r.OutputTokens))
		st.Turns = append(st.Turns, float64(r.Turns))
		st.DurationS = append(st.DurationS, float64(r.DurationMS)/1000)
		st.Blocks = append(st.Blocks, float64(r.Blocks))
	}
	return out
}

// WriteMarkdown prints a report: one table per scenario plus an overall delta.
func WriteMarkdown(w io.Writer, rs []Result) {
	agg := Aggregate(rs)
	var names []string
	for n := range agg {
		names = append(names, n)
	}
	sort.Strings(names)

	fmt.Fprintf(w, "# detour benchmark\n\n")
	fmt.Fprintf(w, "%d sessions. Costs are USD. Claude cost is `total_cost_usd` as reported by Claude Code (list price; notional on a subscription). Net = Claude + delegate model. Mean ± sd over successful sessions.\n\n", len(rs))

	var baseTotal, detTotal float64
	var paired int
	for _, n := range names {
		fmt.Fprintf(w, "## %s\n\n", n)
		fmt.Fprintf(w, "| variant | ok/n | pass | net cost | claude | delegate | context tok | output tok | turns | time s | blocks |\n")
		fmt.Fprintf(w, "|---|---|---|---|---|---|---|---|---|---|---|\n")
		for _, v := range []string{Baseline, Detour} {
			st := agg[n][v]
			if st == nil {
				continue
			}
			ok := st.N - st.Errors
			fmt.Fprintf(w, "| %s | %d/%d | %d/%d | %.4f ± %.4f | %.4f | %.4f | %.0f | %.0f | %.1f | %.0f | %.1f |\n",
				v, ok, st.N, st.Passed, ok,
				mean(st.NetCost), sd(st.NetCost), mean(st.ClaudeCost), mean(st.DelegateCost),
				mean(st.ContextTokens), mean(st.OutputTokens), mean(st.Turns), mean(st.DurationS), mean(st.Blocks))
		}
		b, d := agg[n][Baseline], agg[n][Detour]
		if b != nil && d != nil && len(b.NetCost) > 0 && len(d.NetCost) > 0 {
			mb, md := mean(b.NetCost), mean(d.NetCost)
			fmt.Fprintf(w, "\nNet cost change with detour: **%+.1f%%** (%.4f → %.4f).\n", 100*(md-mb)/mb, mb, md)
			baseTotal += mb
			detTotal += md
			paired++
		}
		fmt.Fprintln(w)
	}
	if paired > 0 {
		fmt.Fprintf(w, "## Overall\n\nSum of per-scenario mean net cost: baseline %.4f, detour %.4f, change **%+.1f%%** across %d scenarios.\n",
			baseTotal, detTotal, 100*(detTotal-baseTotal)/baseTotal, paired)
		fmt.Fprintf(w, "\nWith few repetitions, treat changes smaller than the sd as noise.\n")
	}
}

package trial

import (
	"fmt"
	"io"
	"math"
	"math/rand"
	"sort"
	"strings"

	"github.com/jperdior/detour/internal/ledger"
	"github.com/jperdior/detour/internal/transcript"
)

// Row is one measured session.
type Row struct {
	ID, Arm, Project, Origin, Activity string
	CostUSD                            float64 // agent transcript + Claude-plan delegations
	DelegateUSD                        float64
	Tokens                             transcript.Usage // summed over models
	Calls, Prompts                     int
	Minutes                            float64
	Blocks                             int // block, or shadow-block in the off arm
	Delegations                        int
	Unpriced                           []string
}

// Rows measures every ended trial session. Transcripts are read again, so a
// session resumed after its SessionEnd is counted in full.
func Rows(dir string, events []ledger.Event) ([]Row, error) {
	ss, err := All(dir)
	if err != nil {
		return nil, err
	}
	by := map[string][]ledger.Event{}
	for _, e := range events {
		if e.SessionID != "" {
			by[e.SessionID] = append(by[e.SessionID], e)
		}
	}
	var rows []Row
	for _, s := range ss {
		if s.Ended.IsZero() {
			continue
		}
		u := s.Usage
		if s.TranscriptPath != "" {
			if fresh, err := transcript.Summarize(s.TranscriptPath); err == nil {
				u = &fresh
			}
		}
		if u == nil || u.Calls() == 0 {
			continue
		}
		r := Row{ID: s.ID, Arm: s.Arm, Project: s.Project, Origin: s.Origin, Activity: u.Activity(),
			CostUSD: u.CostUSD, Calls: u.Calls(), Prompts: u.UserPrompts, Unpriced: u.UnpricedModel}
		if !u.First.IsZero() {
			r.Minutes = u.Last.Sub(u.First).Minutes()
		}
		for _, m := range u.Models {
			r.Tokens.Input += m.Input
			r.Tokens.CacheWrite5m += m.CacheWrite5m
			r.Tokens.CacheWrite1h += m.CacheWrite1h
			r.Tokens.CacheRead += m.CacheRead
			r.Tokens.Output += m.Output
		}
		for _, e := range by[s.ID] {
			switch e.Kind {
			case ledger.KindBlock, ledger.ShadowPrefix + ledger.KindBlock:
				r.Blocks++
			case ledger.KindDelegate:
				if e.Error == "" {
					r.Delegations++
				}
				if e.ClaudePlan {
					r.DelegateUSD += e.CostUSD
				}
			}
		}
		r.CostUSD += r.DelegateUSD
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}

// WriteReport prints the trial report as markdown.
func WriteReport(w io.Writer, cfg Config, rows []Row) {
	fmt.Fprintf(w, "# detour field trial\n\n")
	fmt.Fprintf(w, "%d measured sessions (target %d). Cost is list price for everything the session drew from the Claude plan: the agent, its subagents and Haiku delegations.\n\n", len(rows), cfg.Target)
	arms := cfg.Arms

	fmt.Fprintf(w, "## All sessions\n\n")
	armTable(w, arms, rows)
	diffLine(w, arms, rows)

	engaged := filter(rows, func(r Row) bool { return r.Blocks > 0 })
	fmt.Fprintf(w, "\n## Sessions that tried to read a large file whole\n\nIn the off arm these are shadow blocks: what detour would have blocked. This is where detour can act at all.\n\n")
	armTable(w, arms, engaged)
	diffLine(w, arms, engaged)

	for _, dim := range []struct {
		name string
		key  func(Row) string
	}{
		{"activity (from tool use)", func(r Row) string { return r.Activity }},
		{"origin", func(r Row) string { return r.Origin }},
		{"project", func(r Row) string { return r.Project }},
	} {
		fmt.Fprintf(w, "\n## By %s\n\n", dim.name)
		fmt.Fprintf(w, "| %s |", strings.Split(dim.name, " ")[0])
		for _, a := range arms {
			fmt.Fprintf(w, " %s n | %s mean $ | %s median $ |", a, a, a)
		}
		fmt.Fprintf(w, " change in mean |\n|---|")
		for range arms {
			fmt.Fprintf(w, "---|---|---|")
		}
		fmt.Fprintf(w, "---|\n")
		for _, k := range keys(rows, dim.key) {
			sub := filter(rows, func(r Row) bool { return dim.key(r) == k })
			fmt.Fprintf(w, "| %s |", k)
			var means []float64
			for _, a := range arms {
				c := costs(filter(sub, func(r Row) bool { return r.Arm == a }))
				means = append(means, mean(c))
				fmt.Fprintf(w, " %d | %.3f | %.3f |", len(c), mean(c), median(c))
			}
			if len(means) >= 2 && means[0] > 0 && !math.IsNaN(means[1]) {
				fmt.Fprintf(w, " %+.1f%% |\n", (means[1]-means[0])/means[0]*100)
			} else {
				fmt.Fprintf(w, " |\n")
			}
		}
	}
	var unpriced []string
	for _, r := range rows {
		unpriced = append(unpriced, r.Unpriced...)
	}
	if len(unpriced) > 0 {
		fmt.Fprintf(w, "\nModels without a known price (counted at Opus 5.5's): %s\n", strings.Join(uniq(unpriced), ", "))
	}
	fmt.Fprintf(w, "\nSession cost is heavy-tailed: compare medians too, and trust a difference only when its 95%% interval excludes zero.\n")
}

func armTable(w io.Writer, arms []string, rows []Row) {
	fmt.Fprintf(w, "| arm | n | mean $ | median $ | total $ | input | cache write | cache read | output | API calls | blocks | delegations | Haiku $ | minutes (median) |\n")
	fmt.Fprintf(w, "|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, a := range arms {
		sub := filter(rows, func(r Row) bool { return r.Arm == a })
		c := costs(sub)
		var in, cw, cr, out, calls, blocks, dels, mins, del []float64
		for _, r := range sub {
			in = append(in, float64(r.Tokens.Input))
			cw = append(cw, float64(r.Tokens.CacheWrite5m+r.Tokens.CacheWrite1h))
			cr = append(cr, float64(r.Tokens.CacheRead))
			out = append(out, float64(r.Tokens.Output))
			calls = append(calls, float64(r.Calls))
			blocks = append(blocks, float64(r.Blocks))
			dels = append(dels, float64(r.Delegations))
			del = append(del, r.DelegateUSD)
			mins = append(mins, r.Minutes)
		}
		fmt.Fprintf(w, "| %s | %d | %.3f | %.3f | %.2f | %.0f | %.0f | %.0f | %.0f | %.1f | %.2f | %.2f | %.4f | %.1f |\n",
			a, len(sub), mean(c), median(c), sum(c), mean(in), mean(cw), mean(cr), mean(out), mean(calls), mean(blocks), mean(dels), mean(del), median(mins))
	}
}

// diffLine compares the second arm with the first: mean difference with a
// bootstrap 95% interval.
func diffLine(w io.Writer, arms []string, rows []Row) {
	if len(arms) < 2 {
		return
	}
	a := costs(filter(rows, func(r Row) bool { return r.Arm == arms[0] }))
	b := costs(filter(rows, func(r Row) bool { return r.Arm == arms[1] }))
	if len(a) < 2 || len(b) < 2 {
		fmt.Fprintf(w, "\nNot enough sessions in each arm to compare yet.\n")
		return
	}
	lo, hi := bootstrapPct(a, b, 4000)
	fmt.Fprintf(w, "\n%s vs %s: mean cost **%+.1f%%** (95%% interval %+.1f%% to %+.1f%%), median %+.1f%%.\n",
		arms[1], arms[0], pct(mean(a), mean(b)), lo, hi, pct(median(a), median(b)))
}

func bootstrapPct(a, b []float64, n int) (lo, hi float64) {
	rng := rand.New(rand.NewSource(1))
	var ds []float64
	for i := 0; i < n; i++ {
		ma, mb := resampleMean(rng, a), resampleMean(rng, b)
		if ma > 0 {
			ds = append(ds, pct(ma, mb))
		}
	}
	sort.Float64s(ds)
	if len(ds) == 0 {
		return math.NaN(), math.NaN()
	}
	return ds[int(0.025*float64(len(ds)))], ds[int(0.975*float64(len(ds)-1))]
}

func resampleMean(rng *rand.Rand, xs []float64) float64 {
	s := 0.0
	for range xs {
		s += xs[rng.Intn(len(xs))]
	}
	return s / float64(len(xs))
}

func filter(rows []Row, keep func(Row) bool) []Row {
	var out []Row
	for _, r := range rows {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

func keys(rows []Row, key func(Row) string) []string {
	var ks []string
	for _, r := range rows {
		ks = append(ks, key(r))
	}
	return uniq(ks)
}

func uniq(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

func costs(rows []Row) []float64 {
	var c []float64
	for _, r := range rows {
		c = append(c, r.CostUSD)
	}
	return c
}

func sum(xs []float64) float64 {
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	return sum(xs) / float64(len(xs))
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}

func pct(base, v float64) float64 { return (v - base) / base * 100 }

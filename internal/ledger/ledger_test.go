package ledger

import (
	"path/filepath"
	"testing"
)

func TestAppendReadSummarize(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "ledger.jsonl")
	t.Setenv("DETOUR_RUN_ID", "run-1")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(Append(p, Event{Kind: KindBlock, EstTokens: 1000}))
	must(Append(p, Event{Kind: KindDelegate, PromptTokens: 900, CompletionTokens: 100, CostUSD: 0.001}))
	must(Append(p, Event{Kind: KindDelegate, Error: "boom", RunID: "run-2"}))
	must(Append(p, Event{Kind: KindDelegate, CostUSD: 0.002, ClaudePlan: true, RunID: "run-3"}))

	events, err := Read(p)
	must(err)
	if len(events) != 4 {
		t.Fatalf("got %d events", len(events))
	}
	if events[0].RunID != "run-1" || events[0].Time.IsZero() {
		t.Fatalf("run id/time not filled: %+v", events[0])
	}

	all := Summarize(events, nil)
	if all.PlanCostUSD != 0.002 {
		t.Fatalf("plan cost: %+v", all)
	}
	if all.Blocks != 1 || all.Delegations != 2 || all.DelegateErrors != 1 || all.BlockedEstTokens != 1000 {
		t.Fatalf("summary: %+v", all)
	}
	run1 := Summarize(events, func(e Event) bool { return e.RunID == "run-1" })
	if run1.DelegateErrors != 0 || run1.CostUSD != 0.001 {
		t.Fatalf("filtered summary: %+v", run1)
	}
}

func TestReadMissingIsEmpty(t *testing.T) {
	ev, err := Read(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil || len(ev) != 0 {
		t.Fatalf("got %v, %v", ev, err)
	}
}

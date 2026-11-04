package transcript

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mainLog = `{"type":"user","timestamp":"2026-11-04T10:00:00Z","message":{"role":"user","content":"explain the report flow"}}
{"type":"assistant","timestamp":"2026-11-04T10:00:05Z","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"thinking","thinking":""}],"usage":{"input_tokens":8,"cache_creation_input_tokens":19751,"cache_read_input_tokens":85488,"output_tokens":3555,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":19751}}}}
{"type":"assistant","timestamp":"2026-11-04T10:00:06Z","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"tool_use","name":"Read","input":{}}],"usage":{"input_tokens":8,"cache_creation_input_tokens":19751,"cache_read_input_tokens":85488,"output_tokens":3555,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":19751}}}}
{"type":"user","timestamp":"2026-11-04T10:00:07Z","message":{"role":"user","content":[{"type":"tool_result","content":"..."}]}}
{"type":"assistant","timestamp":"2026-11-04T10:00:09Z","message":{"id":"m2","model":"claude-opus-5-5","content":[{"type":"tool_use","name":"Edit","input":{}}],"usage":{"input_tokens":2,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":50}}}
{"type":"assistant","timestamp":"2026-11-04T10:00:09Z","message":{"id":"x","model":"<synthetic>","content":[],"usage":{"input_tokens":999,"output_tokens":999}}}
not json
{"type":"user","timestamp":"2026-11-04T10:02:00Z","message":{"role":"user","content":"thanks"}}
`

const subLog = `{"type":"assistant","isSidechain":true,"timestamp":"2026-11-04T10:01:00Z","message":{"id":"s1","model":"claude-haiku-5-5","content":[{"type":"tool_use","name":"Grep","input":{}}],"usage":{"input_tokens":1000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":100}}}
`

func TestSummarize(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "abc.jsonl")
	os.WriteFile(p, []byte(mainLog), 0o644)
	os.MkdirAll(filepath.Join(dir, "abc", "subagents"), 0o755)
	os.WriteFile(filepath.Join(dir, "abc", "subagents", "agent-1.jsonl"), []byte(subLog), 0o644)

	s, err := Summarize(p)
	if err != nil {
		t.Fatal(err)
	}
	o := s.Models["claude-opus-5-5"]
	if o == nil || o.Calls != 2 || o.Input != 10 || o.CacheWrite1h != 19751 || o.CacheWrite5m != 100 || o.CacheRead != 86488 || o.Output != 3605 {
		t.Fatalf("opus usage (message m1 must count once): %+v", o)
	}
	if _, ok := s.Models["<synthetic>"]; ok {
		t.Fatal("synthetic messages are not API calls")
	}
	if h := s.Models["claude-haiku-5-5"]; h == nil || h.Input != 1000 || s.Subagents != 1 {
		t.Fatalf("subagent usage: %+v, subagents %d", h, s.Subagents)
	}
	if s.Tools["Read"] != 1 || s.Tools["Edit"] != 1 || s.Tools["Grep"] != 1 || s.Activity() != "edit" {
		t.Fatalf("tools %v activity %s", s.Tools, s.Activity())
	}
	if s.UserPrompts != 2 {
		t.Fatalf("human prompts: %d", s.UserPrompts)
	}
	if s.Last.Sub(s.First).Minutes() != 2 {
		t.Fatalf("duration %v", s.Last.Sub(s.First))
	}
	// m1 alone is a real Claude Code result: total_cost_usd 0.2462376.
	m1 := Usage{Input: 8, CacheWrite1h: 19751, CacheRead: 85488, Output: 3555}
	if p, _ := PriceFor("claude-opus-5-5"); math.Abs(p.Cost(m1)-0.2462376) > 1e-7 {
		t.Fatalf("opus 5.5 price: %.7f", p.Cost(m1))
	}
}

func TestUnknownModelIsFlagged(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(p, []byte(strings.ReplaceAll(subLog, "claude-haiku-5-5", "claude-new-9")), 0o644)
	s, _ := Summarize(p)
	if len(s.UnpricedModel) != 1 || s.CostUSD == 0 {
		t.Fatalf("unknown model must be priced and flagged: %+v", s)
	}
}

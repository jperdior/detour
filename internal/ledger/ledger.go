// Package ledger appends one JSON line per detour event (blocked read,
// targeted read, delegation) so savings and costs can be measured later.
package ledger

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Event kinds.
const (
	KindBlock    = "block"    // the hook denied a whole-file read
	KindPartial  = "partial"  // the hook allowed a targeted read of a large file
	KindDelegate = "delegate" // a question was answered by the cheap model
)

// Event is one ledger line.
type Event struct {
	Time      time.Time `json:"ts"`
	RunID     string    `json:"run_id,omitempty"` // set by the benchmark via DETOUR_RUN_ID
	SessionID string    `json:"session_id,omitempty"`
	Kind      string    `json:"kind"`
	Agent     string    `json:"agent,omitempty"`
	Tool      string    `json:"tool,omitempty"`
	Paths     []string  `json:"paths,omitempty"`
	Lines     int       `json:"lines,omitempty"`
	Bytes     int64     `json:"bytes,omitempty"`
	EstTokens int       `json:"est_tokens,omitempty"` // bytes/4, blocked content only

	Mode             string  `json:"mode,omitempty"`
	Model            string  `json:"model,omitempty"`
	PromptTokens     int     `json:"prompt_tokens,omitempty"`
	CachedTokens     int     `json:"cached_tokens,omitempty"`
	CompletionTokens int     `json:"completion_tokens,omitempty"`
	AnswerBytes      int     `json:"answer_bytes,omitempty"` // what flows back into the expensive context
	CostUSD          float64 `json:"cost_usd,omitempty"`
	ClaudePlan       bool    `json:"claude_plan,omitempty"` // cost counts against the user's Claude usage
	LatencyMS        int64   `json:"latency_ms,omitempty"`
	Error            string  `json:"error,omitempty"`
}

// Path is DETOUR_LEDGER or ~/.local/state/detour/ledger.jsonl.
func Path() string {
	if p := os.Getenv("DETOUR_LEDGER"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "detour-ledger.jsonl"
	}
	return filepath.Join(home, ".local", "state", "detour", "ledger.jsonl")
}

// Append writes e to the ledger at path, filling Time and RunID if empty.
func Append(path string, e Event) error {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	if e.RunID == "" {
		e.RunID = os.Getenv("DETOUR_RUN_ID")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// Read returns all events at path. A missing file is an empty ledger.
func Read(path string) ([]Event, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// Summary aggregates events.
type Summary struct {
	Blocks, Partials, Delegations, DelegateErrors int
	BlockedEstTokens                              int
	PromptTokens, CachedTokens, CompletionTokens  int
	AnswerBytes                                   int
	CostUSD                                       float64
	PlanCostUSD                                   float64 // part of CostUSD on the user's Claude plan
	LatencyMS                                     int64
}

// Summarize aggregates events, keeping only those for which keep is true
// (nil keeps all).
func Summarize(events []Event, keep func(Event) bool) Summary {
	var s Summary
	for _, e := range events {
		if keep != nil && !keep(e) {
			continue
		}
		switch e.Kind {
		case KindBlock:
			s.Blocks++
			s.BlockedEstTokens += e.EstTokens
		case KindPartial:
			s.Partials++
		case KindDelegate:
			if e.Error != "" {
				s.DelegateErrors++
				continue
			}
			s.Delegations++
			s.PromptTokens += e.PromptTokens
			s.CachedTokens += e.CachedTokens
			s.CompletionTokens += e.CompletionTokens
			s.AnswerBytes += e.AnswerBytes
			s.CostUSD += e.CostUSD
			if e.ClaudePlan {
				s.PlanCostUSD += e.CostUSD
			}
			s.LatencyMS += e.LatencyMS
		}
	}
	return s
}

// Package transcript measures a Claude Code session from the transcript
// Claude Code writes under ~/.claude/projects: tokens by type and model,
// list-price cost, tool use and duration. Subagent transcripts, stored next
// to the session's in <session-id>/subagents/, are included.
package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Usage is the token count of one model, by type.
type Usage struct {
	Input        int64 `json:"input"`
	CacheWrite5m int64 `json:"cache_write_5m"`
	CacheWrite1h int64 `json:"cache_write_1h"`
	CacheRead    int64 `json:"cache_read"`
	Output       int64 `json:"output"`
	Calls        int   `json:"calls"` // API calls (distinct message ids)
}

// Summary measures one session.
type Summary struct {
	Models        map[string]*Usage `json:"models"`
	CostUSD       float64           `json:"cost_usd"` // list price, all models
	UnpricedModel []string          `json:"unpriced_models,omitempty"`
	Tools         map[string]int    `json:"tools,omitempty"` // tool_use blocks by name, subagents included
	UserPrompts   int               `json:"user_prompts"`    // human turns in the main conversation
	Subagents     int               `json:"subagents"`
	First         time.Time         `json:"first"`
	Last          time.Time         `json:"last"`
}

// Calls is the number of API calls across models.
func (s Summary) Calls() int {
	n := 0
	for _, u := range s.Models {
		n += u.Calls
	}
	return n
}

// Edits counts file-changing tool calls.
func (s Summary) Edits() int {
	return s.Tools["Edit"] + s.Tools["Write"] + s.Tools["MultiEdit"] + s.Tools["NotebookEdit"]
}

// Activity is a coarse class from tool use: "edit" if any file was changed,
// "read" if tools were used without edits, "chat" otherwise.
func (s Summary) Activity() string {
	switch {
	case s.Edits() > 0:
		return "edit"
	case len(s.Tools) > 0:
		return "read"
	}
	return "chat"
}

type line struct {
	Type        string    `json:"type"`
	Timestamp   time.Time `json:"timestamp"`
	IsSidechain bool      `json:"isSidechain"`
	IsMeta      bool      `json:"isMeta"`
	Message     struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens              int64 `json:"input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreation            *struct {
				Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
				Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

type block struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// Summarize reads the session transcript at path and its subagents.
func Summarize(path string) (Summary, error) {
	s := Summary{Models: map[string]*Usage{}, Tools: map[string]int{}}
	seen := map[string]bool{}
	if err := s.add(path, true, seen); err != nil {
		return s, err
	}
	subs, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents", "*.jsonl"))
	sort.Strings(subs)
	for _, p := range subs {
		if err := s.add(p, false, seen); err == nil {
			s.Subagents++
		}
	}
	for model, u := range s.Models {
		p, ok := PriceFor(model)
		if !ok {
			s.UnpricedModel = append(s.UnpricedModel, model)
		}
		s.CostUSD += p.Cost(*u)
	}
	sort.Strings(s.UnpricedModel)
	return s, nil
}

func (s *Summary) add(path string, main bool, seen map[string]bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		var l line
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		if !l.Timestamp.IsZero() {
			if s.First.IsZero() || l.Timestamp.Before(s.First) {
				s.First = l.Timestamp
			}
			if l.Timestamp.After(s.Last) {
				s.Last = l.Timestamp
			}
		}
		switch l.Type {
		case "assistant":
			s.addAssistant(l, seen)
		case "user":
			if main && !l.IsSidechain && !l.IsMeta && isHumanPrompt(l.Message.Content) {
				s.UserPrompts++
			}
		}
	}
	return sc.Err()
}

func (s *Summary) addAssistant(l line, seen map[string]bool) {
	var blocks []block
	if json.Unmarshal(l.Message.Content, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "tool_use" && b.Name != "" {
				s.Tools[b.Name]++
			}
		}
	}
	// Claude Code writes one line per content block, each repeating the
	// message's usage, so count usage once per message id.
	u := l.Message.Usage
	if u == nil || l.Message.Model == "" || l.Message.Model == "<synthetic>" {
		return
	}
	if l.Message.ID != "" {
		if seen[l.Message.ID] {
			return
		}
		seen[l.Message.ID] = true
	}
	m := s.Models[l.Message.Model]
	if m == nil {
		m = &Usage{}
		s.Models[l.Message.Model] = m
	}
	m.Calls++
	m.Input += u.InputTokens
	m.CacheRead += u.CacheReadInputTokens
	m.Output += u.OutputTokens
	if c := u.CacheCreation; c != nil && c.Ephemeral5m+c.Ephemeral1h == u.CacheCreationInputTokens {
		m.CacheWrite5m += c.Ephemeral5m
		m.CacheWrite1h += c.Ephemeral1h
	} else {
		m.CacheWrite5m += u.CacheCreationInputTokens
	}
}

// isHumanPrompt is true for a user line typed by a person: plain text, not
// a tool result or a command's injected output.
func isHumanPrompt(raw json.RawMessage) bool {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text != "" && !strings.HasPrefix(text, "<local-command") && !strings.HasPrefix(text, "<command-")
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return false
		}
	}
	return len(blocks) > 0
}

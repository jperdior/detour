// Package delegate sends a question plus files to a mode's cheap model and
// returns a compact answer for the expensive agent.
package delegate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jperdior/detour/internal/config"
	"github.com/jperdior/detour/internal/llm"
)

// Result of one delegation.
type Result struct {
	Answer    string
	Usage     llm.Usage
	CostUSD   float64
	Latency   time.Duration
	InputLen  int // bytes of file content sent
	FileCount int
	// ClaudePlan: the call consumed the user's Claude usage (claude-cli).
	ClaudePlan bool
}

// secretPatterns are file names never sent to a third-party model.
var secretPatterns = []string{
	".env", ".env.*", "*.pem", "*.key", "*.p12", "*.pfx", "id_rsa*", "id_ed25519*",
	"*.keystore", "credentials", "credentials.*", ".npmrc", ".netrc", "*.tfvars",
}

// LooksSecret reports whether a path matches a known secret file name.
func LooksSecret(path string) bool {
	base := filepath.Base(path)
	for _, p := range secretPatterns {
		if ok, _ := filepath.Match(p, base); ok {
			return true
		}
	}
	return false
}

// File is a file to send.
type File struct {
	Path    string
	Content string
}

// LoadFiles reads paths, refusing secrets.
func LoadFiles(paths []string) ([]File, error) {
	var out []File
	for _, p := range paths {
		if LooksSecret(p) {
			return nil, fmt.Errorf("%s looks like a secrets file; detour will not send it to another model", p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		out = append(out, File{Path: p, Content: string(b)})
	}
	return out, nil
}

// BuildPrompt wraps each file in XML tags with numbered lines ("L12|..."), so
// the worker can cite exact lines and the agent can follow up with targeted
// reads. Numbering costs worker tokens, which are cheap.
func BuildPrompt(question string, files []File) string {
	var b strings.Builder
	for _, f := range files {
		lines := strings.Split(strings.TrimSuffix(f.Content, "\n"), "\n")
		fmt.Fprintf(&b, "<file path=%q lines=\"%d\">\n", f.Path, len(lines))
		for i, l := range lines {
			fmt.Fprintf(&b, "L%d|%s\n", i+1, l)
		}
		b.WriteString("</file>\n")
	}
	fmt.Fprintf(&b, "\n<question>\n%s\n</question>\n", strings.TrimSpace(question))
	return b.String()
}

// Ask runs the delegation.
func Ask(ctx context.Context, r *config.Resolved, question string, files []File) (*Result, error) {
	prompt := BuildPrompt(question, files)
	if max := r.Mode.MaxInputTokens; max > 0 && len(prompt)/4 > max {
		return nil, fmt.Errorf("input is ~%d tokens, over this mode's limit of %d; split the files across several questions", len(prompt)/4, max)
	}
	timeout := time.Duration(r.Mode.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if r.Provider.Type == config.TypeClaudeCLI {
		return askClaudeCLI(ctx, r, prompt, files)
	}

	start := time.Now()
	resp, err := llm.Chat(ctx, http.DefaultClient, r.Provider.BaseURL, r.APIKey, llm.Request{
		Model:       r.Model.ID,
		Temperature: r.Mode.Temperature,
		MaxTokens:   r.Mode.MaxOutputTokens,
		Messages: []llm.Message{
			{Role: "system", Content: r.Mode.Instructions},
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return nil, err
	}
	return &Result{
		Answer:    strings.TrimSpace(resp.Content),
		Usage:     resp.Usage,
		CostUSD:   r.Model.Price.Cost(resp.Usage.PromptTokens, resp.Usage.CachedTokens, resp.Usage.CompletionTokens),
		Latency:   time.Since(start),
		InputLen:  contentLen(files),
		FileCount: len(files),
	}, nil
}

func contentLen(files []File) int {
	n := 0
	for _, f := range files {
		n += len(f.Content)
	}
	return n
}

// askClaudeCLI runs the mode through `claude -p` with no tools, no user
// settings (so no plugins or hooks, and no recursion into detour) and the
// mode's instructions as the whole system prompt. Fixed overhead is a few
// hundred tokens. The prompt goes on stdin to avoid argument size limits.
func askClaudeCLI(ctx context.Context, r *config.Resolved, prompt string, files []File) (*Result, error) {
	bin := r.Provider.Bin
	if bin == "" {
		bin = "claude"
	}
	args := []string{
		"-p",
		"--model", r.Model.ID,
		"--output-format", "json",
		"--system-prompt", r.Mode.Instructions,
		"--tools", "",
		"--setting-sources", "",
		"--strict-mcp-config",
		"--no-session-persistence",
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	runErr := cmd.Run()

	var out struct {
		IsError      bool    `json:"is_error"`
		Subtype      string  `json:"subtype"`
		Result       string  `json:"result"`
		TotalCostUSD float64 `json:"total_cost_usd"`
		Usage        struct {
			InputTokens              int `json:"input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			OutputTokens             int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &out); err != nil {
		return nil, fmt.Errorf("%s -p: %v (exit: %v) %s", bin, err, runErr, strings.TrimSpace(stderr.String()))
	}
	if out.IsError {
		return nil, fmt.Errorf("%s -p: %s: %s", bin, out.Subtype, out.Result)
	}
	u := out.Usage
	return &Result{
		Answer: strings.TrimSpace(out.Result),
		Usage: llm.Usage{
			PromptTokens:     u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens,
			CachedTokens:     u.CacheReadInputTokens,
			CompletionTokens: u.OutputTokens,
		},
		CostUSD:    out.TotalCostUSD,
		Latency:    time.Since(start),
		InputLen:   contentLen(files),
		FileCount:  len(files),
		ClaudePlan: true,
	}, nil
}

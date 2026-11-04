// Package delegate sends a question plus files to a mode's cheap model and
// returns a compact answer for the expensive agent.
package delegate

import (
	"context"
	"fmt"
	"net/http"
	"os"
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
	total := 0
	for _, f := range files {
		total += len(f.Content)
	}
	return &Result{
		Answer:    strings.TrimSpace(resp.Content),
		Usage:     resp.Usage,
		CostUSD:   r.Model.Price.Cost(resp.Usage.PromptTokens, resp.Usage.CachedTokens, resp.Usage.CompletionTokens),
		Latency:   time.Since(start),
		InputLen:  total,
		FileCount: len(files),
	}, nil
}

// Package llm is a minimal client for OpenAI-compatible chat completion
// endpoints (DeepSeek, OpenRouter, Ollama, LiteLLM, vLLM...).
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Message is a chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request is a chat completion request.
type Request struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream"`
}

// Usage is normalised token usage.
type Usage struct {
	PromptTokens     int
	CachedTokens     int
	CompletionTokens int
}

// Response is the answer and its usage.
type Response struct {
	Content string
	Usage   Usage
}

type wireResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens         int `json:"prompt_tokens"`
		CompletionTokens     int `json:"completion_tokens"`
		PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"` // DeepSeek
		PromptTokensDetails  *struct {
			CachedTokens int `json:"cached_tokens"` // OpenAI style
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Chat sends one chat completion request.
func Chat(ctx context.Context, client *http.Client, baseURL, apiKey string, req Request) (*Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(baseURL, "/") + "/chat/completions"
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		hr.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	var w wireResponse
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("%s: HTTP %d: %s", url, resp.StatusCode, truncate(string(raw), 300))
	}
	if resp.StatusCode >= 300 || w.Error != nil {
		msg := truncate(string(raw), 300)
		if w.Error != nil {
			msg = w.Error.Message
		}
		return nil, fmt.Errorf("%s: HTTP %d: %s", url, resp.StatusCode, msg)
	}
	if len(w.Choices) == 0 {
		return nil, fmt.Errorf("%s: response has no choices", url)
	}
	u := Usage{PromptTokens: w.Usage.PromptTokens, CompletionTokens: w.Usage.CompletionTokens, CachedTokens: w.Usage.PromptCacheHitTokens}
	if u.CachedTokens == 0 && w.Usage.PromptTokensDetails != nil {
		u.CachedTokens = w.Usage.PromptTokensDetails.CachedTokens
	}
	return &Response{Content: w.Choices[0].Message.Content, Usage: u}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

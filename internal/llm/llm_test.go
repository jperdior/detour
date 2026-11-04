package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatDeepSeekUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("bad request: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var req Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "m" || len(req.Messages) != 2 {
			t.Errorf("bad body: %+v", req)
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}],
			"usage":{"prompt_tokens":100,"completion_tokens":7,"prompt_cache_hit_tokens":40}}`))
	}))
	defer srv.Close()

	resp, err := Chat(context.Background(), srv.Client(), srv.URL+"/", "k", Request{
		Model: "m", Messages: []Message{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "hi" || resp.Usage != (Usage{PromptTokens: 100, CachedTokens: 40, CompletionTokens: 7}) {
		t.Fatalf("got %+v", resp)
	}
}

func TestChatOpenAIStyleCachedTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"x"}}],
			"usage":{"prompt_tokens":10,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":4}}}`))
	}))
	defer srv.Close()
	resp, err := Chat(context.Background(), srv.Client(), srv.URL, "", Request{Model: "m"})
	if err != nil || resp.Usage.CachedTokens != 4 {
		t.Fatalf("got %+v, %v", resp, err)
	}
}

func TestChatHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"message":"Authentication Fails"}}`))
	}))
	defer srv.Close()
	_, err := Chat(context.Background(), srv.Client(), srv.URL, "bad", Request{Model: "m"})
	if err == nil || !strings.Contains(err.Error(), "Authentication Fails") {
		t.Fatalf("got %v", err)
	}
}

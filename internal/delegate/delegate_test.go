package delegate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jperdior/detour/internal/config"
)

func TestLooksSecret(t *testing.T) {
	for _, p := range []string{".env", "app/.env.local", "certs/server.pem", "id_rsa", "prod.tfvars"} {
		if !LooksSecret(p) {
			t.Errorf("%s should be secret", p)
		}
	}
	for _, p := range []string{"command.go", "env.go", "README.md"} {
		if LooksSecret(p) {
			t.Errorf("%s should not be secret", p)
		}
	}
}

func TestBuildPromptNumbersLines(t *testing.T) {
	p := BuildPrompt("  what? ", []File{{Path: "a.go", Content: "package a\nfunc A() {}\n"}})
	want := "<file path=\"a.go\" lines=\"2\">\nL1|package a\nL2|func A() {}\n</file>\n\n<question>\nwhat?\n</question>\n"
	if p != want {
		t.Fatalf("got:\n%q\nwant:\n%q", p, want)
	}
}

func TestAskComputesCost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		msgs := body["messages"].([]any)
		if !strings.Contains(msgs[1].(map[string]any)["content"].(string), "L1|x") {
			t.Error("files not in prompt")
		}
		w.Write([]byte(`{"choices":[{"message":{"content":" - answer \n"}}],"usage":{"prompt_tokens":1000000,"completion_tokens":0}}`))
	}))
	defer srv.Close()

	r := &config.Resolved{
		Mode:     config.Mode{Instructions: "be brief"},
		Model:    config.Model{ID: "m", Price: config.Price{Input: 0.3}},
		Provider: config.Provider{BaseURL: srv.URL},
	}
	res, err := Ask(context.Background(), r, "q", []File{{Path: "f", Content: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer != "- answer" || res.CostUSD != 0.3 {
		t.Fatalf("got %+v", res)
	}
}

func TestAskRejectsOversizedInput(t *testing.T) {
	r := &config.Resolved{Mode: config.Mode{MaxInputTokens: 10}}
	_, err := Ask(context.Background(), r, "q", []File{{Path: "f", Content: strings.Repeat("x", 1000)}})
	if err == nil || !strings.Contains(err.Error(), "split") {
		t.Fatalf("got %v", err)
	}
}

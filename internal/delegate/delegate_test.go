package delegate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// fakeClaude writes a script that records its stdin and args and prints a
// canned `claude -p --output-format json` result.
func fakeClaude(t *testing.T, result string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "claude")
	script := "#!/bin/sh\ncat > \"" + dir + "/stdin\"\necho \"$@\" > \"" + dir + "/args\"\ncat <<'EOF'\n" + result + "\nEOF\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func TestAskClaudeCLI(t *testing.T) {
	bin, dir := fakeClaude(t, `{"is_error":false,"result":" - L2 Hello ","total_cost_usd":0.0002,
"usage":{"input_tokens":2,"cache_creation_input_tokens":630,"cache_read_input_tokens":100,"output_tokens":31}}`)
	r := &config.Resolved{
		Mode:     config.Mode{Instructions: "be brief"},
		Model:    config.Model{ID: "haiku"},
		Provider: config.Provider{Type: config.TypeClaudeCLI, Bin: bin},
	}
	res, err := Ask(context.Background(), r, "which?", []File{{Path: "a.go", Content: "package a\n"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer != "- L2 Hello" || res.CostUSD != 0.0002 || !res.ClaudePlan {
		t.Fatalf("got %+v", res)
	}
	if res.Usage.PromptTokens != 732 || res.Usage.CachedTokens != 100 || res.Usage.CompletionTokens != 31 {
		t.Fatalf("usage %+v", res.Usage)
	}
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if !strings.Contains(string(stdin), "L1|package a") {
		t.Fatalf("prompt not on stdin: %q", stdin)
	}
	for _, want := range []string{"--model haiku", "--system-prompt be brief", "--setting-sources"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("args %q lack %q", args, want)
		}
	}
}

func TestAskClaudeCLIError(t *testing.T) {
	bin, _ := fakeClaude(t, `{"is_error":true,"subtype":"error_during_execution","result":"rate limited"}`)
	r := &config.Resolved{Model: config.Model{ID: "haiku"}, Provider: config.Provider{Type: config.TypeClaudeCLI, Bin: bin}}
	_, err := Ask(context.Background(), r, "q", []File{{Path: "f", Content: "x"}})
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("got %v", err)
	}
}

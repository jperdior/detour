package trial

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallKeepsSettingsAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	orig := `{"model":"opus","permissions":{"allow":["mcp__x"]},"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"mine"}]}]}}`
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(orig), 0o600)
	for i := 0; i < 2; i++ {
		if _, err := Install(dir, "/bin/detour"); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	var s map[string]any
	json.Unmarshal(b, &s)
	if s["model"] != "opus" {
		t.Fatal("other settings must stay")
	}
	pre := s["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(pre) != 2 || !strings.Contains(string(b), `"mine"`) {
		t.Fatalf("existing hook kept, ours added once: %s", b)
	}
	if n := strings.Count(string(b), "detour hook claude"); n != 3 {
		t.Fatalf("three trial hooks, got %d: %s", n, b)
	}
	if n := strings.Count(string(b), readPermission); n != 1 {
		t.Fatalf("permission once, got %d", n)
	}
	if bk, _ := os.ReadFile(filepath.Join(dir, "settings.json.before-detour")); string(bk) != orig {
		t.Fatal("backup must hold the original")
	}
	Uninstall(dir)
	b, _ = os.ReadFile(filepath.Join(dir, "settings.json"))
	if strings.Contains(string(b), "detour") || !strings.Contains(string(b), `"mine"`) || !strings.Contains(string(b), "mcp__x") {
		t.Fatalf("uninstall removes only ours: %s", b)
	}
}

func TestAddProjectsUsesMainCheckout(t *testing.T) {
	repo := gitRepo(t)
	cfgPath := filepath.Join(t.TempDir(), "trial.yaml")
	cfg, err := AddProjects(cfgPath, []string{repo, repo})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || len(cfg.Projects) != 1 || cfg.DelegateModel != "haiku" || len(cfg.Arms) != 2 {
		t.Fatalf("config: %+v", cfg)
	}
	back, _ := LoadConfig(cfgPath)
	if len(back.Projects) != 1 || realPath(back.Projects[0].Repo) != realPath(repo) {
		t.Fatalf("round trip: %+v", back)
	}
	if _, err := AddProjects(cfgPath, []string{t.TempDir()}); err == nil {
		t.Fatal("a non-git directory is refused")
	}
}

func TestShareRoundTripIsAnonymous(t *testing.T) {
	rows := []Row{
		{ID: "secret-session", Arm: ArmOff, Project: "acme-billing", Profile: "work", CostUSD: 1},
		{ID: "s2", Arm: ArmDetour, Project: "acme-billing", Profile: "work", CostUSD: 0.5},
		{ID: "s3", Arm: ArmDetour, Project: "side-project", Profile: "personal", CostUSD: 0.7},
	}
	sh := NewShared(Config{Arms: []string{ArmOff, ArmDetour}, DelegateModel: "haiku"}, rows, "abc123", "0.3.0")
	body, err := sh.Body()
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"acme", "side-project", "secret-session", "personal", "work"} {
		if strings.Contains(body, leak) {
			t.Fatalf("body leaks %q:\n%s", leak, body)
		}
	}
	back, err := ParseBody("some text the tester added\n" + body)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Sessions) != 3 || back.Sessions[0].Project != "project-1" || back.Sessions[2].Project != "project-2" || back.Contributor != "abc123" {
		t.Fatalf("parsed: %+v", back.Sessions)
	}
	if _, err := ParseBody("random issue"); err == nil {
		t.Fatal("other issues are ignored")
	}
}

func TestUninstallFindsHooksWhateverTheBinaryIsCalled(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"model":"opus"}`), 0o600)
	Install(dir, "/opt/tools/detour-dev")
	Uninstall(dir)
	b, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if string(b) != "{\n  \"model\": \"opus\"\n}\n" {
		t.Fatalf("settings must be back to the original: %s", b)
	}
}

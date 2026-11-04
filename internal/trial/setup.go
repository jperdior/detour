package trial

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Hook commands the trial installs, relative to the detour binary.
var hookEvents = []struct {
	event, matcher, args string
	timeout              int
}{
	{"SessionStart", "", "hook claude-session-start", 10},
	{"PreToolUse", "Read|Bash", "hook claude --trial", 10},
	{"SessionEnd", "", "hook claude-session-end", 30},
}

const readPermission = "Bash(detour read:*)"

// Install adds the trial hooks and the `detour read` permission to the
// settings.json of a Claude Code config dir, keeping everything else. The
// first install saves settings.json.before-detour. Installing twice changes
// nothing.
func Install(configDir, bin string) (string, error) {
	path := filepath.Join(configDir, "settings.json")
	s, err := readSettings(path)
	if err != nil {
		return path, err
	}
	backup := path + ".before-detour"
	if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
		if b, err := os.ReadFile(path); err == nil {
			if err := os.WriteFile(backup, b, 0o600); err != nil {
				return path, err
			}
		}
	}
	hooks, _ := s["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for _, h := range hookEvents {
		cmd := bin + " " + h.args
		list, _ := hooks[h.event].([]any)
		if !hasCommand(list, cmd) {
			entry := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": cmd, "timeout": h.timeout}}}
			if h.matcher != "" {
				entry["matcher"] = h.matcher
			}
			list = append(list, entry)
		}
		hooks[h.event] = list
	}
	s["hooks"] = hooks
	perms, _ := s["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}
	allow, _ := perms["allow"].([]any)
	if !containsString(allow, readPermission) {
		allow = append(allow, readPermission)
	}
	perms["allow"] = allow
	s["permissions"] = perms
	return path, writeSettings(path, s)
}

// Uninstall removes every detour trial hook and the permission from a
// config dir's settings.json. Other hooks and settings stay.
func Uninstall(configDir string) (string, error) {
	path := filepath.Join(configDir, "settings.json")
	s, err := readSettings(path)
	if err != nil {
		return path, err
	}
	if hooks, ok := s["hooks"].(map[string]any); ok {
		for event, v := range hooks {
			list, _ := v.([]any)
			var kept []any
			for _, e := range list {
				if !isTrialEntry(e) {
					kept = append(kept, e)
				}
			}
			if len(kept) == 0 {
				delete(hooks, event)
			} else {
				hooks[event] = kept
			}
		}
		if len(hooks) == 0 {
			delete(s, "hooks")
		}
	}
	if perms, ok := s["permissions"].(map[string]any); ok {
		if allow, ok := perms["allow"].([]any); ok {
			var kept []any
			for _, a := range allow {
				if a != readPermission {
					kept = append(kept, a)
				}
			}
			if len(kept) == 0 {
				delete(perms, "allow")
			} else {
				perms["allow"] = kept
			}
		}
		if len(perms) == 0 {
			delete(s, "permissions")
		}
	}
	return path, writeSettings(path, s)
}

func isTrialEntry(e any) bool {
	m, _ := e.(map[string]any)
	hs, _ := m["hooks"].([]any)
	for _, h := range hs {
		hm, _ := h.(map[string]any)
		cmd, _ := hm["command"].(string)
		for _, ev := range hookEvents {
			if strings.HasSuffix(cmd, " "+ev.args) {
				return true
			}
		}
	}
	return false
}

func hasCommand(list []any, cmd string) bool {
	for _, e := range list {
		m, _ := e.(map[string]any)
		hs, _ := m["hooks"].([]any)
		for _, h := range hs {
			if hm, _ := h.(map[string]any); hm["command"] == cmd {
				return true
			}
		}
	}
	return false
}

func containsString(xs []any, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func readSettings(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	s := map[string]any{}
	if len(strings.TrimSpace(string(b))) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON, leaving it alone: %w", path, err)
	}
	return s, nil
}

func writeSettings(path string, s map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// AddProjects writes the trial config with these repositories added
// (worktrees count automatically), creating it with defaults if needed.
// Names come from the repository's directory; a clash gets a suffix.
func AddProjects(path string, repos []string) (Config, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		return cfg, err
	}
	fresh := len(cfg.Projects) == 0 && !cfg.Enabled
	if fresh {
		cfg = Config{Enabled: true, Target: 100, Arms: []string{ArmOff, ArmDetour}, DelegateModel: "haiku"}
	}
	have := map[string]bool{}
	names := map[string]bool{}
	for _, p := range cfg.Projects {
		have[realPath(p.Repo)] = true
		names[p.Name] = true
	}
	for _, r := range repos {
		top, err := exec.Command("git", "-C", expandHome(r), "rev-parse", "--show-toplevel").Output()
		if err != nil {
			return cfg, fmt.Errorf("%s is not a git repository", r)
		}
		root := strings.TrimSpace(string(top))
		// A worktree's toplevel is the worktree; the trial matches by
		// repository, so store the main checkout.
		if common, err := exec.Command("git", "-C", root, "rev-parse", "--path-format=absolute", "--git-common-dir").Output(); err == nil {
			if c := strings.TrimSpace(string(common)); filepath.Base(c) == ".git" {
				root = filepath.Dir(c)
			}
		}
		if have[realPath(root)] {
			continue
		}
		name := filepath.Base(root)
		for i := 2; names[name]; i++ {
			name = fmt.Sprintf("%s-%d", filepath.Base(root), i)
		}
		cfg.Projects = append(cfg.Projects, Project{Name: name, Repo: root})
		have[realPath(root)], names[name] = true, true
	}
	sort.Slice(cfg.Projects, func(i, j int) bool { return cfg.Projects[i].Name < cfg.Projects[j].Name })
	cfg.Enabled = true
	return cfg, SaveConfig(path, cfg)
}

// SaveConfig writes the trial config.
func SaveConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	head := "# detour field trial. Each Claude Code session in these projects is randomly\n" +
		"# assigned \"off\" (logs only) or \"detour\". Set enabled: false to pause it.\n" +
		"# Delegation in trial sessions always goes through Claude Code (Haiku).\n"
	return os.WriteFile(path, append([]byte(head), b...), 0o644)
}

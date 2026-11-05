// Package trial runs detour as a randomized field trial in real use. Each
// Claude Code session that starts inside one of the configured projects is
// assigned an arm at random: "off" (the hook only logs what it would have
// done) or "detour" (the hook blocks and the agent delegates). At the end of
// each session its transcript is measured, so the arms can be compared on
// list-price cost per session once enough sessions are in.
package trial

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jperdior/detour/internal/transcript"
)

// Arms.
const (
	ArmOff    = "off"    // shadow: log what the hook would do, change nothing
	ArmDetour = "detour" // full detour: block, budget, delegate to Haiku
	ArmBlock  = "block"  // block only, no delegation
)

// Config is the trial's configuration file.
type Config struct {
	Enabled bool     `yaml:"enabled"`
	Target  int      `yaml:"target"` // measured sessions before notifying
	Arms    []string `yaml:"arms"`
	// DelegateModel is the model `detour read` must use in trial sessions,
	// whatever the flags or environment say. It must run through Claude Code.
	DelegateModel string    `yaml:"delegate_model"`
	Projects      []Project `yaml:"projects"`
	// ConfigDirs are the Claude Code config dirs the hooks were installed
	// in, so uninstall can clean every profile.
	ConfigDirs []string `yaml:"config_dirs,omitempty"`
}

// Project is a repository in the trial. Its worktrees count too.
type Project struct {
	Name string `yaml:"name"`
	Repo string `yaml:"repo"`
}

// ConfigPath is DETOUR_TRIAL_CONFIG or ~/.config/detour/trial.yaml.
func ConfigPath() string {
	if p := os.Getenv("DETOUR_TRIAL_CONFIG"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "detour", "trial.yaml")
}

// Dir is DETOUR_TRIAL_DIR or ~/.local/state/detour/trial.
func Dir() string {
	if p := os.Getenv("DETOUR_TRIAL_DIR"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "detour", "trial")
}

// LoadConfig reads the config. A missing file means no trial.
func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if c.Target <= 0 {
		c.Target = 100
	}
	if len(c.Arms) == 0 {
		c.Arms = []string{ArmOff, ArmDetour}
	}
	for _, a := range c.Arms {
		if a != ArmOff && a != ArmDetour && a != ArmBlock {
			return c, fmt.Errorf("%s: unknown arm %q", path, a)
		}
	}
	if c.DelegateModel == "" {
		c.DelegateModel = "haiku"
	}
	for i, p := range c.Projects {
		c.Projects[i].Repo = expandHome(p.Repo)
	}
	return c, nil
}

// Session is one session's record.
type Session struct {
	ID      string `json:"session_id"`
	Project string `json:"project,omitempty"` // empty: not in the trial
	Arm     string `json:"arm,omitempty"`
	Origin  string `json:"origin,omitempty"` // interactive, headless, loop
	// Profile is the Claude Code config dir in use: "default" for ~/.claude,
	// else the dir name without ".claude-" (two accounts, two profiles).
	Profile        string `json:"profile,omitempty"`
	Source         string `json:"source,omitempty"` // SessionStart source, or "lazy"
	Cwd            string `json:"cwd,omitempty"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	// Excluded says why a session in a trial project is left out, e.g.
	// "joined-late": it was already running when the trial reached it.
	Excluded string    `json:"excluded,omitempty"`
	Started  time.Time `json:"started"`
	Ended    time.Time `json:"ended,omitempty"`
	// EndReason is "session-end" (the hook ran) or "stale" (no transcript
	// activity for StaleAfter: Claude Code died before its hook ran).
	EndReason string `json:"end_reason,omitempty"`
	// Usage is measured at SessionEnd; the report measures again from the
	// transcript, which may have grown if the session was resumed.
	Usage *transcript.Summary `json:"usage,omitempty"`
}

// InTrial is true for sessions in a trial project.
func (s *Session) InTrial() bool { return s != nil && s.Project != "" }

func sessionPath(dir, id string) string {
	return filepath.Join(dir, "sessions", safeName(id)+".json")
}

// Lookup returns the stored session, or nil.
func Lookup(dir, id string) *Session {
	if id == "" {
		return nil
	}
	b, err := os.ReadFile(sessionPath(dir, id))
	if err != nil {
		return nil
	}
	var s Session
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	return &s
}

// Save writes the session record atomically.
func (s *Session) Save(dir string) error {
	p := sessionPath(dir, s.ID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// All returns every stored session in the trial.
func All(dir string) ([]*Session, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "sessions", "*.json"))
	if err != nil {
		return nil, err
	}
	var out []*Session
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var s Session
		if json.Unmarshal(b, &s) == nil && s.InTrial() {
			out = append(out, &s)
		}
	}
	return out, nil
}

// Begin returns the session's record, assigning an arm on first sight. A
// session outside the trial projects is stored with no project, so later
// hook calls don't run git again.
func Begin(cfg Config, dir, id, cwd, source, transcriptPath string) (*Session, error) {
	if s := Lookup(dir, id); s != nil {
		if transcriptPath != "" && s.TranscriptPath == "" && s.InTrial() {
			s.TranscriptPath = transcriptPath
			_ = s.Save(dir)
		}
		return s, nil
	}
	s := &Session{ID: id, Cwd: cwd, Source: source, TranscriptPath: transcriptPath, Started: time.Now().UTC()}
	if name, ok := MatchProject(cfg, cwd); ok && source != "startup" && source != "clear" && joinedLate(transcriptPath) {
		// The session ran without the trial until now (lazy: it started before
		// the hooks; resume: an older conversation): it would be in neither arm.
		s.Excluded = "joined-late: " + name
		s.Cwd, s.TranscriptPath = "", ""
	} else if ok && Complete(cfg, dir) {
		// The trial has its sessions: no more draws, detour stays off.
		s.Excluded = "trial-complete: " + name
		s.Cwd, s.TranscriptPath = "", ""
	} else if ok {
		s.Project = name
		s.Arm = pick(cfg.Arms)
		s.Origin = origin()
		s.Profile = profile()
	} else {
		s.Cwd, s.TranscriptPath = "", ""
	}
	return s, s.Save(dir)
}

// Complete reports whether the trial has reached its target. From then on
// new sessions are not assigned an arm; sessions already running keep
// theirs until they end.
func Complete(cfg Config, dir string) bool {
	return cfg.Target > 0 && Measured(dir) >= cfg.Target
}

// MatchProject finds the trial project cwd belongs to, through git's common
// directory, so linked worktrees anywhere on disk count as their repository.
func MatchProject(cfg Config, cwd string) (string, bool) {
	if cwd == "" || len(cfg.Projects) == 0 {
		return "", false
	}
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return "", false
	}
	common := realPath(strings.TrimSpace(string(out)))
	for _, p := range cfg.Projects {
		if realPath(filepath.Join(p.Repo, ".git")) == common {
			return p.Name, true
		}
	}
	return "", false
}

// joinedLate is true when the transcript shows the session had been running
// for more than two minutes before the trial first saw it.
func joinedLate(transcriptPath string) bool {
	if transcriptPath == "" {
		return false
	}
	u, err := transcript.Summarize(transcriptPath)
	return err == nil && !u.First.IsZero() && time.Since(u.First) > 2*time.Minute
}

func profile() string {
	d := os.Getenv("CLAUDE_CONFIG_DIR")
	if d == "" {
		return "default"
	}
	name := filepath.Base(filepath.Clean(d))
	if n := strings.TrimPrefix(name, ".claude-"); n != name && n != "" {
		return n
	}
	return name
}

// OriginInteractive marks a session a person drives; every other origin has
// a driver that reads its denials.
const OriginInteractive = "interactive"

func origin() string {
	switch {
	case os.Getenv("DELIVERY_LOOP_ORIGIN") != "":
		return "loop"
	case os.Getenv("CLAUDE_CODE_ENTRYPOINT") == "cli":
		return OriginInteractive
	}
	return "headless"
}

func pick(arms []string) string {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(arms))))
	if err != nil {
		return arms[time.Now().UnixNano()%int64(len(arms))]
	}
	return arms[n.Int64()]
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

func safeName(id string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == '.' {
			return '_'
		}
		return r
	}, id)
}

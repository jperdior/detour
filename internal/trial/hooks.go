package trial

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jperdior/detour/internal/hook"
	"github.com/jperdior/detour/internal/policy"
	"github.com/jperdior/detour/internal/transcript"
)

type hookInput struct {
	SessionID      string `json:"session_id"`
	Cwd            string `json:"cwd"`
	TranscriptPath string `json:"transcript_path"`
	Source         string `json:"source"`
}

// guidance replaces the detour-read skill in the detour arm. A skill would be
// loaded in every session, control arm included, so it is injected at
// SessionStart instead and only where detour is on.
const guidance = `This session reads large files through detour.

Large files (over ~350 lines) are expensive to read into your context. To understand one, ask a cheap model about it instead:

  detour read -q "<specific question>" path/to/file [more files...]

- Put every file the question needs in one call.
- If you already know which lines matter (from Grep, or an earlier answer), send only that range: path/to/file:736-1180.
- Targeted reads of a large file are capped per session; the denial message says how many lines are left. Once blocked, don't try smaller chunks: delegate.
- Ask a specific question that names the functions, types or behaviour you need.
- The answer cites lines as file:L123. To see exact code, Read only that range with offset and limit.
- To edit a file, read the exact section you will change with offset and limit. Don't delegate subtle debugging (concurrency, security, off-by-one): read the relevant ranges yourself.
- If detour read fails, fall back to Grep and targeted Reads.`

// SessionStart assigns the session an arm and, in the detour arm, gives the
// agent the guidance. It never fails the session.
func SessionStart(in io.Reader, out io.Writer, cfg Config, dir string) error {
	if !cfg.Enabled {
		return nil
	}
	var hi hookInput
	if json.NewDecoder(in).Decode(&hi) != nil || hi.SessionID == "" {
		return nil
	}
	s, err := Begin(cfg, dir, hi.SessionID, hi.Cwd, hi.Source, hi.TranscriptPath)
	if err != nil || !s.InTrial() || s.Arm != ArmDetour {
		return nil
	}
	var o struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	o.HookSpecificOutput.HookEventName = "SessionStart"
	o.HookSpecificOutput.AdditionalContext = guidance
	return json.NewEncoder(out).Encode(o)
}

// PreToolUse applies the session's arm. Sessions outside the trial pass
// through untouched. A session first seen here (SessionStart did not run)
// is assigned now.
func PreToolUse(in io.Reader, out io.Writer, cfg Config, dir, ledgerPath string, base policy.Config) error {
	if !cfg.Enabled {
		return nil
	}
	raw, err := io.ReadAll(in)
	if err != nil {
		return nil
	}
	var hi hookInput
	if json.Unmarshal(raw, &hi) != nil || hi.SessionID == "" {
		return nil
	}
	s, err := Begin(cfg, dir, hi.SessionID, hi.Cwd, "lazy", hi.TranscriptPath)
	if err != nil || !s.InTrial() {
		return nil
	}
	pc := base
	switch s.Arm {
	case ArmOff:
		pc.Shadow = true
	case ArmBlock:
		pc.BlockOnly = true
	}
	return hook.Claude(bytes.NewReader(raw), out, pc, ledgerPath)
}

// SessionEnd measures the session from its transcript and, once the trial
// has Target measured sessions, notifies the user one time.
func SessionEnd(in io.Reader, cfg Config, dir string, notify func(title, msg string)) error {
	if !cfg.Enabled {
		return nil
	}
	var hi hookInput
	if json.NewDecoder(in).Decode(&hi) != nil || hi.SessionID == "" {
		return nil
	}
	s := Lookup(dir, hi.SessionID)
	if !s.InTrial() {
		return nil
	}
	if hi.TranscriptPath != "" {
		s.TranscriptPath = hi.TranscriptPath
	}
	s.Ended = time.Now().UTC()
	if s.TranscriptPath != "" {
		if u, err := transcript.Summarize(s.TranscriptPath); err == nil {
			s.Usage = &u
		}
	}
	if err := s.Save(dir); err != nil {
		return nil
	}
	n := Measured(dir)
	marker := filepath.Join(dir, "notified")
	if n >= cfg.Target {
		if _, err := os.Stat(marker); os.IsNotExist(err) {
			if notify != nil {
				notify("detour", fmt.Sprintf("%d sesiones medidas: ya se pueden comparar los resultados (detour trial report)", n))
			}
			_ = os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
		}
	}
	return nil
}

// Measured counts trial sessions that ended with at least one API call.
func Measured(dir string) int {
	ss, _ := All(dir)
	n := 0
	for _, s := range ss {
		if !s.Ended.IsZero() && s.Usage != nil && s.Usage.Calls() > 0 {
			n++
		}
	}
	return n
}

// NotifyMac shows a macOS notification; elsewhere it does nothing.
func NotifyMac(title, msg string) {
	script := fmt.Sprintf("display notification %q with title %q", msg, title)
	_ = exec.Command("osascript", "-e", script).Run()
}

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

// StaleAfter is how long an open session's transcript may go unchanged
// before the session counts as ended. Closing a terminal tab can kill
// Claude Code before its SessionEnd hook runs.
const StaleAfter = 2 * time.Hour

// Sweep ends open sessions that have gone stale and measures them. A stale
// session that is later resumed is measured in full again by the report.
func Sweep(dir string, now time.Time) int {
	ss, _ := All(dir)
	n := 0
	for _, s := range ss {
		if !s.Ended.IsZero() {
			continue
		}
		last := s.Started
		if s.TranscriptPath != "" {
			if st, err := os.Stat(s.TranscriptPath); err == nil {
				last = st.ModTime()
			}
		}
		if now.Sub(last) < StaleAfter {
			continue
		}
		s.Ended, s.EndReason = last.UTC(), "stale"
		if s.TranscriptPath != "" {
			if u, err := transcript.Summarize(s.TranscriptPath); err == nil {
				s.Usage = &u
			}
		}
		if s.Save(dir) == nil {
			n++
		}
	}
	return n
}

// notifyAtTarget notifies once when the trial reaches its target.
func notifyAtTarget(cfg Config, dir string, notify func(title, msg string)) {
	n := Measured(dir)
	marker := filepath.Join(dir, "notified")
	if n < cfg.Target {
		return
	}
	if _, err := os.Stat(marker); os.IsNotExist(err) {
		if notify != nil {
			notify("detour", fmt.Sprintf("%d sessions measured. See your results with: detour trial report. Share them with: detour trial share", n))
		}
		_ = os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
	}
}

// SessionStart assigns the session an arm and, in the detour arm, gives the
// agent the guidance. It also ends stale sessions. It never fails the
// session.
func SessionStart(in io.Reader, out io.Writer, cfg Config, dir string, notify func(title, msg string)) error {
	if !cfg.Enabled {
		return nil
	}
	var hi hookInput
	if json.NewDecoder(in).Decode(&hi) != nil || hi.SessionID == "" {
		return nil
	}
	if Sweep(dir, time.Now()) > 0 {
		notifyAtTarget(cfg, dir, notify)
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
	s.Ended, s.EndReason = time.Now().UTC(), "session-end"
	if s.TranscriptPath != "" {
		if u, err := transcript.Summarize(s.TranscriptPath); err == nil {
			s.Usage = &u
		}
	}
	if err := s.Save(dir); err != nil {
		return nil
	}
	Sweep(dir, time.Now())
	notifyAtTarget(cfg, dir, notify)
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

// NotifyMac shows a desktop notification: macOS, or notify-send on Linux.
func NotifyMac(title, msg string) {
	if _, err := exec.LookPath("osascript"); err == nil {
		_ = exec.Command("osascript", "-e", fmt.Sprintf("display notification %q with title %q", msg, title)).Run()
		return
	}
	if _, err := exec.LookPath("notify-send"); err == nil {
		_ = exec.Command("notify-send", title, msg).Run()
	}
}

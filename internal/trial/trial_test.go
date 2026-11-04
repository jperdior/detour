package trial

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jperdior/detour/internal/ledger"
	"github.com/jperdior/detour/internal/policy"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"commit", "-q", "--allow-empty", "-m", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func TestMatchProjectCountsWorktreesAnywhere(t *testing.T) {
	repo := gitRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", "--detach", wt).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	os.MkdirAll(filepath.Join(repo, "sub"), 0o755)
	cfg := Config{Projects: []Project{{Name: "p", Repo: repo}}}
	for _, cwd := range []string{repo, filepath.Join(repo, "sub"), wt} {
		if name, ok := MatchProject(cfg, cwd); !ok || name != "p" {
			t.Fatalf("%s should be in project p", cwd)
		}
	}
	if _, ok := MatchProject(cfg, gitRepo(t)); ok {
		t.Fatal("another repository is not in the trial")
	}
	if _, ok := MatchProject(cfg, t.TempDir()); ok {
		t.Fatal("a non-git directory is not in the trial")
	}
}

func payload(session, cwd, tool, input string) string {
	return fmt.Sprintf(`{"session_id":%q,"cwd":%q,"hook_event_name":"PreToolUse","tool_name":%q,"tool_input":%s}`, session, cwd, tool, input)
}

func TestArms(t *testing.T) {
	repo := gitRepo(t)
	os.WriteFile(filepath.Join(repo, "big.go"), []byte(strings.Repeat("x\n", 600)), 0o644)
	state := t.TempDir()
	lp := filepath.Join(state, "ledger.jsonl")
	base := policy.Config{MinLines: 350, FileBudget: 350}
	read := `{"file_path":"big.go"}`

	// Off arm: never denies, logs a shadow block.
	off := Config{Enabled: true, Arms: []string{ArmOff}, Projects: []Project{{Name: "p", Repo: repo}}}
	var out bytes.Buffer
	if err := SessionStart(strings.NewReader(`{"session_id":"s-off","cwd":"`+repo+`","source":"startup"}`), &out, off, state); err != nil || out.Len() != 0 {
		t.Fatalf("off arm gets no guidance: %q %v", out.String(), err)
	}
	PreToolUse(strings.NewReader(payload("s-off", repo, "Read", read)), &out, off, state, lp, base)
	if out.Len() != 0 {
		t.Fatalf("off arm must not deny: %q", out.String())
	}

	// Detour arm: guidance at start, denial on a whole read.
	on := off
	on.Arms = []string{ArmDetour}
	SessionStart(strings.NewReader(`{"session_id":"s-on","cwd":"`+repo+`","source":"startup"}`), &out, on, state)
	if !strings.Contains(out.String(), "additionalContext") || !strings.Contains(out.String(), "detour read") {
		t.Fatalf("detour arm needs guidance: %q", out.String())
	}
	out.Reset()
	PreToolUse(strings.NewReader(payload("s-on", repo, "Read", read)), &out, on, state, lp, base)
	if !strings.Contains(out.String(), `"deny"`) {
		t.Fatalf("detour arm must deny a whole read: %q", out.String())
	}

	// The stored arm wins over the config: s-off stays off.
	out.Reset()
	PreToolUse(strings.NewReader(payload("s-off", repo, "Read", read)), &out, on, state, lp, base)
	if out.Len() != 0 {
		t.Fatal("a session keeps its arm")
	}

	// Outside the projects nothing happens, even in the detour arm.
	elsewhere := t.TempDir()
	os.WriteFile(filepath.Join(elsewhere, "big.go"), []byte(strings.Repeat("x\n", 600)), 0o644)
	out.Reset()
	PreToolUse(strings.NewReader(payload("s-out", elsewhere, "Read", read)), &out, on, state, lp, base)
	if out.Len() != 0 || Lookup(state, "s-out").InTrial() {
		t.Fatal("sessions outside the trial pass through")
	}

	// Disabled: nothing at all.
	out.Reset()
	PreToolUse(strings.NewReader(payload("s-new", repo, "Read", read)), &out, Config{}, state, lp, base)
	if out.Len() != 0 || Lookup(state, "s-new") != nil {
		t.Fatal("a disabled trial does nothing")
	}

	ev, _ := ledger.Read(lp)
	kinds := map[string]string{}
	for _, e := range ev {
		kinds[e.SessionID] += e.Kind + " "
	}
	if kinds["s-off"] != "shadow-block shadow-block " || kinds["s-on"] != "block " {
		t.Fatalf("ledger: %v", kinds)
	}
}

func TestSessionEndMeasuresAndNotifiesOnce(t *testing.T) {
	repo := gitRepo(t)
	state := t.TempDir()
	tr := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(tr, []byte(`{"type":"assistant","timestamp":"2026-11-04T10:00:00Z","message":{"id":"m","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":1000000,"output_tokens":0}}}`+"\n"), 0o644)
	cfg := Config{Enabled: true, Target: 2, Arms: []string{ArmOff}, Projects: []Project{{Name: "p", Repo: repo}}}
	var notes []string
	notify := func(_, msg string) { notes = append(notes, msg) }
	for _, id := range []string{"a", "b", "c"} {
		start := fmt.Sprintf(`{"session_id":%q,"cwd":%q,"source":"startup","transcript_path":%q}`, id, repo, tr)
		SessionStart(strings.NewReader(start), &bytes.Buffer{}, cfg, state)
		SessionEnd(strings.NewReader(fmt.Sprintf(`{"session_id":%q,"transcript_path":%q}`, id, tr)), cfg, state, notify)
	}
	if s := Lookup(state, "a"); s.Usage == nil || s.Usage.CostUSD != 4 {
		t.Fatalf("1M input tokens of Opus 5.5 cost $4: %+v", s.Usage)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "2 sesiones") {
		t.Fatalf("notify exactly once at the target: %v", notes)
	}
	rows, _ := Rows(state, nil)
	if len(rows) != 3 || rows[0].Arm != ArmOff {
		t.Fatalf("rows: %+v", rows)
	}
	var b bytes.Buffer
	WriteReport(&b, cfg, rows)
	if !strings.Contains(b.String(), "| off | 3 | 4.000 |") {
		t.Fatalf("report:\n%s", b.String())
	}
}

func TestLateJoinerIsExcluded(t *testing.T) {
	repo := gitRepo(t)
	state := t.TempDir()
	tr := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(tr, []byte(`{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"content":"hi"}}`+"\n"), 0o644)
	cfg := Config{Enabled: true, Arms: []string{ArmDetour}, Projects: []Project{{Name: "p", Repo: repo}}}
	s, _ := Begin(cfg, state, "old", repo, "lazy", tr)
	if s.InTrial() || !strings.HasPrefix(s.Excluded, "joined-late") {
		t.Fatalf("a session already running must stay out: %+v", s)
	}
	// A session first seen lazily right after it started is fine.
	fresh := filepath.Join(t.TempDir(), "f.jsonl")
	os.WriteFile(fresh, []byte(fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":"hi"}}`+"\n", time.Now().UTC().Format(time.RFC3339))), 0o644)
	if s, _ := Begin(cfg, state, "new", repo, "lazy", fresh); !s.InTrial() {
		t.Fatalf("a fresh session joins: %+v", s)
	}
}

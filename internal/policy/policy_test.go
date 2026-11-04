package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLines(t *testing.T, dir, name string, n int) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(strings.Repeat("line\n", n)), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCheckRead(t *testing.T) {
	dir := t.TempDir()
	writeLines(t, dir, "big.go", 500)
	writeLines(t, dir, "small.go", 100)
	c := Config{MinLines: 350}

	cases := []struct {
		name          string
		path          string
		offset, limit int
		want          Kind
	}{
		{"small file passes", "small.go", 0, 0, Allow},
		{"missing file passes", "nope.go", 0, 0, Allow},
		{"whole read of big file is blocked", "big.go", 0, 0, Block},
		{"targeted read passes as partial", "big.go", 100, 50, Partial},
		{"limit above threshold is a whole read", "big.go", 1, 2000, Block},
		{"absolute path works", filepath.Join(dir, "big.go"), 0, 0, Block},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := c.CheckRead(tc.path, tc.offset, tc.limit, dir)
			if d.Kind != tc.want {
				t.Fatalf("got %s, want %s", d.Kind, tc.want)
			}
		})
	}
}

func TestPartialReportsRequestedLines(t *testing.T) {
	dir := t.TempDir()
	writeLines(t, dir, "big.go", 500)
	c := Config{MinLines: 350}
	if d := c.CheckRead("big.go", 10, 120, dir); d.Lines != 120 {
		t.Fatalf("Read lines = %d", d.Lines)
	}
	if d := c.CheckBash("head -n 40 big.go", dir); d.Lines != 40 {
		t.Fatalf("head lines = %d", d.Lines)
	}
}

func TestCheckReadReasonTellsModelWhatToRun(t *testing.T) {
	dir := t.TempDir()
	writeLines(t, dir, "big.go", 400)
	d := Config{MinLines: 350}.CheckRead("big.go", 0, 0, dir)
	if !strings.Contains(d.Reason, `detour read -q "<your specific question>" big.go`) {
		t.Fatalf("reason does not show the command:\n%s", d.Reason)
	}
	if d.Files[0].Lines != 400 {
		t.Fatalf("lines = %d", d.Files[0].Lines)
	}
}

func TestCheckBash(t *testing.T) {
	dir := t.TempDir()
	writeLines(t, dir, "big.go", 500)
	writeLines(t, dir, "small.go", 10)
	c := Config{MinLines: 350}

	cases := []struct {
		cmd  string
		want Kind
	}{
		{"cat big.go", Block},
		{"cat small.go", Allow},
		{"cat small.go big.go", Block},
		{"/bin/cat -n big.go", Block},
		{"less 'big.go'", Block},
		{"head big.go", Partial}, // default 10 lines
		{"head -n 50 big.go", Partial},
		{"head -n 2000 big.go", Block},
		{"head -2000 big.go", Block},
		{"tail -n400 big.go", Block},
		{"tail --lines=20 big.go", Partial},
		{"cat big.go | grep foo", Allow}, // compound commands pass in v0
		{"grep -n foo big.go", Allow},
		{"ls -la", Allow},
		{"", Allow},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			if got := c.CheckBash(tc.cmd, dir).Kind; got != tc.want {
				t.Fatalf("%q: got %s, want %s", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestCountLinesWithoutTrailingNewline(t *testing.T) {
	n, err := countLines(strings.NewReader("a\nb\nc"))
	if err != nil || n != 3 {
		t.Fatalf("got %d, %v", n, err)
	}
	n, _ = countLines(strings.NewReader(""))
	if n != 0 {
		t.Fatalf("empty: got %d", n)
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("DETOUR_MIN_LINES", "120")
	if c := FromEnv(); c.MinLines != 120 || c.FileBudget != 120 {
		t.Fatalf("env override ignored: %+v", c)
	}
	t.Setenv("DETOUR_FILE_BUDGET", "0")
	if FromEnv().FileBudget != 0 {
		t.Fatal("budget 0 should disable it")
	}
	t.Setenv("DETOUR_MIN_LINES", "garbage")
	if FromEnv().MinLines != DefaultMinLines {
		t.Fatal("bad value should fall back to default")
	}
}

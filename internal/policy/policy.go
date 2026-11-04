// Package policy decides whether an agent may read a file itself or must
// delegate the reading to a cheaper model. It is agent-agnostic: adapters
// translate each agent's tool call into CheckRead or CheckBash.
package policy

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultMinLines is the line count above which a whole-file read is blocked.
// It is the same default as Spotify's shunt; tune it with benchmark data.
const DefaultMinLines = 350

// Config holds the policy knobs.
type Config struct {
	// MinLines: files with more lines than this must be delegated.
	MinLines int
	// FileBudget: lines of one large file the agent may read in targeted
	// chunks per session before further chunks are blocked too. Without it a
	// blocked agent simply pages through the file. 0 disables the budget.
	FileBudget int
}

// FromEnv returns the default config, overridden by DETOUR_MIN_LINES and
// DETOUR_FILE_BUDGET. The budget defaults to MinLines.
func FromEnv() Config {
	c := Config{MinLines: DefaultMinLines}
	if v, err := strconv.Atoi(os.Getenv("DETOUR_MIN_LINES")); err == nil && v > 0 {
		c.MinLines = v
	}
	c.FileBudget = c.MinLines
	if v, err := strconv.Atoi(os.Getenv("DETOUR_FILE_BUDGET")); err == nil && v >= 0 {
		c.FileBudget = v
	}
	return c
}

// File describes a file the policy looked at.
type File struct {
	Path  string `json:"path"`
	Lines int    `json:"lines"`
	Bytes int64  `json:"bytes"`
}

// EstTokens is a rough token estimate (bytes / 4). The benchmark reads real
// billed tokens from the agent; this number is only for the ledger.
func (f File) EstTokens() int { return int(f.Bytes / 4) }

// Kind of decision.
type Kind string

const (
	Allow   Kind = "allow"   // nothing to do
	Partial Kind = "partial" // large file, but a targeted read: allowed
	Block   Kind = "block"   // whole read of a large file: denied
)

// Decision is the policy result for one tool call.
type Decision struct {
	Kind   Kind
	Files  []File // large files involved (Partial or Block)
	Reason string // message for the model when blocked
	// Lines requested by a Partial read (limit, or head/tail count); the
	// caller charges them to the session's file budget.
	Lines int
}

// CheckRead handles a file-read tool (Claude Code's Read). offset and limit
// are 0 when absent.
func (c Config) CheckRead(path string, offset, limit int, cwd string) Decision {
	f, ok := inspect(resolve(path, cwd))
	if !ok || f.Lines <= c.MinLines {
		return Decision{Kind: Allow}
	}
	f.Path = path
	if limit > 0 && limit <= c.MinLines {
		return Decision{Kind: Partial, Files: []File{f}, Lines: limit}
	}
	return Decision{Kind: Block, Files: []File{f}, Reason: c.reason([]File{f})}
}

// readers are shell commands that dump whole files.
var readers = map[string]bool{"cat": true, "less": true, "more": true, "bat": true, "head": true, "tail": true}

// CheckBash handles a shell command. Like shunt, compound commands (pipes,
// redirects, chains) pass through in this version; the ledger cannot see
// them, so they are a known leak the benchmark measures indirectly.
func (c Config) CheckBash(command, cwd string) Decision {
	if hasShellOperators(command) {
		return Decision{Kind: Allow}
	}
	args := splitFields(command)
	if len(args) == 0 {
		return Decision{Kind: Allow}
	}
	name := filepath.Base(args[0])
	if !readers[name] {
		return Decision{Kind: Allow}
	}

	var files []string
	count := -1 // lines requested by head/tail -n; -1 = whole file
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case (name == "head" || name == "tail") && (a == "-n" || a == "--lines") && i+1 < len(args):
			count = atoiLoose(args[i+1])
			i++
		case (name == "head" || name == "tail") && strings.HasPrefix(a, "--lines="):
			count = atoiLoose(strings.TrimPrefix(a, "--lines="))
		case (name == "head" || name == "tail") && strings.HasPrefix(a, "-n"):
			count = atoiLoose(strings.TrimPrefix(a, "-n"))
		case (name == "head" || name == "tail") && len(a) > 1 && a[0] == '-' && isDigits(a[1:]):
			count = atoiLoose(a[1:])
		case strings.HasPrefix(a, "-"):
			// other flag
		default:
			files = append(files, a)
		}
	}
	if (name == "head" || name == "tail") && count < 0 {
		count = 10 // their default
	}

	var large []File
	for _, p := range files {
		f, ok := inspect(resolve(p, cwd))
		if ok && f.Lines > c.MinLines {
			f.Path = p
			large = append(large, f)
		}
	}
	if len(large) == 0 {
		return Decision{Kind: Allow}
	}
	if count >= 0 && count <= c.MinLines {
		return Decision{Kind: Partial, Files: large, Lines: count}
	}
	return Decision{Kind: Block, Files: large, Reason: c.reason(large)}
}

func (c Config) reason(files []File) string {
	var b strings.Builder
	var paths []string
	for _, f := range files {
		fmt.Fprintf(&b, "detour: %s has %d lines (limit %d, ~%d tokens). Don't read it whole.\n", f.Path, f.Lines, c.MinLines, f.EstTokens())
		paths = append(paths, shellQuote(f.Path))
	}
	fmt.Fprintf(&b, "Ask a specific question about it instead, adding any other files the question needs:\n")
	fmt.Fprintf(&b, "  detour read -q \"<your specific question>\" %s\n", strings.Join(paths, " "))
	fmt.Fprintf(&b, "The answer cites line numbers. To see or edit exact lines, use Read with offset and limit (limit <= %d).", c.MinLines)
	return b.String()
}

// BudgetReason is the message when targeted reads of a file exceed the
// session budget.
func (c Config) BudgetReason(f File, used int) string {
	return fmt.Sprintf("detour: you have already read %d lines of %s (%d lines) in this session; the budget per large file is %d.\n"+
		"Reading it chunk by chunk costs as much as reading it whole. Ask about the rest instead:\n"+
		"  detour read -q \"<your specific question>\" %s\n"+
		"If you are about to edit, you have already read enough to call Edit on the lines you saw.",
		used, f.Path, f.Lines, c.FileBudget, shellQuote(f.Path))
}

func resolve(path, cwd string) string {
	if filepath.IsAbs(path) || cwd == "" {
		return path
	}
	return filepath.Join(cwd, path)
}

// inspect returns line and byte counts for a regular file.
func inspect(path string) (File, bool) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return File{}, false
	}
	fh, err := os.Open(path)
	if err != nil {
		return File{}, false
	}
	defer fh.Close()
	lines, err := countLines(fh)
	if err != nil {
		return File{}, false
	}
	return File{Path: path, Lines: lines, Bytes: st.Size()}, true
}

func countLines(r io.Reader) (int, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	n, last := 0, byte('\n')
	buf := make([]byte, 64*1024)
	for {
		k, err := br.Read(buf)
		for _, ch := range buf[:k] {
			if ch == '\n' {
				n++
			}
		}
		if k > 0 {
			last = buf[k-1]
		}
		if err == io.EOF {
			if last != '\n' {
				n++ // final line without newline
			}
			return n, nil
		}
		if err != nil {
			return 0, err
		}
	}
}

func hasShellOperators(cmd string) bool {
	return strings.ContainsAny(cmd, "|;&><`$(")
}

// splitFields splits a simple command line, honouring single and double quotes.
func splitFields(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	inField := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inField = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inField {
				out = append(out, cur.String())
				cur.Reset()
				inField = false
			}
		default:
			cur.WriteRune(r)
			inField = true
		}
	}
	if inField {
		out = append(out, cur.String())
	}
	return out
}

func atoiLoose(s string) int {
	s = strings.TrimPrefix(s, "+")
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"\\$`") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

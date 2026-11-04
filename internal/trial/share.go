package trial

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// ReportMarker opens the body of a shared trial report. The aggregator
// finds reports by it, so it must not change within a format version.
const ReportMarker = "<!-- detour-trial-report v1 -->"

// Shared is what a tester sends: per-session measurements with nothing
// that identifies their code. Project names become project-1, -2...;
// session ids, paths, prompts and repository names are left out.
type Shared struct {
	Format        int       `json:"format"`
	Contributor   string    `json:"contributor"` // random, stable per machine
	Version       string    `json:"version"`
	OS            string    `json:"os"`
	Generated     time.Time `json:"generated"`
	Arms          []string  `json:"arms"`
	DelegateModel string    `json:"delegate_model"`
	Sessions      []Row     `json:"sessions"`
}

// Anonymize replaces project and profile names by stable placeholders
// (ordered by session count) and drops session ids.
func Anonymize(rows []Row) []Row {
	rename := func(key func(Row) string) map[string]string {
		n := map[string]int{}
		for _, r := range rows {
			n[key(r)]++
		}
		names := make([]string, 0, len(n))
		for k := range n {
			names = append(names, k)
		}
		sort.Slice(names, func(i, j int) bool {
			if n[names[i]] != n[names[j]] {
				return n[names[i]] > n[names[j]]
			}
			return names[i] < names[j]
		})
		m := map[string]string{}
		for i, k := range names {
			m[k] = fmt.Sprintf("%d", i+1)
		}
		return m
	}
	proj := rename(func(r Row) string { return r.Project })
	prof := rename(func(r Row) string { return r.Profile })
	out := make([]Row, len(rows))
	for i, r := range rows {
		r.ID = ""
		r.Project = "project-" + proj[r.Project]
		r.Profile = "account-" + prof[r.Profile]
		r.Unpriced = nil
		out[i] = r
	}
	return out
}

// ContributorID returns a random id kept in the config dir, so several
// reports from one tester can be told apart from reports by others.
func ContributorID(configPath string) string {
	p := filepath.Join(filepath.Dir(configPath), "contributor-id")
	if b, err := os.ReadFile(p); err == nil && len(bytes.TrimSpace(b)) > 0 {
		return string(bytes.TrimSpace(b))
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	_ = os.WriteFile(p, []byte(id+"\n"), 0o644)
	return id
}

// NewShared builds the shared report.
func NewShared(cfg Config, rows []Row, contributor, version string) Shared {
	return Shared{
		Format: 1, Contributor: contributor, Version: version,
		OS: runtime.GOOS + "/" + runtime.GOARCH, Generated: time.Now().UTC().Truncate(time.Second),
		Arms: cfg.Arms, DelegateModel: cfg.DelegateModel, Sessions: Anonymize(rows),
	}
}

// Title is the issue title for a report.
func (s Shared) Title() string {
	return fmt.Sprintf("[trial-report] %d sessions, detour %s", len(s.Sessions), s.Version)
}

// Body renders the issue body: a readable summary, then the data the
// aggregator parses.
func (s Shared) Body() (string, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", ReportMarker)
	fmt.Fprintf(&b, "Anonymous detour field-trial results: contributor `%s`, %s, delegate %s.\n\n", s.Contributor, s.OS, s.DelegateModel)
	var summary bytes.Buffer
	WriteSummary(&summary, s.Arms, s.Sessions)
	b.Write(summary.Bytes())
	fmt.Fprintf(&b, "\n<details><summary>Data (per session, anonymized)</summary>\n\n```json\n%s\n```\n</details>\n", data)
	return b.String(), nil
}

var dataBlock = regexp.MustCompile("(?s)```json\\s*(\\{.*?\\})\\s*```")

// ParseBody extracts a shared report from an issue body.
func ParseBody(body string) (Shared, error) {
	var s Shared
	if !strings.Contains(body, ReportMarker) {
		return s, errors.New("not a detour trial report")
	}
	m := dataBlock.FindStringSubmatch(body)
	if m == nil {
		return s, errors.New("report has no data block")
	}
	if err := json.Unmarshal([]byte(m[1]), &s); err != nil {
		return s, err
	}
	if s.Format != 1 {
		return s, fmt.Errorf("unknown report format %d", s.Format)
	}
	return s, nil
}

// NewIssueURL is a prefilled "new issue" link, for testers without gh.
// GitHub truncates very long URLs, so the caller checks the length.
func NewIssueURL(repo, title, body string) string {
	q := url.Values{}
	q.Set("title", title)
	q.Set("body", body)
	q.Set("labels", "trial-report")
	return "https://github.com/" + repo + "/issues/new?" + q.Encode()
}

package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
)

// budget tracks, per session, how many lines of each large file the agent has
// read in targeted chunks. State lives in <dir>/<session>.json. Parallel tool
// calls can race on it; the worst case is one extra chunk getting through.
type budget struct {
	path string
	Used map[string]int `json:"used"`
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func loadBudget(dir, session string) *budget {
	b := &budget{path: filepath.Join(dir, unsafeChars.ReplaceAllString(session, "_")+".json"), Used: map[string]int{}}
	if data, err := os.ReadFile(b.path); err == nil {
		_ = json.Unmarshal(data, b)
		if b.Used == nil {
			b.Used = map[string]int{}
		}
	}
	return b
}

func (b *budget) save() error {
	if err := os.MkdirAll(filepath.Dir(b.path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, b.path)
}

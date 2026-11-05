// Package hook adapts agent hook protocols to the shared policy.
package hook

import (
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jperdior/detour/internal/ledger"
	"github.com/jperdior/detour/internal/policy"
)

// claudeInput is the subset of Claude Code's PreToolUse payload we use.
type claudeInput struct {
	SessionID     string          `json:"session_id"`
	Cwd           string          `json:"cwd"`
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
}

type claudeOutput struct {
	HookSpecificOutput struct {
		HookEventName            string         `json:"hookEventName"`
		PermissionDecision       string         `json:"permissionDecision"`
		PermissionDecisionReason string         `json:"permissionDecisionReason,omitempty"`
		UpdatedInput             map[string]any `json:"updatedInput,omitempty"`
		AdditionalContext        string         `json:"additionalContext,omitempty"`
	} `json:"hookSpecificOutput"`
}

// Claude handles one Claude Code PreToolUse call. It fails open: any problem
// parsing the input lets the tool run, so detour never breaks a session.
// Blocked and targeted reads go to the ledger at ledgerPath.
func Claude(in io.Reader, out io.Writer, cfg policy.Config, ledgerPath string) error {
	var hi claudeInput
	if err := json.NewDecoder(in).Decode(&hi); err != nil {
		return nil
	}
	var d policy.Decision
	switch hi.ToolName {
	case "Read":
		var ti struct {
			FilePath string `json:"file_path"`
			Offset   int    `json:"offset"`
			Limit    int    `json:"limit"`
		}
		if json.Unmarshal(hi.ToolInput, &ti) != nil {
			return nil
		}
		d = cfg.CheckRead(ti.FilePath, ti.Offset, ti.Limit, hi.Cwd)
	case "Bash":
		var ti struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(hi.ToolInput, &ti) != nil {
			return nil
		}
		d = cfg.CheckBash(ti.Command, hi.Cwd)
	default:
		return nil
	}

	if d.Kind == policy.Allow {
		return nil
	}
	d = governed(d, hi.Cwd)
	if d.Kind == policy.Allow {
		return nil
	}
	budgetDir := filepath.Join(filepath.Dir(ledgerPath), "sessions")
	useBudget := !cfg.BlockOnly && !cfg.Shadow && cfg.FileBudget > 0 && hi.SessionID != "" && len(d.Files) > 0
	if d.Kind == policy.Partial && useBudget {
		d = chargeBudget(cfg, d, hi.SessionID, budgetDir, hi.Cwd)
	} else if d.Kind == policy.Block && useBudget {
		b := loadBudget(budgetDir, hi.SessionID)
		for _, f := range d.Files {
			d.Reason += cfg.BudgetNotice(f, b.Used[budgetKey(f.Path, hi.Cwd)])
		}
	}
	ev := ledger.Event{SessionID: hi.SessionID, Agent: "claude-code", Tool: hi.ToolName, Kind: ledger.KindPartial}
	for _, f := range d.Files {
		ev.Paths = append(ev.Paths, f.Path)
		ev.Lines += f.Lines
		ev.Bytes += f.Bytes
		ev.EstTokens += f.EstTokens()
	}
	if d.Kind == policy.Block {
		ev.Kind = ledger.KindBlock
	}
	if cfg.Shadow {
		ev.Kind = ledger.ShadowPrefix + ev.Kind
		_ = ledger.Append(ledgerPath, ev)
		return nil
	}
	_ = ledger.Append(ledgerPath, ev) // measurement must never break the agent

	if d.Kind != policy.Block {
		return nil
	}
	var o claudeOutput
	o.HookSpecificOutput.HookEventName = "PreToolUse"
	if cfg.Redirect {
		if in, note, ok := neutralise(hi.ToolName, hi.ToolInput); ok {
			o.HookSpecificOutput.PermissionDecision = "allow"
			o.HookSpecificOutput.UpdatedInput = in
			o.HookSpecificOutput.AdditionalContext = note + " " + d.Reason
			return json.NewEncoder(out).Encode(o)
		}
	}
	o.HookSpecificOutput.PermissionDecision = "deny"
	o.HookSpecificOutput.PermissionDecisionReason = d.Reason
	return json.NewEncoder(out).Encode(o)
}

// neutralise rewrites a blocked call into a harmless one: a Read of the
// first line only, or a Bash command that does nothing.
func neutralise(tool string, raw json.RawMessage) (map[string]any, string, bool) {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil || in == nil {
		return nil, "", false
	}
	switch tool {
	case "Read":
		delete(in, "offset")
		in["limit"] = 1
		return in, "detour returned only the first line of this read; it is not the whole file.", true
	case "Bash":
		in["command"] = "true"
		return in, "detour did not run this command; its empty output is not the file.", true
	}
	return nil, "", false
}

// chargeBudget adds a targeted read to the session's per-file budget and turns
// it into a block once the budget is exceeded. Blocked chunks are not charged.
func chargeBudget(cfg policy.Config, d policy.Decision, session, dir, cwd string) policy.Decision {
	b := loadBudget(dir, session)
	for _, f := range d.Files {
		if used := b.Used[budgetKey(f.Path, cwd)]; used+d.Lines > cfg.FileBudget {
			return policy.Decision{Kind: policy.Block, Files: d.Files, Reason: cfg.BudgetReason(f, used)}
		}
	}
	for _, f := range d.Files {
		b.Used[budgetKey(f.Path, cwd)] += d.Lines
	}
	_ = b.save()
	return d
}

// governed drops the files the policy must leave alone (prose, files outside
// the session's repository) and allows the call if none remain.
func governed(d policy.Decision, cwd string) policy.Decision {
	root := repoRoot(cwd)
	var keep []policy.File
	for _, f := range d.Files {
		if policy.Governed(f.Path, cwd, root) {
			keep = append(keep, f)
		}
	}
	if len(keep) == 0 {
		return policy.Decision{Kind: policy.Allow}
	}
	if len(keep) != len(d.Files) {
		d.Files = keep
	}
	return d
}

// repoRoot is the git toplevel of cwd, or cwd itself outside git. It runs
// only for reads of large files, so the hook stays fast on everything else.
func repoRoot(cwd string) string {
	if cwd == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return cwd
	}
	return strings.TrimSpace(string(out))
}

func budgetKey(path, cwd string) string {
	if !filepath.IsAbs(path) && cwd != "" {
		return filepath.Join(cwd, path)
	}
	return path
}

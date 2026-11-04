// Package hook adapts agent hook protocols to the shared policy.
package hook

import (
	"encoding/json"
	"io"

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
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
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
	_ = ledger.Append(ledgerPath, ev) // measurement must never break the agent

	if d.Kind != policy.Block {
		return nil
	}
	var o claudeOutput
	o.HookSpecificOutput.HookEventName = "PreToolUse"
	o.HookSpecificOutput.PermissionDecision = "deny"
	o.HookSpecificOutput.PermissionDecisionReason = d.Reason
	return json.NewEncoder(out).Encode(o)
}

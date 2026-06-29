package grok

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentcarto/core/common"
	"github.com/agentcarto/core/domain"
)

func (p *Plugin) PlanFork(_ context.Context, s domain.Session, t domain.ForkTarget) (domain.MutationPlan, domain.Command, error) {
	newID := common.NewID()
	dst := filepath.Join(filepath.Dir(s.SourceRef.Source), newID)
	plan := domain.MutationPlan{PluginID: p.id, Description: "fork Grok session", AllowedRoots: []string{p.o.SessionsDir}}
	// Copy the whole session directory and create a fork by truncating the copy
	// at the chosen point; the original is left untouched.
	e := filepath.WalkDir(s.SourceRef.Source, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(s.SourceRef.Source, path)
		switch rel {
		case "chat_history.jsonl":
			b = grokTruncateChatHistory(b, t.KeepTurns) // truncate the canonical history shown by the manager
		case "signals.json":
			b = grokClearReverted(b) // make chat_history be read as the canonical source
		case "updates.jsonl":
			b = appendRewindMarker(b, newID, t.KeepTurns)
		case "summary.json":
			b = grokMarkFork(b, s.SessionID, newID) // record fork lineage for the manager
		}
		plan.Writes = append(plan.Writes, domain.FileWrite{Path: filepath.Join(dst, rel), Data: b, Mode: 0600})
		return nil
	})
	if e != nil {
		return plan, domain.Command{}, e
	}
	return plan, domain.Command{Executable: p.o.Executable, Args: []string{"--resume", newID}, WorkingDirectory: s.CWD}, nil
}

// appendRewindMarker appends a rewind_marker update line that points at the
// truncation boundary, so the forked session resumes from that turn.
func appendRewindMarker(b []byte, sessionID string, keepTurns int) []byte {
	now := time.Now()
	marker := map[string]any{
		"timestamp": float64(now.UnixMilli()) / 1000,
		"params": map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate":       "rewind_marker",
				"target_prompt_index": keepTurns,
				"created_at":          now.UTC().Format(time.RFC3339Nano),
			},
			"_meta": map[string]any{
				"eventId":          "rewind-" + common.NewID(),
				"agentTimestampMs": now.UnixMilli(),
			},
		},
	}
	x, _ := json.Marshal(marker)
	b = append(b, x...)
	return append(b, '\n')
}

// grokTruncateChatHistory truncates chat_history.jsonl to the first keep
// <user_query> turns.
func grokTruncateChatHistory(b []byte, keep int) []byte {
	var out [][]byte
	seen := 0
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var o map[string]any
		if json.Unmarshal(line, &o) == nil && common.String(o["type"]) == "user" {
			if strings.Contains(userLineText(o), "<user_query>") {
				seen++
				if seen > keep {
					break
				}
			}
		}
		out = append(out, line)
	}
	return bytes.Join(append(out, nil), []byte("\n"))
}

// userLineText flattens the content of a chat_history user line into plain text,
// whether the content is a string or a list of text parts.
func userLineText(o map[string]any) string {
	switch c := o["content"].(type) {
	case string:
		return c
	case []any:
		var ps []string
		for _, x := range c {
			ps = append(ps, common.String(common.Map(x)["text"]))
		}
		return strings.Join(ps, " ")
	}
	return ""
}

// grokClearReverted resets the revert signals so the forked session's
// chat_history is read as the canonical source.
func grokClearReverted(b []byte) []byte {
	var s map[string]any
	if json.Unmarshal(b, &s) != nil {
		return b
	}
	s["hasReverted"] = false
	s["editAndRetryCount"] = 0
	s["regenerationCount"] = 0
	x, e := json.Marshal(s)
	if e != nil {
		return b
	}
	return x
}

// grokMarkFork records the parent and new session IDs in the fork's summary.json.
func grokMarkFork(b []byte, parentID, newID string) []byte {
	var s map[string]any
	if json.Unmarshal(b, &s) != nil {
		s = map[string]any{}
	}
	s["parent_session_id"] = parentID
	s["session_kind"] = "fork"
	s["session_id"] = newID
	x, e := json.Marshal(s)
	if e != nil {
		return b
	}
	return x
}

func encode(cwd string) string { return url.PathEscape(cwd) }

func (p *Plugin) PlanRelocate(_ context.Context, old, new string, _ []domain.Session) (domain.MutationPlan, error) {
	oldDir, newDir := filepath.Join(p.o.SessionsDir, encode(old)), filepath.Join(p.o.SessionsDir, encode(new))
	plan := domain.MutationPlan{PluginID: p.id, Description: "relocate Grok sessions", AllowedRoots: []string{p.o.SessionsDir}}
	fs, e := common.WalkFiles(oldDir, func(x string) bool {
		return filepath.Base(x) == "summary.json" || filepath.Base(x) == "prompt_context.json"
	})
	if e != nil {
		return plan, e
	}
	for _, f := range fs {
		data, changed, e := common.RewriteJSON(f, func(o map[string]any) bool {
			if filepath.Base(f) == "summary.json" {
				info := common.Map(o["info"])
				if common.String(info["cwd"]) == old {
					info["cwd"] = new
					return true
				}
			} else if common.String(o["working_directory"]) == old {
				o["working_directory"] = new
				return true
			}
			return false
		})
		if e != nil {
			return plan, e
		}
		if changed {
			plan.Writes = append(plan.Writes, domain.FileWrite{Path: f, Data: data, Mode: 0600})
		}
	}
	if oldDir != newDir {
		plan.Moves = append(plan.Moves, domain.PathMove{From: oldDir, To: newDir})
	}
	return plan, nil
}

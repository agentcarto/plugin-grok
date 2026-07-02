package grok

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/agentcarto/core/common"
	"github.com/agentcarto/core/domain"
	"github.com/agentcarto/core/plugin"
	"github.com/agentcarto/core/scan"
)

// summary reads summary.json and returns the session title, model, and parent.
func summary(dir string) (title, model, parent string) {
	b, e := os.ReadFile(filepath.Join(dir, "summary.json"))
	if e != nil {
		return
	}
	var v map[string]any
	if json.Unmarshal(b, &v) != nil {
		return
	}
	model = common.String(v["current_model_id"])
	parent = common.String(v["parent_session_id"])
	if parent == "" {
		parent = common.String(v["parentSessionId"])
	}
	// Pick the title from summary-like fields, then strip noise and tidy it.
	title = common.CleanTitle(common.TitleCandidate(grokPickSummary(v)))
	return
}

// grokPickSummary searches summary.json for a string usable as a summary title.
func grokPickSummary(data any) string {
	switch v := data.(type) {
	case string:
		return v
	case map[string]any:
		for _, k := range []string{"generated_title", "session_summary", "title", "summary", "name", "description", "topic", "text"} {
			switch x := v[k].(type) {
			case string:
				if strings.TrimSpace(x) != "" {
					return x
				}
			case map[string]any:
				if inner := grokPickSummary(x); inner != "" {
					return inner
				}
			}
		}
	}
	return ""
}

// sessionDirs returns the candidate session directories under a top-level entry:
// the root itself when it holds a summary.json, plus each immediate subdirectory.
func sessionDirs(root string) []string {
	var dirs []string
	if _, e := os.Stat(filepath.Join(root, "summary.json")); e == nil {
		dirs = append(dirs, root)
	}
	children, _ := os.ReadDir(root)
	for _, child := range children {
		if child.IsDir() {
			dirs = append(dirs, filepath.Join(root, child.Name()))
		}
	}
	return dirs
}

// sessionFromDir builds a Session from a parsed session directory.
func (p *Plugin) sessionFromDir(ctx context.Context, dir, cwd string, ev []domain.Event) domain.Session {
	title, model, parent := summary(dir)
	if title == "" {
		title = common.Title(ev, "(grok session)")
	}
	// Use the directory name as the SessionID (the physical ID, unique per fork).
	// The info.id in summary.json is a copy of the parent's logical ID left behind
	// when the fork was created; overwriting the SessionID with it would make a
	// fork collide with its parent, so buildForkMap would treat the session as its
	// own child and corrupt the display (the parent vanishes and a child sprouts).
	// The basename avoids that.
	id := filepath.Base(dir)
	updated := common.MaxMTime(dir)
	// The first timestamped event marks the session start; fall back to the
	// update time only when no event carries a timestamp.
	started := updated
	for _, e := range ev {
		if !e.Timestamp.IsZero() {
			started = e.Timestamp
			break
		}
	}
	return domain.Session{PluginID: p.id, AgentType: "grok", SessionID: id, CWD: cwd, StartedAt: started, UpdatedAt: updated, Title: title, Model: model, ParentSessionID: parent, SourceRef: domain.SessionRef{Source: dir}, LastKind: grokTailKind(ctx, dir)}
}

func (p *Plugin) Scan(ctx context.Context, in plugin.ScanInput) (plugin.ScanOutput, error) {
	cache := scan.New(in.Warm, in.Dead, Factory{}.Descriptor().ParserVersion)
	ds, e := os.ReadDir(p.o.SessionsDir)
	if os.IsNotExist(e) {
		return plugin.ScanOutput{}, nil
	}
	if e != nil {
		return plugin.ScanOutput{}, e
	}
	var out []domain.Session
	for _, d := range ds {
		if !d.IsDir() {
			continue
		}
		// PathUnescape, not QueryUnescape: directory names are written with
		// url.PathEscape (see encode in fork.go), and QueryUnescape would turn a
		// literal "+" in the cwd into a space.
		cwd, _ := url.PathUnescape(d.Name())
		root := filepath.Join(p.o.SessionsDir, d.Name())
		for _, dir := range sessionDirs(root) {
			if s, ok := cache.Reuse(dir); ok {
				out = append(out, s)
				continue
			}
			if cache.Skip(dir) {
				continue
			}
			ev := parse(ctx, dir)
			if len(ev) == 0 {
				cache.Dead(dir)
				continue
			}
			s := p.sessionFromDir(ctx, dir, cwd, ev)
			cache.Stamp(&s)
			out = append(out, s)
		}
	}
	return plugin.ScanOutput{Sessions: out, Dead: cache.DeadOut()}, nil
}

var grokPhaseKind = map[string]domain.EventKind{
	"waiting_for_model":   domain.EventStream,
	"streaming_reasoning": domain.EventReasoning,
	"streaming_text":      domain.EventStream,
	"tool_execution":      domain.EventToolCall,
	"permission_prompt":   domain.EventToolCall,
}

// grokEventKind maps a single events.jsonl record to the event kind used for
// activity detection.
func grokEventKind(o map[string]any) domain.EventKind {
	switch common.String(o["type"]) {
	case "phase_changed":
		if k, ok := grokPhaseKind[common.String(o["phase"])]; ok {
			return k
		}
		return domain.EventStream
	case "turn_started", "loop_started", "first_token":
		return domain.EventStream
	case "tool_started", "permission_requested", "permission_resolved":
		return domain.EventToolCall
	case "tool_completed":
		return domain.EventToolResult
	case "turn_ended":
		return domain.EventTurnComplete
	}
	return domain.EventMeta
}

// grokTailKind returns the kind of the last real event in events.jsonl.
func grokTailKind(ctx context.Context, dir string) domain.EventKind {
	last := domain.EventKind("")
	_ = common.JSONLines(ctx, filepath.Join(dir, "events.jsonl"), func(_ int, o map[string]any) error {
		if k := grokEventKind(o); k != domain.EventMeta {
			last = k
		}
		return nil
	})
	return last
}

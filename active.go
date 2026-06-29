package grok

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/agentcarto/core/common"
	"github.com/agentcarto/core/domain"
)

// aliveSessions reads the active-sessions file and returns the set of session
// IDs whose recorded pid matches a currently running process. The file may store
// either a map keyed by session ID or a list of objects carrying session_id.
func (p *Plugin) aliveSessions(ps []domain.Process) map[string]bool {
	alive := map[string]bool{}
	b, _ := os.ReadFile(p.o.ActiveSessionsFile)
	var raw any
	if json.Unmarshal(b, &raw) != nil {
		return alive
	}
	pidRunning := func(pid int32) bool {
		for _, pr := range ps {
			if pr.PID == pid {
				return true
			}
		}
		return false
	}
	switch x := raw.(type) {
	case map[string]any:
		for id, v := range x {
			m := common.Map(v)
			if n, ok := m["pid"].(float64); ok && pidRunning(int32(n)) {
				alive[id] = true
			}
		}
	case []any:
		for _, v := range x {
			m := common.Map(v)
			if n, ok := m["pid"].(float64); ok && pidRunning(int32(n)) {
				alive[common.String(m["session_id"])] = true
			}
		}
	}
	return alive
}

func (p *Plugin) DetectActive(ctx context.Context, ss []domain.Session, ps []domain.Process) ([]domain.Session, error) {
	alive := p.aliveSessions(ps)
	for i := range ss {
		if alive[ss[i].SessionID] || common.ProcessMatches(ss[i], ps) {
			ss[i].Status = common.ActiveStatus(ss[i].LastKind, p.o.UserEventMeansRunning)
			ss[i].PermissionWait = grokPermission(ctx, ss[i].SourceRef.Source)
		}
	}
	return ss, nil
}

// grokPermission reports whether the session is currently blocked waiting for a
// permission decision, based on the tail state in events.jsonl.
func grokPermission(ctx context.Context, dir string) bool {
	wait := false
	_ = common.JSONLines(ctx, filepath.Join(dir, "events.jsonl"), func(_ int, o map[string]any) error {
		switch common.String(o["type"]) {
		case "permission_requested":
			wait = true
		case "permission_resolved", "turn_ended":
			wait = false
		}
		return nil
	})
	return wait
}

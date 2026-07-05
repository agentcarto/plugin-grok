package grok

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agentcarto/core/common"
	"github.com/agentcarto/core/domain"
)

func (p *Plugin) LoadConversation(ctx context.Context, r domain.SessionRef) (*domain.Conversation, error) {
	// Grok records a single model per session (summary.json current_model_id),
	// with no per-turn attribution in the chat log, so every turn shows that one
	// model.
	_, model, _ := summary(r.Source)
	// Only sessions that used revert build a branch tree from updates; everything
	// else is a linear conversation read from chat_history.
	if grokReverted(r.Source) {
		if c := grokConversation(grokStampModel(parseUpdates(ctx, r.Source), model)); len(c.Nodes) > 0 {
			return &c, nil
		}
	}
	c := common.Linear(grokStampModel(parse(ctx, r.Source), model))
	return &c, nil
}

// grokStampModel stamps the session's single model onto every event so each
// turn row can display it.
func grokStampModel(ev []domain.Event, model string) []domain.Event {
	if model == "" {
		return ev
	}
	for i := range ev {
		ev[i].Model = model
	}
	return ev
}

// grokReverted reports whether the session used rewind or edit-and-retry,
// based on signals.json.
func grokReverted(dir string) bool {
	b, e := os.ReadFile(filepath.Join(dir, "signals.json"))
	if e != nil {
		return false
	}
	var s map[string]any
	if json.Unmarshal(b, &s) != nil {
		return false
	}
	if v, ok := s["hasReverted"].(bool); ok && v {
		return true
	}
	num := func(k string) float64 { n, _ := s[k].(float64); return n }
	return num("editAndRetryCount") > 0 || num("regenerationCount") > 0
}

// grokPreambles are the prefixes of update blocks that Grok does not count as
// real conversation turns.
var grokPreambles = []string{"<user_info", "<system_reminder", "<agent_skills", "<environment", "<command-"}

func grokIsPreamble(text string) bool {
	t := strings.TrimLeft(text, " \t\r\n")
	for _, p := range grokPreambles {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// grokConversation builds a conversation tree from an updates-derived event
// sequence, branching at each rewind_marker.
func grokConversation(ev []domain.Event) domain.Conversation {
	grokMarkCompaction(ev)
	annotate(ev)
	var nodes []domain.ConvNode
	parentByID := map[string]string{}
	parent := ""
	var turnStarts []string
	for i, e := range ev {
		if e.RawType == "rewind_marker" {
			k := -1
			fmt.Sscan(e.Text, &k)
			if k >= 0 && k < len(turnStarts) {
				dropFirst := turnStarts[k] // first turn-start node to drop
				parent = parentByID[dropFirst]
				turnStarts = turnStarts[:k]
			}
			continue
		}
		id := fmt.Sprintf("event-%08d", i)
		nodes = append(nodes, domain.ConvNode{ID: id, Parent: parent, Timestamp: e.Timestamp, Events: []domain.Event{e}})
		parentByID[id] = parent
		parent = id
		if e.Kind == domain.EventUser && e.RawType != domain.RawCompactSummary && !grokIsPreamble(e.Text) {
			turnStarts = append(turnStarts, id)
		}
	}
	return domain.NewConversation(nodes)
}

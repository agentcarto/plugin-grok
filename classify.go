package grok

import (
	"encoding/json"
	"strings"

	"github.com/agentcarto/core/domain"
)

// promptText returns the cleaned, whitespace-folded genuine prompt in text, or
// "" when the message is a Grok-injected preamble or a short single-line slash
// command rather than real user input. A <user_query> wrapper is unwrapped to
// its inner content first (Grok wraps the typed prompt in it).
func promptText(text string) string {
	t := strings.TrimSpace(text)
	if m := userQueryRE.FindStringSubmatch(t); m != nil && strings.TrimSpace(m[1]) != "" {
		t = strings.TrimSpace(m[1])
	}
	// <rules> blocks are system-injected like the other preambles, but stay out
	// of grokPreambles: that list also drives rollback counting, whose behavior
	// must not change here.
	if t == "" || grokIsPreamble(t) || strings.HasPrefix(t, "<rules") {
		return ""
	}
	if strings.HasPrefix(t, "/") && !strings.Contains(t, "\n") && len([]rune(t)) <= 40 {
		return ""
	}
	return strings.Join(strings.Fields(t), " ")
}

// annotate fills the normalized Prompt field on user events and ToolArg on
// tool calls, in place. It runs after grokMarkCompaction so compaction
// summaries never carry a prompt.
func annotate(ev []domain.Event) {
	for i := range ev {
		switch {
		case ev[i].Kind == domain.EventToolCall:
			ev[i].ToolArg = toolArg(ev[i].Text)
		case ev[i].Kind == domain.EventUser && ev[i].RawType != domain.RawCompactSummary:
			ev[i].Prompt = promptText(ev[i].Text)
		}
	}
}

// toolArg extracts the one-line display argument for a tool call from its JSON
// arguments payload, or "" when the payload has no salient string field.
func toolArg(text string) string {
	var m map[string]any
	if json.Unmarshal([]byte(text), &m) != nil {
		return ""
	}
	for _, k := range []string{"description", "file_path", "notebook_path", "path", "command", "pattern", "query", "url", "prompt"} {
		if v, _ := m[k].(string); strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

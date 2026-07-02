package grok

import (
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

// annotate fills the normalized Prompt field on user events, in place. It runs
// after grokMarkCompaction so compaction summaries never carry a prompt.
func annotate(ev []domain.Event) {
	for i := range ev {
		if ev[i].Kind != domain.EventUser || ev[i].RawType == domain.RawCompactSummary {
			continue
		}
		ev[i].Prompt = promptText(ev[i].Text)
	}
}

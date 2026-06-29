package grok

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/agentcarto/core/domain"
)

func TestEventOf(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		kind domain.EventKind
		text string
	}{
		{"role user", map[string]any{"role": "user", "text": "hi"}, domain.EventUser, "hi"},
		{"role assistant", map[string]any{"role": "assistant", "content": "yo"}, domain.EventAssistant, "yo"},
		{"type overrides role", map[string]any{"role": "user", "type": "agent_message", "text": "x"}, domain.EventAssistant, "x"},
		{"reasoning", map[string]any{"type": "reasoning"}, domain.EventReasoning, ""},
		{"tool call", map[string]any{"type": "tooluse"}, domain.EventToolCall, ""},
		{"tool result", map[string]any{"type": "tool_completed"}, domain.EventToolResult, ""},
		{"turn ended", map[string]any{"type": "turn_ended"}, domain.EventTurnComplete, ""},
		{"event field as type", map[string]any{"event": "streaming"}, domain.EventStream, ""},
		{"phase_changed -> reasoning", map[string]any{"type": "phase_changed", "phase": "streaming_reasoning"}, domain.EventReasoning, ""},
		{"phase_changed -> toolcall", map[string]any{"type": "phase_changed", "phase": "permission_prompt"}, domain.EventToolCall, ""},
		{"phase_changed default -> stream", map[string]any{"type": "phase_changed", "phase": "whatever"}, domain.EventStream, ""},
		{"text falls back to content", map[string]any{"content": "body"}, domain.EventMeta, "body"},
		{"unknown stays meta", map[string]any{"type": "mystery"}, domain.EventMeta, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := eventOf(c.in)
			if e.Kind != c.kind {
				t.Errorf("kind = %q, want %q", e.Kind, c.kind)
			}
			if e.Text != c.text {
				t.Errorf("text = %q, want %q", e.Text, c.text)
			}
		})
	}
}

func TestGrokIsCompactText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"plain prefix", "Your conversation was summarized to save space", true},
		{"leading whitespace", "\n\t  Your conversation was summarized", true},
		{"inside user_query", "<user_query>Your conversation was summarized</user_query>", true},
		{"normal message", "Please summarize the file", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := grokIsCompactText(c.in); got != c.want {
				t.Errorf("grokIsCompactText(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestGrokMarkCompaction(t *testing.T) {
	ev := []domain.Event{
		{Kind: domain.EventUser, Text: "Your conversation was summarized ..."},
		{Kind: domain.EventUser, Text: "a real question"},
		{Kind: domain.EventAssistant, Text: "Your conversation was summarized ..."}, // not a user turn
	}
	grokMarkCompaction(ev)
	if ev[0].RawType != "compact_summary" {
		t.Error("compaction user message should be tagged")
	}
	if ev[1].RawType == "compact_summary" {
		t.Error("ordinary user message must not be tagged")
	}
	if ev[2].RawType == "compact_summary" {
		t.Error("assistant message must not be tagged even if text matches")
	}
}

func TestUpdateMillis(t *testing.T) {
	if got := updateMillis(map[string]any{"timestamp": float64(2)}); got.UnixMilli() != 2000 {
		t.Errorf("UnixMilli = %d, want 2000", got.UnixMilli())
	}
	if got := updateMillis(map[string]any{}); !got.IsZero() {
		t.Errorf("missing timestamp should yield zero time, got %v", got)
	}
}

func TestQuoteCol(t *testing.T) {
	cases := map[string]string{
		"":     "''",
		"role": `"role"`,
		`a"b`:  `"a""b"`, // embedded double-quote is doubled (injection guard)
	}
	for in, want := range cases {
		if got := quoteCol(in); got != want {
			t.Errorf("quoteCol(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseUpdatesMergesChunks(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		`{"timestamp":1,"params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"Hel"}}}}`,
		`{"timestamp":1,"params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"lo"}}}}`,
		`{"timestamp":2,"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"text":"hi"}}}}`,
		`{"timestamp":3,"params":{"update":{"sessionUpdate":"tool_call","kind":"bash","title":"run it"}}}`,
		`{"timestamp":4,"params":{"update":{"sessionUpdate":"rewind_marker","target_prompt_index":2}}}`,
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "updates.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	ev := parseUpdates(context.Background(), dir)
	if len(ev) != 4 {
		t.Fatalf("got %d events, want 4: %#v", len(ev), ev)
	}
	if ev[0].Kind != domain.EventAssistant || ev[0].Text != "Hello" {
		t.Errorf("event0 = %v %q, want assistant 'Hello' (chunks merged)", ev[0].Kind, ev[0].Text)
	}
	if ev[1].Kind != domain.EventUser || ev[1].Text != "hi" {
		t.Errorf("event1 = %v %q, want user 'hi'", ev[1].Kind, ev[1].Text)
	}
	if ev[2].Kind != domain.EventToolCall || ev[2].ToolName != "bash" || ev[2].Text != "run it" {
		t.Errorf("event2 = %#v, want tool_call bash 'run it'", ev[2])
	}
	if ev[3].Kind != domain.EventMeta || ev[3].RawType != "rewind_marker" || ev[3].Text != "2" {
		t.Errorf("event3 = %#v, want rewind_marker meta '2'", ev[3])
	}
}

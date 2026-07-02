package grok

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/agentcarto/core/common"
	"github.com/agentcarto/core/domain"
	_ "modernc.org/sqlite"
)

// eventOf maps a raw chat_history/events record to a domain.Event, deriving the
// event kind from its role and/or type fields.
func eventOf(o map[string]any) domain.Event {
	role := common.String(o["role"])
	typ := common.String(o["type"])
	if typ == "" {
		typ = common.String(o["event"])
	}
	k := domain.EventMeta
	switch role {
	case "user":
		k = domain.EventUser
	case "assistant":
		k = domain.EventAssistant
	case "system":
		k = domain.EventSystem
	}
	switch typ {
	case "user":
		k = domain.EventUser
	case "system":
		k = domain.EventSystem
	case "agent_message", "assistant":
		k = domain.EventAssistant
	case "reasoning", "streaming_reasoning":
		k = domain.EventReasoning
	case "tooluse", "tool_execution", "permission_requested", "tool_started":
		k = domain.EventToolCall
	case "tooluse_result", "tool_completed", "tool_result":
		k = domain.EventToolResult
	case "streaming", "streaming_text", "waiting_for_model":
		k = domain.EventStream
	case "streaming_complete", "turn_ended":
		k = domain.EventTurnComplete
	case "phase_changed":
		switch common.String(o["phase"]) {
		case "streaming_reasoning":
			k = domain.EventReasoning
		case "tool_execution", "permission_prompt":
			k = domain.EventToolCall
		default:
			k = domain.EventStream
		}
	case "turn_started", "loop_started", "first_token":
		k = domain.EventStream
	}
	// common.Text handles both plain strings and [{"text": ...}] block lists
	// (user content arrays and reasoning summary_text items).
	text := common.Text(o["text"])
	if text == "" {
		text = common.Text(o["content"])
	}
	if text == "" {
		text = common.Text(o["summary"])
	}
	return domain.Event{Kind: k, Text: text, Timestamp: common.Time(common.String(o["timestamp"])), ToolName: common.String(o["tool_name"]), RawType: typ}
}

// chatEvents expands a single chat_history record into its events: the record's
// own event plus one tool-call event per entry of an assistant "tool_calls"
// array (Grok keeps calls inline in the assistant record rather than as
// separate records). Text-less assistant and reasoning events are dropped —
// reasoning bodies are encrypted with only the summary in plaintext, and an
// assistant record may carry tool calls with no message.
func chatEvents(o map[string]any) []domain.Event {
	e := eventOf(o)
	var out []domain.Event
	empty := strings.TrimSpace(e.Text) == ""
	skip := empty && (e.Kind == domain.EventReasoning || e.Kind == domain.EventAssistant)
	if !skip && (e.Kind != domain.EventMeta || e.RawType != "") {
		out = append(out, e)
	}
	for _, tc := range common.Slice(o["tool_calls"]) {
		m := common.Map(tc)
		name := common.String(m["name"])
		if name == "" {
			name = "tool"
		}
		out = append(out, domain.Event{Kind: domain.EventToolCall, Text: common.String(m["arguments"]), Timestamp: e.Timestamp, ToolName: name, RawType: "tool_call"})
	}
	return out
}

// parse reads exactly one conversation-body file, trying candidates in priority
// order, so that runtime noise (e.g. events.jsonl) is kept out of the chat
// history.
func parse(ctx context.Context, dir string) []domain.Event {
	for _, name := range []string{"chat_history.jsonl", "events.jsonl"} {
		path := filepath.Join(dir, name)
		head := make([]byte, 16)
		f, e := os.Open(path)
		if e != nil {
			continue
		}
		_, _ = io.ReadFull(f, head) // a short read leaves head partial, which simply fails the SQLite match
		_ = f.Close()
		var ev []domain.Event
		if string(head) == "SQLite format 3\x00" {
			ev = readSQLite(path)
		} else {
			_ = common.JSONLines(ctx, path, func(_ int, o map[string]any) error {
				ev = append(ev, chatEvents(o)...)
				return nil
			})
		}
		if len(ev) > 0 {
			grokBackfillTimestamps(ctx, dir, ev)
			grokMarkCompaction(ev)
			return ev
		}
	}
	return nil
}

var grokChunkKind = map[string]domain.EventKind{
	"user_message_chunk":  domain.EventUser,
	"agent_message_chunk": domain.EventAssistant,
	"agent_thought_chunk": domain.EventReasoning,
}

const grokCompactPrefix = "Your conversation was summarized"

var userQueryRE = regexp.MustCompile(`(?s)<user_query>(.*?)</user_query>`)

// grokIsCompactText reports whether text is a compaction-summary user message.
// It also inspects the contents of any <user_query> wrapper.
func grokIsCompactText(text string) bool {
	t := text
	if m := userQueryRE.FindStringSubmatch(t); m != nil {
		t = m[1]
	}
	return strings.HasPrefix(strings.TrimLeft(t, " \t\r\n"), grokCompactPrefix)
}

// grokMarkCompaction tags user events that are compaction summaries.
func grokMarkCompaction(ev []domain.Event) {
	for i := range ev {
		if ev[i].Kind == domain.EventUser && grokIsCompactText(ev[i].Text) {
			ev[i].RawType = "compact_summary"
		}
	}
}

// updateMillis parses the second-resolution Unix timestamp (fractional seconds
// allowed) from a JSONL update record, returning the zero time when it is
// absent. Fork markers are written in the same unit (see appendRewindMarker).
func updateMillis(o map[string]any) time.Time {
	if n, ok := o["timestamp"].(float64); ok {
		return time.UnixMilli(int64(n * 1000))
	}
	return time.Time{}
}

// updatePayload extracts the params.update object from a JSONL update record.
func updatePayload(o map[string]any) map[string]any {
	return common.Map(common.Map(o["params"])["update"])
}

// grokBackfillTimestamps fills in timestamps on chat_history events that lack
// them, matching the timestamps from updates.jsonl per kind in arrival order.
func grokBackfillTimestamps(ctx context.Context, dir string, ev []domain.Event) {
	byKind := map[domain.EventKind][]time.Time{}
	var cur domain.EventKind
	_ = common.JSONLines(ctx, filepath.Join(dir, "updates.jsonl"), func(_ int, o map[string]any) error {
		k, ok := grokChunkKind[common.String(updatePayload(o)["sessionUpdate"])]
		if !ok {
			cur = ""
			return nil
		}
		if k != cur {
			byKind[k] = append(byKind[k], updateMillis(o))
			cur = k
		}
		return nil
	})
	idx := map[domain.EventKind]int{}
	for i := range ev {
		lst := byKind[ev[i].Kind]
		j := idx[ev[i].Kind]
		if j < len(lst) && !lst[j].IsZero() && ev[i].Timestamp.IsZero() {
			ev[i].Timestamp = lst[j]
		}
		idx[ev[i].Kind] = j + 1
	}
}

// sqliteTables returns the names of all tables in the database.
func sqliteTables(db *sql.DB) []string {
	rows, e := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if e != nil {
		return nil
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		tables = append(tables, s)
	}
	return tables
}

// messageColumns inspects a table's schema and returns the column names that
// best match a message role and message text, or empty strings when missing.
func messageColumns(db *sql.DB, table string) (roleCol, textCol string) {
	cs, e := db.Query("PRAGMA table_info('" + strings.ReplaceAll(table, "'", "''") + "')")
	if e != nil {
		return "", ""
	}
	defer cs.Close()
	for cs.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		_ = cs.Scan(&cid, &name, &typ, &notnull, &def, &pk)
		low := strings.ToLower(name)
		if roleCol == "" && (low == "role" || low == "sender" || low == "author") {
			roleCol = name
		}
		if textCol == "" && (low == "content" || low == "text" || low == "message" || low == "body" || low == "data") {
			textCol = name
		}
	}
	return roleCol, textCol
}

// readMessageTable reads role/text rows from a single table into events.
func readMessageTable(db *sql.DB, table, roleCol, textCol string) []domain.Event {
	q := fmt.Sprintf("SELECT %s,%s FROM %q ORDER BY rowid", quoteCol(roleCol), quoteCol(textCol), table)
	rs, e := db.Query(q)
	if e != nil {
		return nil
	}
	defer rs.Close()
	var out []domain.Event
	for rs.Next() {
		var role, text any
		if rs.Scan(&role, &text) != nil {
			continue
		}
		k := domain.EventMeta
		switch strings.ToLower(fmt.Sprint(role)) {
		case "user":
			k = domain.EventUser
		case "assistant", "agent":
			k = domain.EventAssistant
		case "system":
			k = domain.EventSystem
		}
		out = append(out, domain.Event{Kind: k, Text: fmt.Sprint(text), RawType: fmt.Sprint(role)})
	}
	return out
}

// readSQLite reads conversation events from a SQLite-backed history file,
// returning the rows of the first table that yields any messages.
func readSQLite(path string) []domain.Event {
	db, e := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if e != nil {
		return nil
	}
	defer db.Close()
	for _, table := range sqliteTables(db) {
		roleCol, textCol := messageColumns(db, table)
		if textCol == "" {
			continue
		}
		if out := readMessageTable(db, table, roleCol, textCol); len(out) > 0 {
			return out
		}
	}
	return nil
}

func quoteCol(s string) string {
	if s == "" {
		return "''"
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// parseUpdates reads updates.jsonl and merges consecutive chunks of the same
// kind into a single event.
func parseUpdates(ctx context.Context, dir string) []domain.Event {
	var ev []domain.Event
	var curKind domain.EventKind
	var curText strings.Builder
	var curTs time.Time
	flush := func() {
		if curKind != "" && strings.TrimSpace(curText.String()) != "" {
			ev = append(ev, domain.Event{Kind: curKind, Text: strings.TrimSpace(curText.String()), Timestamp: curTs, RawType: string(curKind)})
		}
		curKind, curTs = "", time.Time{}
		curText.Reset()
	}
	_ = common.JSONLines(ctx, filepath.Join(dir, "updates.jsonl"), func(_ int, o map[string]any) error {
		u := updatePayload(o)
		su := common.String(u["sessionUpdate"])
		ts := updateMillis(o)
		if k, ok := grokChunkKind[su]; ok {
			if k != curKind {
				flush()
				curKind, curTs = k, ts
			}
			curText.WriteString(common.String(common.Map(u["content"])["text"]))
			return nil
		}
		switch su {
		case "tool_call":
			flush()
			// Real records carry the tool name in "title" and the arguments in
			// "rawInput"; "kind" is kept as the first choice for older layouts.
			tool := common.String(u["kind"])
			if tool == "" {
				tool = common.String(u["title"])
			}
			if tool == "" {
				tool = "tool"
			}
			text := common.String(u["title"])
			if in := u["rawInput"]; in != nil {
				args, _ := json.Marshal(in)
				text = strings.TrimSpace(text + "\n" + string(args))
			}
			ev = append(ev, domain.Event{Kind: domain.EventToolCall, Text: text, Timestamp: ts, ToolName: tool, RawType: "tool_call"})
		case "tool_call_update":
			// A call streams many updates (e.g. Bash output_delta); exactly one per
			// call carries a final status, so only that one becomes the result.
			if st := common.String(u["status"]); st != "completed" && st != "failed" {
				return nil
			}
			flush()
			ev = append(ev, domain.Event{Kind: domain.EventToolResult, Text: toolUpdateResultText(u), Timestamp: ts, RawType: "tool_call_update"})
		case "rewind_marker":
			flush()
			ev = append(ev, domain.Event{Kind: domain.EventMeta, Text: fmt.Sprint(u["target_prompt_index"]), Timestamp: ts, RawType: "rewind_marker"})
		}
		return nil
	})
	flush()
	return ev
}

// toolUpdateResultText extracts the output of a finished tool_call_update:
// the full output from rawOutput when present (Grok encodes it as a JSON array
// of byte values under "output" or "stdout"), else the human-readable text
// items of "content" (whose "diff" items carry file edits, not output text).
func toolUpdateResultText(u map[string]any) string {
	raw := common.Map(u["rawOutput"])
	for _, field := range []string{"output", "stdout"} {
		if s := decodeByteArray(raw[field]); s != "" {
			return s
		}
	}
	var lines []string
	for _, it := range common.Slice(u["content"]) {
		inner := common.Map(common.Map(it)["content"])
		if s := common.String(inner["text"]); s != "" {
			lines = append(lines, s)
		}
	}
	return strings.Join(lines, "\n")
}

// decodeByteArray converts a JSON array of numeric byte values into a string,
// returning "" when v is not such an array.
func decodeByteArray(v any) string {
	items := common.Slice(v)
	if len(items) == 0 {
		return ""
	}
	b := make([]byte, 0, len(items))
	for _, it := range items {
		n, ok := it.(float64)
		if !ok {
			return ""
		}
		b = append(b, byte(n))
	}
	return string(b)
}

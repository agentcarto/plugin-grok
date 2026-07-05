package grok

import (
	"context"
	"github.com/agentcarto/core/domain"
	"github.com/agentcarto/core/plugin"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestRewindConversation(t *testing.T) {
	ev := []domain.Event{{Kind: domain.EventUser, Text: "q1"}, {Kind: domain.EventAssistant, Text: "a1"}, {Kind: domain.EventUser, Text: "q2"}, {Kind: domain.EventAssistant, Text: "a2"}, {Kind: domain.EventMeta, Text: "1", RawType: "rewind_marker"}, {Kind: domain.EventUser, Text: "q2'"}}
	c := grokConversation(ev)
	var users []string
	for _, id := range c.ActivePath() {
		for _, e := range c.Nodes[id].Events {
			if e.Kind == domain.EventUser {
				users = append(users, e.Text)
			}
		}
	}
	if len(users) != 2 || users[0] != "q1" || users[1] != "q2'" {
		t.Fatalf("%v", users)
	}
}

// Grok has one model per session (summary.json current_model_id) and no
// per-turn attribution, so LoadConversation stamps that model onto every event.
func TestLoadConversationStampsModel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(`{"current_model_id":"grok-4"}`), 0600); err != nil {
		t.Fatal(err)
	}
	chat := `{"role":"user","text":"hello","timestamp":"2026-01-01T00:00:00Z"}
{"role":"assistant","text":"hi there","timestamp":"2026-01-01T00:00:01Z"}
`
	if err := os.WriteFile(filepath.Join(dir, "chat_history.jsonl"), []byte(chat), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := (&Plugin{}).LoadConversation(context.Background(), domain.SessionRef{Source: dir})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for _, node := range c.Nodes {
		for _, e := range node.Events {
			n++
			if e.Model != "grok-4" {
				t.Fatalf("event %q model=%q, want grok-4", e.Text, e.Model)
			}
		}
	}
	if n == 0 {
		t.Fatal("no events stamped")
	}
}

func TestRewindConversationKeepsDeadBranch(t *testing.T) {
	ev := []domain.Event{
		{Kind: domain.EventUser, Text: "test1"},
		{Kind: domain.EventAssistant, Text: "a1"},
		{Kind: domain.EventUser, Text: "test2"},
		{Kind: domain.EventAssistant, Text: "a2"},
		{Kind: domain.EventUser, Text: "test3"},
		{Kind: domain.EventAssistant, Text: "a3"},
		{Kind: domain.EventMeta, Text: "1", RawType: "rewind_marker"},
		{Kind: domain.EventUser, Text: "test2'"},
		{Kind: domain.EventAssistant, Text: "a2'"},
		{Kind: domain.EventUser, Text: "test3'"},
	}
	c := grokConversation(ev)
	active := map[string]bool{}
	var users []string
	for _, id := range c.ActivePath() {
		active[id] = true
		for _, e := range c.Nodes[id].Events {
			if e.Kind == domain.EventUser {
				users = append(users, e.Text)
			}
		}
	}
	if len(users) != 3 || users[0] != "test1" || users[1] != "test2'" || users[2] != "test3'" {
		t.Fatalf("users=%v", users)
	}
	var dead []string
	for id, n := range c.Nodes {
		if active[id] {
			continue
		}
		for _, e := range n.Events {
			if e.Kind == domain.EventUser {
				dead = append(dead, e.Text)
			}
		}
	}
	sort.Strings(dead)
	if len(dead) != 2 || dead[0] != "test2" || dead[1] != "test3" {
		t.Fatalf("dead=%v", dead)
	}
}

func TestScanGrokForkParentMetadata(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "repo", "child")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(`{"parent_session_id":"parent","session_id":"child","title":"fork"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(`{"type":"user","message":"fork"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{id: "grok", o: Options{SessionsDir: root}}
	res, err := p.Scan(context.Background(), plugin.ScanInput{})
	if err != nil {
		t.Fatal(err)
	}
	ss := res.Sessions
	if len(ss) != 1 || ss[0].ParentSessionID != "parent" {
		t.Fatalf("sessions=%#v", ss)
	}
}

// A fork's summary.json keeps a copy of the parent's info.id. The SessionID must use the
// directory name (physical ID) rather than that copy so it does not collide with the parent
// (regression guard against the "session becomes its own child" corruption).
func TestScanGrokForkSessionIDIsDirNotCopiedInfoID(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "repo", "childdir")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// info.id is the parent "parentid" (left over from the fork copy). The dir name is "childdir".
	if err := os.WriteFile(filepath.Join(dir, "summary.json"),
		[]byte(`{"parent_session_id":"parentid","session_kind":"fork","info":{"id":"parentid"},"title":"fork"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(`{"type":"user","message":"fork"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{id: "grok", o: Options{SessionsDir: root}}
	res, err := p.Scan(context.Background(), plugin.ScanInput{})
	if err != nil {
		t.Fatal(err)
	}
	ss := res.Sessions
	if len(ss) != 1 {
		t.Fatalf("sessions=%#v", ss)
	}
	if ss[0].SessionID != "childdir" {
		t.Fatalf("SessionID=%q want dir name childdir (not copied info.id)", ss[0].SessionID)
	}
	if ss[0].SessionID == ss[0].ParentSessionID {
		t.Fatalf("SessionID must not collide with the parent: %q", ss[0].SessionID)
	}
}

// The session-dir name is written with url.PathEscape, so Scan must decode it
// with PathUnescape: QueryUnescape turned a literal "+" in the cwd into a
// space (regression guard for /home/u/c++proj -> "/home/u/c  proj").
func TestScanDecodesCWDWithPlusSign(t *testing.T) {
	root := t.TempDir()
	cwd := "/home/u/c++proj"
	dir := filepath.Join(root, encode(cwd), "sess")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(`{"type":"user","message":"hi"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{id: "grok", o: Options{SessionsDir: root}}
	res, err := p.Scan(context.Background(), plugin.ScanInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].CWD != cwd {
		t.Fatalf("CWD did not survive the encode/decode round trip: %#v", res.Sessions)
	}
}

// StartedAt comes from the first timestamped event, not from the directory
// mtime (which is the *update* time and made every grok session look like it
// started when it was last touched).
func TestScanStartedAtUsesFirstEventTimestamp(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "repo", "sess")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	lines := `{"role":"user","content":"hi","timestamp":"2026-01-02T03:04:05Z"}` + "\n" +
		`{"role":"assistant","content":"yo","timestamp":"2026-01-02T03:05:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "chat_history.jsonl"), []byte(lines), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{id: "grok", o: Options{SessionsDir: root}}
	res, err := p.Scan(context.Background(), plugin.ScanInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("sessions=%#v", res.Sessions)
	}
	s := res.Sessions[0]
	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if !s.StartedAt.Equal(want) {
		t.Fatalf("StartedAt=%v want %v", s.StartedAt, want)
	}
	if s.StartedAt.Equal(s.UpdatedAt) {
		t.Fatal("StartedAt must not just mirror UpdatedAt")
	}
}

package grok

import (
	"context"
	"github.com/agentcarto/core/domain"
	"github.com/agentcarto/core/plugin"
	"os"
	"path/filepath"
	"sort"
	"testing"
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

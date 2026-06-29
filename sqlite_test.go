package grok

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/agentcarto/core/domain"
	_ "modernc.org/sqlite"
)

func makeDB(t *testing.T, schema string, exec ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	for _, q := range exec {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestReadSQLite(t *testing.T) {
	path := makeDB(t,
		`CREATE TABLE messages (role TEXT, content TEXT)`,
		`INSERT INTO messages VALUES ('user','hello'),('assistant','hi there'),('system','boot')`,
	)
	ev := readSQLite(path)
	if len(ev) != 3 {
		t.Fatalf("got %d events, want 3", len(ev))
	}
	want := []domain.EventKind{domain.EventUser, domain.EventAssistant, domain.EventSystem}
	for i, k := range want {
		if ev[i].Kind != k {
			t.Errorf("event %d kind = %q, want %q", i, ev[i].Kind, k)
		}
	}
	if ev[0].Text != "hello" {
		t.Errorf("text = %q, want hello", ev[0].Text)
	}
}

func TestReadSQLiteAlternateColumnsAndAgentRole(t *testing.T) {
	// "sender"/"body" are accepted aliases; the "agent" role maps to assistant.
	path := makeDB(t,
		`CREATE TABLE chat (sender TEXT, body TEXT)`,
		`INSERT INTO chat VALUES ('agent','from the agent')`,
	)
	ev := readSQLite(path)
	if len(ev) != 1 || ev[0].Kind != domain.EventAssistant || ev[0].Text != "from the agent" {
		t.Fatalf("events = %#v, want one assistant event", ev)
	}
}

func TestReadSQLiteSkipsTableWithoutTextColumn(t *testing.T) {
	// The first table has no recognizable text column and must be skipped in
	// favor of the one that does.
	path := makeDB(t,
		`CREATE TABLE meta (id INTEGER, note TEXT)`,
		`CREATE TABLE messages (role TEXT, content TEXT)`,
		`INSERT INTO meta VALUES (1,'irrelevant')`,
		`INSERT INTO messages VALUES ('user','the real message')`,
	)
	ev := readSQLite(path)
	if len(ev) != 1 || ev[0].Text != "the real message" {
		t.Fatalf("events = %#v, want the message-bearing table", ev)
	}
}

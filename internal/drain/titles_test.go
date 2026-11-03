package drain

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestClaudeRecordedTitlesPreferCustomAndIgnoreEmbeddedMetadata(t *testing.T) {
	d := t.TempDir()
	p := put(t, d, "p.jsonl", `{"type":"user","sessionId":"p","cwd":"/work/Runcity","message":{"role":"user","content":"Original task"}}`+"\n"+
		`{"type":"summary","summary":"Early summary"}`+"\n"+
		`{"type":"custom-title","sessionId":"p","customTitle":"First name"}`+"\n"+
		`{"type":"custom-title","sessionId":"p","customTitle":"Runcity"}`+"\n"+
		`{"type":"ai-title","sessionId":"p","aiTitle":"Generated later"}`+"\n"+
		`{"type":"assistant","sessionId":"p","message":{"role":"assistant","content":"{\"customTitle\":\"tool content must not rename the session\"}"}}`+"\n")
	b, err := Load("claude", p)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Sessions) != 1 || b.Sessions[0].Title != "Runcity" {
		t.Fatalf("title not preserved: %#v", b.Sessions)
	}
}

func TestClaudeTitleFallbacks(t *testing.T) {
	for _, tc := range []struct{ name, record, want string }{
		{"ai", `{"type":"ai-title","aiTitle":"Agent work"}`, "Agent work"},
		{"summary", `{"type":"summary","summary":"Recorded summary"}`, "Recorded summary"},
		{"project", `{"type":"user","slug":"random-session-slug"}`, "Runcity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := put(t, t.TempDir(), "p.jsonl", `{"type":"user","sessionId":"p","cwd":"C:\\work\\Runcity","message":{"role":"user","content":"Task"}}`+"\n"+tc.record+"\n")
			b, err := Load("claude", p)
			if err != nil {
				t.Fatal(err)
			}
			if b.Sessions[0].Title != tc.want {
				t.Fatalf("title=%q want=%q", b.Sessions[0].Title, tc.want)
			}
		})
	}
}

func TestCatalogCodexTitlesUseLastIndexLineThenSQLiteThenProject(t *testing.T) {
	d := t.TempDir()
	for _, id := range []string{"index", "database", "project"} {
		put(t, d, "sessions/2026/11/03/"+id+".jsonl", `{"type":"session_meta","payload":{"id":"`+id+`","cwd":"/work/Runcity"}}`+"\nINVALID body must never be decoded\n")
	}
	put(t, d, "session_index.jsonl", `{"id":"index","thread_name":"Old","updated_at":"2099-01-01T00:00:00Z"}`+"\ninvalid optional index line\n"+`{"id":"index","thread_name":"Runcity","updated_at":"2020-01-01T00:00:00Z"}`+"\n"+`{"id":"index","thread_name":" "}`+"\n")
	db, err := sql.Open("sqlite", filepath.Join(d, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`CREATE TABLE threads(id TEXT,title TEXT,name TEXT,first_user_message TEXT)`, `INSERT INTO threads VALUES('index','Stale','Stale name','private body'),('database','Generated title','Chosen database name','private body')`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := CatalogCodex(filepath.Join(d, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"index": "Runcity", "database": "Chosen database name", "project": "Runcity"}
	for _, s := range b.Sessions {
		if s.Title != want[s.ID] {
			t.Errorf("%s title=%q want=%q", s.ID, s.Title, want[s.ID])
		}
		if s.Events != nil {
			t.Fatal("catalog read a conversation body")
		}
	}
}

func TestCatalogCodexOldSQLiteAndMissingMetadata(t *testing.T) {
	d := t.TempDir()
	put(t, d, "sessions/p.jsonl", `{"type":"session_meta","payload":{"id":"p","title":"Header title"}}`+"\n")
	db, err := sql.Open("sqlite", filepath.Join(d, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE threads(id TEXT,title TEXT); INSERT INTO threads VALUES('p','Database title')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := CatalogCodex(filepath.Join(d, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	if b.Sessions[0].Title != "Database title" {
		t.Fatalf("old metadata schema not supported: %q", b.Sessions[0].Title)
	}
}

func TestCodexCopiedStoreDoesNotReadGlobalTitles(t *testing.T) {
	global := t.TempDir()
	t.Setenv("CODEX_HOME", global)
	put(t, global, "session_index.jsonl", `{"id":"p","thread_name":"Unrelated global title"}`+"\n")
	copied := t.TempDir()
	put(t, copied, "p.jsonl", `{"type":"session_meta","payload":{"id":"p","cwd":"/work/CopiedProject"}}`+"\nINVALID unrelated body\n")
	b, err := CatalogCodex(copied)
	if err != nil {
		t.Fatal(err)
	}
	if b.Sessions[0].Title != "CopiedProject" {
		t.Fatalf("copied store title=%q", b.Sessions[0].Title)
	}
}

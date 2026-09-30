package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sub", "archive.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func count(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestOpenCreatesSchema(t *testing.T) {
	s := openTest(t)
	for _, table := range []string{"chats", "topics", "users", "peers", "messages", "media", "message_media", "sync_cursors", "meta"} {
		if count(t, s, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table) != 1 {
			t.Errorf("table %s missing", table)
		}
	}
	v, ok, err := s.Meta(context.Background(), "schema_version")
	if err != nil || !ok || v != "3" {
		t.Fatalf("schema_version = %q %v %v", v, ok, err)
	}
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q %v", mode, err)
	}
	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d %v", fk, err)
	}
}

func TestReopenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta(context.Background(), "k", "v"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, ok, err := s.Meta(context.Background(), "k")
	if err != nil || !ok || v != "v" {
		t.Fatalf("meta after reopen = %q %v %v", v, ok, err)
	}
}

func TestOpenHandlesSpecialCharactersInPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Архив #1 %20", "archive.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetMeta(context.Background(), "k", "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database not at the requested path: %v", err)
	}
}

func TestMetaMissingKey(t *testing.T) {
	s := openTest(t)
	_, ok, err := s.Meta(context.Background(), "nope")
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

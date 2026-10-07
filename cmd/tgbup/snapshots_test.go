package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joeunderwood/tgbup/internal/backup"
	"github.com/joeunderwood/tgbup/internal/config"
	"github.com/joeunderwood/tgbup/internal/serve"
	"github.com/joeunderwood/tgbup/internal/store"
)

// The Snapshots tab against a real restic: a snapshot is listed with its
// command, restored into a folder of its own next to the archive, and the
// archive's history then links that copy. The config has no chats yet (a new
// Mac with backups set up first): a full restore would adopt the snapshot's
// config there, a copy must not.
func TestSnapshotCopyIsRestoredNextToTheArchive(t *testing.T) {
	restic := os.Getenv("TGBUP_TEST_RESTIC")
	if restic == "" {
		t.Skip("set TGBUP_TEST_RESTIC to a restic binary")
	}
	w, _, p := testWizard(t, "")
	ctx := context.Background()
	archive, repo := filepath.Join(p.Home, "Tgbup", "archive"), filepath.Join(p.Home, "Tgbup", "backup")
	for path, v := range map[string]any{
		"archive_dir": archive, "backup.repositories": []string{repo}, "deps.restic": restic,
	} {
		if err := config.SetValue(p.Config, strings.Split(path, "."), v); err != nil {
			t.Fatal(err)
		}
	}
	if err := config.SetSecrets(p.Secrets, map[string]string{"RESTIC_PASSWORD": "a test password 1"}); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(archive, "archive.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertChat(ctx, store.Chat{ID: 10, Type: store.ChatPrivate, Title: "Mom"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveBatch(ctx, store.Batch{ChatID: 10, Messages: []store.Message{{ChatID: 10, ID: 1, Date: 1772400000, Text: "hi", Raw: []byte{1}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := renderArchive(ctx, db, archive, false); err != nil {
		t.Fatal(err)
	}
	st, err := loadLocalSetup(w.env, false)
	if err != nil {
		t.Fatal(err)
	}
	o, err := backupOptions(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if res := backup.Init(ctx, o); res[0].Err != nil {
		t.Fatal(res[0].Err)
	}
	o.Update = latestUpdate(ctx, db)
	if r := backup.Run(ctx, db, o); !r.OK() {
		t.Fatalf("backup = %+v", r)
	}
	db.Close()
	cfgBefore, _ := os.ReadFile(p.Config)
	secBefore, _ := os.ReadFile(p.Secrets)

	b := &serveBackend{env: w.env}
	snaps, err := b.Snapshots(ctx, 0)
	if err != nil || len(snaps) != 1 {
		t.Fatalf("snapshots = %+v, %v", snaps, err)
	}
	sn := snaps[0]
	wantCopy := "~/Tgbup/archive-snapshots/" + sn.ID
	if sn.Restored || sn.Copy != wantCopy || sn.Changes == "" ||
		sn.Command != "tgbup restore --copy --from ~/Tgbup/backup --snapshot "+sn.ID {
		t.Fatalf("snapshot = %+v", sn)
	}
	if err := b.OpenCopy(0, sn.ID); !errors.Is(err, serve.ErrBadRequest) {
		t.Errorf("open before the restore = %v", err)
	}

	got, err := b.RestoreCopy(ctx, 0, sn.ID)
	if err != nil || !got.Restored {
		t.Fatalf("restore a copy = %+v, %v", got, err)
	}
	copyDir := filepath.Join(p.Home, "Tgbup", "archive-snapshots", sn.ID)
	for _, f := range []string{"archive.sqlite", "html/index.html", ".tgbup-copy-done"} {
		if _, err := os.Stat(filepath.Join(copyDir, f)); err != nil {
			t.Errorf("the copy lacks %s: %v", f, err)
		}
	}
	page, _ := os.ReadFile(filepath.Join(archive, "html", "history.html"))
	if !strings.Contains(string(page), "archive-snapshots/"+sn.ID+"/html/index.html") {
		t.Errorf("the archive's history does not link the copy:\n%s", page)
	}
	// The settings are as they were, to the byte.
	if after, _ := os.ReadFile(p.Config); string(after) != string(cfgBefore) {
		t.Errorf("the config changed:\n%s", after)
	}
	if after, _ := os.ReadFile(p.Secrets); string(after) != string(secBefore) {
		t.Error("the secrets changed")
	}
	if again, err := b.Snapshots(ctx, 0); err != nil || !again[0].Restored {
		t.Errorf("listed again = %+v, %v", again, err)
	}
}

// Ids and places from the page are checked before anything is read: an id
// also names the copy's folder.
func TestSnapshotCopyRefusesOddIDs(t *testing.T) {
	w, _, p := testWizard(t, "")
	if err := config.SetValue(p.Config, []string{"backup", "repositories"}, []string{filepath.Join(p.Home, "b")}); err != nil {
		t.Fatal(err)
	}
	b := &serveBackend{env: w.env}
	for _, bad := range []string{"../../etc", "latest", "ABCDEF12", "abcd123", "abcd12345", ""} {
		if _, err := b.RestoreCopy(context.Background(), 0, bad); !errors.Is(err, serve.ErrBadRequest) {
			t.Errorf("RestoreCopy(%q) = %v", bad, err)
		}
		if err := b.OpenCopy(0, bad); !errors.Is(err, serve.ErrBadRequest) {
			t.Errorf("OpenCopy(%q) = %v", bad, err)
		}
	}
	for _, place := range []int{-1, 1} {
		if err := b.OpenCopy(place, "abcd1234"); !errors.Is(err, serve.ErrBadRequest) {
			t.Errorf("OpenCopy(place %d) = %v", place, err)
		}
	}
	if err := b.OpenCopy(0, "abcd1234"); !errors.Is(err, serve.ErrBadRequest) {
		t.Errorf("open a copy never restored = %v", err)
	}
}

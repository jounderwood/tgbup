package backup

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// restic splits -o rclone.program on spaces; tgbup puts rclone's folder on PATH instead.
func TestRcloneFoundThroughPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	dir := filepath.Join(t.TempDir(), "with space")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "log")
	fake := filepath.Join(dir, "restic")
	script := "#!/bin/sh\necho \"$@\" > '" + log + "'\necho \"$PATH\" >> '" + log + "'\nenv | grep -c '^RESTIC_PASSWORD_FILE=' >> '" + log + "'\nexit 0\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RESTIC_PASSWORD_FILE", "/elsewhere")
	o := Options{Restic: fake, Rclone: filepath.Join(dir, "rclone"), Password: "pw"}
	if _, err := o.restic(context.Background(), "rclone:gdrive:x", "snapshots"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if strings.Contains(lines[0], "rclone.program") || !strings.HasPrefix(lines[1], dir+string(os.PathListSeparator)) {
		t.Fatalf("args %q, PATH %q", lines[0], lines[1])
	}
	// The user's own restic settings must not override tgbup's password.
	if lines[2] != "0" {
		t.Fatal("RESTIC_PASSWORD_FILE leaked into restic's environment")
	}
}

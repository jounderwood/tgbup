package deps

import (
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// bz2 holds "#!/bin/sh\necho restic\n" compressed with bzip2 (Go has only a decompressor).
var bz2 = []byte{
	0x42, 0x5a, 0x68, 0x39, 0x31, 0x41, 0x59, 0x26, 0x53, 0x59, 0xc0, 0xaa, 0xb4, 0x1b, 0x00, 0x00,
	0x02, 0x51, 0x80, 0x00, 0x10, 0x68, 0x00, 0x9a, 0x61, 0x9c, 0x00, 0x20, 0x00, 0x22, 0x81, 0xb5,
	0x3d, 0x4d, 0xa9, 0xb5, 0x0a, 0x60, 0x00, 0x01, 0x43, 0x61, 0x14, 0xa3, 0xd4, 0x9b, 0xe5, 0x39,
	0x93, 0xf1, 0x77, 0x24, 0x53, 0x85, 0x09, 0x0c, 0x0a, 0xab, 0x41, 0xb0,
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func zipped(name string, body []byte) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("rclone-v1/" + name)
	_, _ = w.Write(body)
	_ = zw.Close()
	return buf.Bytes()
}

func server(t *testing.T, files map[string][]byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/"
}

func platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

func noLook(string) (string, error) { return "", errors.New("not found") }

func TestBz2FixtureIsValid(t *testing.T) {
	var out bytes.Buffer
	if _, err := out.ReadFrom(bzip2.NewReader(bytes.NewReader(bz2))); err != nil || !strings.Contains(out.String(), "echo restic") {
		t.Fatalf("fixture: %q %v", out.String(), err)
	}
}

func TestEnsureDownloadsAndVerifies(t *testing.T) {
	zipBody := zipped(Exe("rclone"), []byte("rclone-binary"))
	base := server(t, map[string][]byte{"restic.bz2": bz2, "rclone.zip": zipBody})
	restic := Tool{Name: "restic", Version: "1", Assets: map[string]Asset{platform(): {File: "restic.bz2", SHA256: sum(bz2), Format: Bz2}}}
	rclone := Tool{Name: "rclone", Version: "1", Assets: map[string]Asset{platform(): {File: "rclone.zip", SHA256: sum(zipBody), Format: Zip}}}
	f := Finder{StateDir: t.TempDir(), BaseURL: map[string]string{"restic": base, "rclone": base}, LookPath: noLook,
		Exe: func() (string, error) { return filepath.Join(t.TempDir(), "tgbup"), nil }}

	for _, tool := range []Tool{restic, rclone} {
		path, err := f.Ensure(context.Background(), tool)
		if err != nil {
			t.Fatalf("%s: %v", tool.Name, err)
		}
		if filepath.Dir(path) != filepath.Join(f.StateDir, "bin", tool.Name+"-1") {
			t.Errorf("%s installed at %s", tool.Name, path)
		}
		if st, err := os.Stat(path); err != nil || st.Mode().Perm()&0o100 == 0 {
			t.Errorf("%s not executable: %v %v", tool.Name, st, err)
		}
		// Found without downloading again.
		if got, ok := f.Find(tool); !ok || got != path {
			t.Errorf("%s not found after install: %s %v", tool.Name, got, ok)
		}
	}
}

func TestEnsureRejectsWrongChecksum(t *testing.T) {
	base := server(t, map[string][]byte{"restic.bz2": bz2})
	tool := Tool{Name: "restic", Version: "1", Assets: map[string]Asset{platform(): {File: "restic.bz2", SHA256: strings.Repeat("0", 64), Format: Bz2}}}
	f := Finder{StateDir: t.TempDir(), BaseURL: map[string]string{"restic": base}, LookPath: noLook,
		Exe: func() (string, error) { return "/nowhere/tgbup", nil }}
	if _, err := f.Ensure(context.Background(), tool); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := f.Find(tool); ok {
		t.Fatal("a binary with a wrong checksum was kept")
	}
}

func TestFindOrder(t *testing.T) {
	dir := t.TempDir()
	write := func(p string) string {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tool := Tool{Name: "restic", Version: "1"}
	configured := write(filepath.Join(dir, "conf", "restic"))
	beside := write(filepath.Join(dir, "app", Exe("restic")))
	cached := write(filepath.Join(dir, "state", "bin", "restic-1", Exe("restic")))
	f := Finder{StateDir: filepath.Join(dir, "state"), Configured: configured,
		Exe:      func() (string, error) { return filepath.Join(dir, "app", "tgbup"), nil },
		LookPath: func(string) (string, error) { return "/usr/bin/restic", nil }}
	steps := []string{configured, beside, cached, "/usr/bin/restic"}
	for i, want := range steps {
		got, ok := f.Find(tool)
		if !ok || got != want {
			t.Fatalf("step %d: found %s, want %s", i, got, want)
		}
		switch i {
		case 0:
			f.Configured = ""
		case 1:
			os.Remove(beside)
		case 2:
			os.Remove(cached)
		}
	}
}

func TestPinnedAssetsCoverPlatforms(t *testing.T) {
	for _, tool := range []Tool{Restic, Rclone} {
		for _, p := range []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64"} {
			a, ok := tool.Assets[p]
			if !ok || len(a.SHA256) != 64 || a.File == "" {
				t.Errorf("%s %s: %+v", tool.Name, p, a)
			}
		}
	}
}

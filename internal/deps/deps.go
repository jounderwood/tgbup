// Package deps finds the restic and rclone binaries tgbup drives, downloading
// a pinned official release when none is installed. Checksums are compiled in,
// so a tampered download is refused.
package deps

import (
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type Format int

const (
	Bz2 Format = iota // a single compressed binary
	Zip               // an archive holding the binary somewhere inside
)

type Asset struct {
	File   string
	SHA256 string
	Format Format
}

type Tool struct {
	Name    string
	Version string
	// Assets by "GOOS/GOARCH".
	Assets map[string]Asset
}

// Pinned versions; sums are from the projects' official SHA256SUMS files.
var (
	Restic = Tool{Name: "restic", Version: "0.19.1", Assets: map[string]Asset{
		"darwin/amd64":  {"restic_0.19.1_darwin_amd64.bz2", "c38d579622cf602f665234c5a8c315030b6cf70656028fe6dc29a786b60e5f35", Bz2},
		"darwin/arm64":  {"restic_0.19.1_darwin_arm64.bz2", "7be0a144ccc377880f294204aa271d76e4b79554b42a751151d425ce6ebac143", Bz2},
		"linux/amd64":   {"restic_0.19.1_linux_amd64.bz2", "f415415624dcc452f2a02b8c33641791a8c6d6d3b65bbb3543fcf9a25151585c", Bz2},
		"linux/arm64":   {"restic_0.19.1_linux_arm64.bz2", "a5f64aaab53d51e311fa3829124c5b703f2d14cf187d8640b6be3b2b49376465", Bz2},
		"windows/amd64": {"restic_0.19.1_windows_amd64.zip", "da948ad707ed690426473aaba2046cd61f8f90f6f0e7dab6be0d5796531de67d", Zip},
	}}
	Rclone = Tool{Name: "rclone", Version: "1.75.1", Assets: map[string]Asset{
		"darwin/amd64":  {"rclone-v1.75.1-osx-amd64.zip", "29253d0288b8fbbac46baad6e5f6add6cb01d462c79f10805bbd4631c4cdf82c", Zip},
		"darwin/arm64":  {"rclone-v1.75.1-osx-arm64.zip", "c61d7a371c62bcbbe882c3423aa4b8bf63485c248dd0f692997b8f0c3f6d0c6f", Zip},
		"linux/amd64":   {"rclone-v1.75.1-linux-amd64.zip", "982b5aa772841168f8e380f139e9e787b2a105403e32b94da8676a0e1c0a13ab", Zip},
		"linux/arm64":   {"rclone-v1.75.1-linux-arm64.zip", "03f2504174034b6d004152ed7369251c9a9ec1f7e0836eda420f5c7a5ec0dff9", Zip},
		"windows/amd64": {"rclone-v1.75.1-windows-amd64.zip", "200eb602c126d82aa38b51e0f6b9ae837473ff99b51278d3f6f837574c494d6e", Zip},
	}}
)

// DefaultBaseURL is where each tool's release files are downloaded from.
var DefaultBaseURL = map[string]string{
	"restic": "https://github.com/restic/restic/releases/download/v" + Restic.Version + "/",
	"rclone": "https://github.com/rclone/rclone/releases/download/v" + Rclone.Version + "/",
}

// Finder locates a tool: the configured path, then next to the tgbup binary,
// then tgbup's own download, then PATH.
type Finder struct {
	StateDir   string
	Configured string
	BaseURL    map[string]string
	HTTP       *http.Client
	LookPath   func(string) (string, error)
	Exe        func() (string, error)
}

func NewFinder(stateDir, configured string) Finder {
	return Finder{StateDir: stateDir, Configured: configured, BaseURL: DefaultBaseURL, HTTP: http.DefaultClient,
		LookPath: exec.LookPath, Exe: os.Executable}
}

// Exe adds the platform's executable suffix.
func Exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func (f Finder) cached(t Tool) string {
	return filepath.Join(f.StateDir, "bin", t.Name+"-"+t.Version, Exe(t.Name))
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func (f Finder) Find(t Tool) (string, bool) {
	if f.Configured != "" && isFile(f.Configured) {
		return f.Configured, true
	}
	if exe, err := f.Exe(); err == nil {
		if p := filepath.Join(filepath.Dir(exe), Exe(t.Name)); isFile(p) {
			return p, true
		}
	}
	if p := f.cached(t); isFile(p) {
		return p, true
	}
	if p, err := f.LookPath(t.Name); err == nil {
		return p, true
	}
	return "", false
}

// Ensure finds the tool or downloads its pinned release.
func (f Finder) Ensure(ctx context.Context, t Tool) (string, error) {
	if f.Configured != "" && !isFile(f.Configured) {
		return "", fmt.Errorf("%s: configured path %s does not exist", t.Name, f.Configured)
	}
	if p, ok := f.Find(t); ok {
		return p, nil
	}
	return f.download(ctx, t)
}

func (f Finder) download(ctx context.Context, t Tool) (string, error) {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	a, ok := t.Assets[platform]
	if !ok {
		return "", fmt.Errorf("%s: no pinned release for %s; install it and set deps.%s in the config", t.Name, platform, t.Name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.BaseURL[t.Name]+a.File, nil)
	if err != nil {
		return "", err
	}
	client := f.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", t.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", t.Name, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 200<<20))
	if err != nil {
		return "", fmt.Errorf("download %s: %w", t.Name, err)
	}
	got := sha256.Sum256(body)
	if hex.EncodeToString(got[:]) != a.SHA256 {
		return "", fmt.Errorf("download %s: checksum mismatch, refusing to install it", t.Name)
	}
	bin, err := extract(body, a.Format, Exe(t.Name))
	if err != nil {
		return "", fmt.Errorf("unpack %s: %w", t.Name, err)
	}
	dst := f.cached(t)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	return dst, os.Rename(tmp.Name(), dst)
}

func extract(body []byte, format Format, name string) ([]byte, error) {
	switch format {
	case Bz2:
		return io.ReadAll(bzip2.NewReader(bytes.NewReader(body)))
	case Zip:
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return nil, err
		}
		for _, zf := range zr.File {
			if filepath.Base(zf.Name) != name || zf.FileInfo().IsDir() {
				continue
			}
			rc, err := zf.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
		return nil, fmt.Errorf("%s not in the archive", name)
	}
	return nil, errors.New("unknown format")
}

// Version runs "<tool> version" for display.
func Version(ctx context.Context, path string) string {
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "unknown version"
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line
}

package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gitlab.com/joeunderwood/tgbup/internal/config"
)

func writeFile(t *testing.T, dir, name, body string, perm os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), perm); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAppliesDefaultsAndParses(t *testing.T) {
	p := writeFile(t, t.TempDir(), "config.yaml", `
archive_dir: ~/Arch
targets:
  folders: ["Семья"]
  include: ["@Some_Group", -1001234567890]
media:
  max_file_size: 2GB
  skip_kinds: [sticker]
backup:
  repositories: ["~/restic"]
  retention: {last: 2}
`, 0o644)

	cfg, err := config.Load(p, "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ArchiveDir != "/home/u/Arch" {
		t.Errorf("ArchiveDir = %q", cfg.ArchiveDir)
	}
	if cfg.Backup.Repositories[0] != "/home/u/restic" {
		t.Errorf("repo = %q", cfg.Backup.Repositories[0])
	}
	if cfg.Media.MaxFileSize != 2<<30 {
		t.Errorf("MaxFileSize = %d", cfg.Media.MaxFileSize)
	}
	if cfg.Media.Workers != 4 {
		t.Errorf("Workers = %d, want default 4", cfg.Media.Workers)
	}
	if cfg.Sync.RunTimeBudget.D() != 2*time.Hour || cfg.Sync.MaxFloodWait.D() != 5*time.Minute {
		t.Errorf("sync defaults = %+v", cfg.Sync)
	}
	if cfg.Backup.Retention != (config.Retention{Last: 2, Daily: 7, Weekly: 4, Monthly: 12}) {
		t.Errorf("Retention = %+v", cfg.Backup.Retention)
	}
	if cfg.Notify.SummaryEvery.D() != 7*24*time.Hour {
		t.Errorf("SummaryEvery = %v", cfg.Notify.SummaryEvery.D())
	}
	if u, ok := cfg.Targets.Include[0].Username(); !ok || u != "some_group" {
		t.Errorf("include[0] = %q %v", u, ok)
	}
	if id, ok := cfg.Targets.Include[1].ID(); !ok || id != -1001234567890 {
		t.Errorf("include[1] = %d %v", id, ok)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"no targets":   {`archive_dir: /a`, "no chats chosen yet"},
		"unknown kind": {"targets: {folders: [x]}\nmedia: {skip_kinds: [gif]}", `unknown kind "gif"`},
		"bad workers":  {"targets: {folders: [x]}\nmedia: {workers: 0}", "media.workers must be 1..16"},
		"bad peer":     {"targets: {include: [abc]}", `targets: invalid peer "abc"`},
		"bad size":     {"targets: {folders: [x]}\nmedia: {max_file_size: lots}", `invalid size "LOTS"`},
		"bad topic":    {"targets: {include: [-1001], exclude_topics: {-1001: [0]}}", "exclude_topics -1001: invalid topic id 0"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := writeFile(t, t.TempDir(), "c.yaml", tc.body, 0o644)
			_, err := config.Load(p, "/h")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestParseByteSize(t *testing.T) {
	cases := map[string]int64{"0": 0, "512": 512, "10KB": 10 << 10, "1.5MB": 3 << 19, "2GB": 2 << 30, "1tb": 1 << 40}
	for in, want := range cases {
		got, err := config.ParseByteSize(in)
		if err != nil || got != want {
			t.Errorf("ParseByteSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := config.ParseByteSize("-1GB"); err == nil {
		t.Error("negative size accepted")
	}
}

func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{"30d": 30 * 24 * time.Hour, "24h": 24 * time.Hour, "5m": 5 * time.Minute}
	for in, want := range cases {
		got, err := config.ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := config.ParseDuration("xd"); err == nil {
		t.Error("bad duration accepted")
	}
}

func TestResolvePaths(t *testing.T) {
	env := map[string]string{"HOME": "/h"}
	getenv := func(k string) string { return env[k] }
	p, err := config.ResolvePaths(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if p.Config != "/h/.config/tgbup/config.yaml" || p.Secrets != "/h/.config/tgbup/secrets.env" || p.StateDir != "/h/.local/state/tgbup" {
		t.Errorf("defaults = %+v", p)
	}
	if p.Session(false) != "/h/.local/state/tgbup/session" || p.Lock() != "/h/.local/state/tgbup/run.lock" {
		t.Errorf("session/lock = %s %s", p.Session(false), p.Lock())
	}
	if p.Session(true) != "/h/.local/state/tgbup/session-test" {
		t.Errorf("test-DC session = %s", p.Session(true))
	}

	env["XDG_CONFIG_HOME"] = "/x"
	env["TGBUP_STATE_DIR"] = "/s"
	p, _ = config.ResolvePaths(getenv)
	if p.Config != "/x/tgbup/config.yaml" || p.StateDir != "/s" {
		t.Errorf("overrides = %+v", p)
	}

	if _, err := config.ResolvePaths(func(string) string { return "" }); err == nil {
		t.Error("missing HOME accepted")
	}
}

func TestLoadExcludeTopics(t *testing.T) {
	p := writeFile(t, t.TempDir(), "c.yaml", "targets:\n  include: [-1001]\n  exclude_topics:\n    -1001: [5, 12] # Family: Flood, Memes\n", 0o600)
	cfg, err := config.Load(p, "/h")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Targets.ExcludeTopics[-1001]; !slices.Equal(got, []int{5, 12}) {
		t.Fatalf("exclude_topics = %v", cfg.Targets.ExcludeTopics)
	}
}

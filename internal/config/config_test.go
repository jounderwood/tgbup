package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/joeunderwood/tgbup/internal/config"
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
	if cfg.Backup.Retention != (config.Retention{Last: 2, Daily: config.KeepAll, Weekly: 4, Monthly: 12}) {
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
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	getenv := func(k string) string { return env[k] }
	j := func(parts ...string) string { return filepath.Join(append([]string{home}, parts...)...) }
	p, err := config.ResolvePaths(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if p.Config != j("Tgbup", "config.yaml") || p.Logs != j("Tgbup", "logs") ||
		p.Secrets != j(".local", "state", "tgbup", "secrets.env") || p.StateDir != j(".local", "state", "tgbup") {
		t.Errorf("defaults = %+v", p)
	}
	if p.Session(false) != j(".local", "state", "tgbup", "session") || p.Lock() != j(".local", "state", "tgbup", "run.lock") ||
		p.Session(true) != j(".local", "state", "tgbup", "session-test") || p.RunLog() != j("Tgbup", "logs", "run.log") {
		t.Errorf("session/lock/log = %s %s %s", p.Session(false), p.Lock(), p.RunLog())
	}

	// A setup from before ~/Tgbup keeps its files.
	for _, f := range []string{"config.yaml", "secrets.env"} {
		if err := os.MkdirAll(j(".config", "tgbup"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(j(".config", "tgbup", f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p, _ = config.ResolvePaths(getenv)
	if p.Config != j(".config", "tgbup", "config.yaml") || p.Secrets != j(".config", "tgbup", "secrets.env") ||
		p.Logs != j(".local", "state", "tgbup", "logs") {
		t.Errorf("old setup = %+v", p)
	}

	env["TGBUP_CONFIG"], env["TGBUP_SECRETS"], env["TGBUP_STATE_DIR"] = "/t/config.yaml", "/t/secrets.env", "/s"
	p, _ = config.ResolvePaths(getenv)
	if p.Config != "/t/config.yaml" || p.Secrets != "/t/secrets.env" || p.StateDir != "/s" || p.Logs != "/t/logs" {
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

func TestParseTimes(t *testing.T) {
	got, err := config.ParseTimes([]string{"18:30", " 6:05", "06:05", "0:00"})
	if err != nil || len(got) != 3 || got[0].String() != "00:00" || got[1].String() != "06:05" || got[2].String() != "18:30" {
		t.Fatalf("times = %v %v", got, err)
	}
	for _, bad := range [][]string{{}, {"24:00"}, {"6"}, {"06:60"}, {"6:30pm"}, {"x"}, {"6:30 18:30"}} {
		if _, err := config.ParseTimes(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if c := config.Default().Schedule.Clocks(); len(c) != 4 || c[1].String() != "06:30" {
		t.Errorf("default = %v", c)
	}
}

func TestRetentionKeepAll(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) (config.Config, error) {
		p := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(p, []byte("targets: {folders: [x]}\nbackup:\n  retention: "+body+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return config.Load(p, dir)
	}
	cfg, err := write("{daily: 90, weekly: all, monthly: unlimited}")
	if err != nil || cfg.Backup.Retention != (config.Retention{Last: 4, Daily: 90, Weekly: config.KeepAll, Monthly: config.KeepAll}) {
		t.Fatalf("retention = %+v, %v", cfg.Backup.Retention, err)
	}
	if cfg.Backup.Retention.Weekly.Restic() != "unlimited" || cfg.Backup.Retention.Daily.Restic() != "90" {
		t.Fatalf("restic values = %s %s", cfg.Backup.Retention.Weekly.Restic(), cfg.Backup.Retention.Daily.Restic())
	}
	for _, bad := range []string{"{daily: -3}", "{daily: often}", "{last: 0, daily: 0, weekly: 0, monthly: 0}"} {
		if _, err := write(bad); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}

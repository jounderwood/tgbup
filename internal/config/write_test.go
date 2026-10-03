package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gitlab.com/joeunderwood/tgbup/internal/config"
)

func includeIDs(t *testing.T, cfg config.Config) []int64 {
	t.Helper()
	var out []int64
	for _, r := range cfg.Targets.Include {
		id, ok := r.ID()
		if !ok {
			t.Fatalf("include %q is not a numeric id", string(r))
		}
		out = append(out, id)
	}
	return out
}

func TestWriteTargetsKeepsOtherSettings(t *testing.T) {
	path := writeFile(t, t.TempDir(), "config.yaml", `# мой архив
archive_dir: ~/Arch # где лежит
targets:
  folders: ["Семья"]
  exclude: [1]
media:
  workers: 2 # медленный диск
`, 0o644)
	titles := map[int64]string{-1000000000001: "Семейный\nфорум #1 \"кавычки\" 🏡", 10: "Мама"}

	if err := config.WriteTargets(path, config.Selected{Chats: []int64{-1000000000001, 10}, ChatTitles: titles}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path, "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	if got := includeIDs(t, cfg); !slices.Equal(got, []int64{-1000000000001, 10}) {
		t.Errorf("include = %v", got)
	}
	if len(cfg.Targets.Folders) != 0 || len(cfg.Targets.Exclude) != 0 {
		t.Errorf("folders/exclude kept: %+v", cfg.Targets)
	}
	if cfg.Media.Workers != 2 || cfg.ArchiveDir != "/home/u/Arch" {
		t.Errorf("other settings lost: %+v", cfg)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{"# мой архив", "# медленный диск", "# Семейный форум #1", "# Мама"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("config lost %q:\n%s", want, raw)
		}
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
}

// Titles come from other people; whatever they contain must not make the
// config unwritable or crash the encoder.
func TestWriteTargetsCleansTitles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	titles := map[int64]string{1: "звонок\x07\x1b[31m", 2: "bad\xff\xfe", 3: "\u202eперевёрнуто", 4: "семья 👨\u200d👩\u200d👧"}
	if err := config.WriteTargets(path, config.Selected{Chats: []int64{1, 2, 3, 4}, ChatTitles: titles}); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path, "/home/u"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{"# звонок[31m", "# bad", "# перевёрнуто", "# семья 👨\u200d👩\u200d👧"} {
		if !strings.Contains(string(raw), want+"\n") {
			t.Errorf("config lacks comment %q:\n%s", want, raw)
		}
	}
}

func TestWriteTargetsCreatesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tgbup", "config.yaml")
	if err := config.WriteTargets(path, config.Selected{Chats: []int64{5}, ChatTitles: map[int64]string{5: "Я"}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path, "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	if got := includeIDs(t, cfg); !slices.Equal(got, []int64{5}) || cfg.ArchiveDir != "/home/u/Tgbup/archive" {
		t.Fatalf("new config = %+v", cfg)
	}
}

func TestWriteTargetsRefusesBadInput(t *testing.T) {
	dir := t.TempDir()
	if err := config.WriteTargets(filepath.Join(dir, "a.yaml"), config.Selected{}); err == nil {
		t.Error("empty selection written")
	}
	list := writeFile(t, dir, "list.yaml", "- not a mapping\n", 0o600)
	if err := config.WriteTargets(list, config.Selected{Chats: []int64{1}}); err == nil {
		t.Error("non-mapping config overwritten")
	}
	if raw, _ := os.ReadFile(list); string(raw) != "- not a mapping\n" {
		t.Errorf("file changed on error: %q", raw)
	}
}

func TestWriteTargetsExcludeTopics(t *testing.T) {
	path := writeFile(t, t.TempDir(), "config.yaml", "archive_dir: /a\ntargets:\n  include: [1]\n  exclude_topics: {-1001: [7]}\n", 0o600)
	sel := config.Selected{
		Chats:         []int64{-1001, 10},
		ChatTitles:    map[int64]string{-1001: "Family", 10: "Mom"},
		ExcludeTopics: map[int64][]int{-1001: {12, 5}, -2002: {3}, 10: nil},
		TopicTitles:   map[int64]map[int]string{-1001: {5: "Flood", 12: "Memes"}},
	}
	if err := config.WriteTargets(path, sel); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "-1001: [5, 12] # Family: Flood, Memes\n") {
		t.Errorf("exclude_topics not written as expected:\n%s", raw)
	}
	cfg, err := config.Load(path, "/h")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Targets.ExcludeTopics) != 1 || !slices.Equal(cfg.Targets.ExcludeTopics[-1001], []int{5, 12}) {
		t.Fatalf("exclude_topics = %v (unselected chats must be dropped)", cfg.Targets.ExcludeTopics)
	}

	if err := config.WriteTargets(path, config.Selected{Chats: []int64{-1001}}); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); strings.Contains(string(raw), "\n  exclude_topics:") {
		t.Errorf("empty exclusions left the key behind:\n%s", raw)
	}
}

// A config kept in a dotfiles repo and symlinked into place must stay a symlink.
func TestWriteTargetsKeepsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := writeFile(t, dir, "real.yaml", "archive_dir: /a\n", 0o600)
	link := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteTargets(link, config.Selected{Chats: []int64{5}}); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("config is no longer a symlink: %v %v", st.Mode(), err)
	}
	if raw, _ := os.ReadFile(real); !strings.Contains(string(raw), "- 5") {
		t.Errorf("symlink target not updated:\n%s", raw)
	}
}

func TestWriteTargetsAddsOptionsReferenceOnce(t *testing.T) {
	path := writeFile(t, t.TempDir(), "config.yaml", "archive_dir: /a # mine\n", 0o600)
	for range 2 {
		if err := config.WriteTargets(path, config.Selected{Chats: []int64{5}}); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(path)
	s := string(raw)
	if n := strings.Count(s, config.OptionsMarker); n != 1 {
		t.Fatalf("options reference appears %d times:\n%s", n, s)
	}
	for _, want := range []string{"# mine", "#   max_file_size: 0", "#   skip_kinds: []", "#   workers: 4",
		"#   run_time_budget: 2h ", "#   max_flood_wait: 5m ", "#   exclude_topics:", "# deps:"} {
		if !strings.Contains(s, want) {
			t.Errorf("config lacks %q:\n%s", want, s)
		}
	}
	if _, err := config.Load(path, "/h"); err != nil {
		t.Fatal(err)
	}
}

func TestRenderTargetsMatchesWhatIsWritten(t *testing.T) {
	path := writeFile(t, t.TempDir(), "config.yaml", "archive_dir: /a\n", 0o600)
	sel := config.Selected{Chats: []int64{5}, ChatTitles: map[int64]string{5: "Mom"}}
	preview, err := config.RenderTargets(path, sel)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "archive_dir: /a\n" {
		t.Fatal("rendering changed the file")
	}
	if err := config.WriteTargets(path, sel); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != string(preview) {
		t.Fatalf("written config differs from the preview:\n%s\n---\n%s", raw, preview)
	}
}

func TestSetValueCreatesAndKeepsTheRest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := config.SetValue(path, []string{"archive_dir"}, "~/Arch"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithoutTargets(path, "/home/u")
	if err != nil || cfg.ArchiveDir != "/home/u/Arch" {
		t.Fatalf("archive_dir = %q, %v", cfg.ArchiveDir, err)
	}
	if err := config.WriteTargets(path, config.Selected{Chats: []int64{10}, ChatTitles: map[int64]string{10: "Мама"}}); err != nil {
		t.Fatal(err)
	}
	repos := []string{"~/iCloud/tgbup-restic", "rclone:gdrive:tgbup-restic"}
	if err := config.SetValue(path, []string{"backup", "repositories"}, repos); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load(path, "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Backup.Repositories, []string{"/home/u/iCloud/tgbup-restic", "rclone:gdrive:tgbup-restic"}) {
		t.Errorf("repositories = %v", cfg.Backup.Repositories)
	}
	if got := includeIDs(t, cfg); !slices.Equal(got, []int64{10}) || cfg.Backup.Retention.Daily != 7 {
		t.Errorf("other settings lost: %v %+v", got, cfg.Backup)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "# Мама") || strings.Count(string(raw), config.OptionsMarker) != 1 {
		t.Errorf("comments not kept once:\n%s", raw)
	}
}

func TestSetValueRefusesInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.SetValue(path, []string{"media", "workers"}, 99); err == nil {
		t.Fatal("invalid workers written")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("file created for an invalid config")
	}
}

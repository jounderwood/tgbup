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

	if err := config.WriteTargets(path, []int64{-1000000000001, 10}, titles); err != nil {
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

func TestWriteTargetsCreatesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tgbup", "config.yaml")
	if err := config.WriteTargets(path, []int64{5}, map[int64]string{5: "Я"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path, "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	if got := includeIDs(t, cfg); !slices.Equal(got, []int64{5}) || cfg.ArchiveDir != "/home/u/TelegramArchive" {
		t.Fatalf("new config = %+v", cfg)
	}
}

func TestWriteTargetsRefusesBadInput(t *testing.T) {
	dir := t.TempDir()
	if err := config.WriteTargets(filepath.Join(dir, "a.yaml"), nil, nil); err == nil {
		t.Error("empty selection written")
	}
	list := writeFile(t, dir, "list.yaml", "- not a mapping\n", 0o600)
	if err := config.WriteTargets(list, []int64{1}, nil); err == nil {
		t.Error("non-mapping config overwritten")
	}
	if raw, _ := os.ReadFile(list); string(raw) != "- not a mapping\n" {
		t.Errorf("file changed on error: %q", raw)
	}
}

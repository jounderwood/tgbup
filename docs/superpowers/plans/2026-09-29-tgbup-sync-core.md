# tgbup — ядро выгрузки: план реализации (1 из 3)

> **Выполнен.** Актуальное устройство — `docs/PROJECT.md`, решения — в спеках (раздел «Расхождения с реализацией»). План не поддерживается.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** бинарник `tgbup` с командами `login` и `sync`, который инкрементально выкачивает выбранные чаты Telegram (включая форумы, служебные сообщения и медиа) в SQLite + `media/`.

**Architecture:** Go-бинарник на `gotd/td`. Пакеты: `config` (YAML + секреты), `store` (SQLite, источник правды), `convert` (gotd-сообщения → строки БД, без сети), `tgclient` (тонкий слой над `*tg.Client` + выбор чатов + подключение), `syncer` (курсоры, дозагрузка, ошибки), `media` (очередь загрузки с докачкой). `syncer` и `media` зависят от интерфейса `tgclient.Telegram` и тестируются на фейке.

**Tech Stack:** Go ≥ 1.25, `github.com/gotd/td`, `github.com/gotd/contrib`, `modernc.org/sqlite`, `go.yaml.in/yaml/v3`, `golang.org/x/term`, `golang.org/x/time`; `gotestsum`, `golangci-lint`.

**Spec:** `docs/superpowers/specs/2026-09-28-tgbup-design.md` (разделы 3–8, 12, 13). Планы 2 (render) и 3 (backup, notify, `run`, launchd) — отдельно.

## Global Constraints

- Go ≥ 1.25 (требование `gotd/td`, `go 1.25.0` в его go.mod). Модуль: `gitlab.com/joeunderwood/tgbup`.
- Сборка без cgo (`CGO_ENABLED=0`); никаких macOS-специфичных API (переезд на NAS/Linux).
- Зависимости только из списка в Tech Stack; любая новая — проверить лицензию (разрешены MIT, Apache-2.0, BSD-2/3, ISC, MPL-2.0, Unlicense, CC0).
- Пути: `~/.config/tgbup/config.yaml` (`TGBUP_CONFIG`), `~/.config/tgbup/secrets.env` (`TGBUP_SECRETS`, chmod 600, поддержка `*_FILE`), `~/.local/state/tgbup/` (`TGBUP_STATE_DIR`: `session`, `run.lock`); учитывать `XDG_CONFIG_HOME`/`XDG_STATE_HOME`.
- Данные: `<archive_dir>/archive.sqlite`, `<archive_dir>/media/<kind>/<telegram_id>.<ext>`.
- ID чатов — формат Bot API: пользователь `id`, группа `-id`, канал/супергруппа `-1000000000000 - id`.
- Сообщение сохраняется в виде первой выгрузки (`ON CONFLICT DO NOTHING`); правки/удаления не отслеживаются.
- Каждая пачка (≤100 сообщений) + её users/media + сдвиг курсора — одна транзакция.
- Медиа: не больше 5 попыток (по одной за запуск), потом `failed`. Ошибки FloodWait/отмены попыткой не считаются.
- Комментарии в коде — только когда неочевидно «почему». Коммиты — conventional (`feat:`, `test:`, `chore:`…), с attribution-трейлером, который задаёт ваш harness.

## Review Focus

1. **Семантика `HistoryAfter` на живом Telegram** (`offset_id = after+1`, `add_offset = -limit`, `min_id = after`): между запусками не должно теряться ни одного сообщения. Юнит-тест фиксирует параметры запроса и фильтрацию (Task 7); на живом аккаунте — ручной smoke (Task 12, шаг «инкремент»).
2. **Папка содержит пиры, которых нет в диалогах** (Saved Messages / `InputPeerSelf`, покинутые чаты): выбор чатов не падает и просто их игнорирует — тест в Task 8.
3. **Прерывание (Ctrl+C / SIGTERM) посреди загрузки файла:** `.part` остаётся, статус `pending`, попытка не засчитана, следующий запуск докачивает — тест в Task 11.
4. **Враждебные имена файлов** (`../../x`, юникод, пробелы): путь на диске строится только из Telegram ID и белого списка расширений — тест в Task 11.
5. **`MessageEmpty` внутри страницы истории** (сообщение удалили между запросами): пропускается, курсор всё равно сдвигается за него — тест в Task 10.

---

## Структура файлов

```
go.mod, go.sum, Makefile, .gitignore, .golangci.yml, README.md
cmd/tgbup/main.go          # диспетчер подкоманд, Env
cmd/tgbup/setup.go         # загрузка путей/конфига/секретов
cmd/tgbup/login.go         # tgbup login
cmd/tgbup/sync.go          # tgbup sync
cmd/tgbup/lock.go          # flock на run.lock
internal/config/{config,paths,secrets}.go
internal/store/store.go, migrations/001_init.sql, types.go, chats.go, batch.go, media.go
internal/convert/{ids,message,media}.go
internal/tgclient/{api,errors,targets,client,login}.go
internal/syncer/syncer.go
internal/media/{path,downloader}.go
```

---

### Task 1: Тулчейн и каркас

**Files:**
- Create: `go.mod`, `Makefile`, `.gitignore`, `.golangci.yml`, `cmd/tgbup/main.go`
- Test: `cmd/tgbup/main_test.go`

**Interfaces:**
- Produces: `type Env struct{ Stdin io.Reader; Stdout, Stderr io.Writer; Getenv func(string) string }`, `func run(ctx context.Context, args []string, env Env) int` — в следующие задачи добавляются ветки `login`, `sync`.

- [ ] **Step 1: Обновить Go и поставить инструменты**

```bash
brew upgrade go || brew install go
go version   # ожидается go1.25 или новее
brew install gotestsum golangci-lint
```

Если `go version` < 1.25 — остановиться и сообщить.

- [ ] **Step 2: Инициализировать модуль**

```bash
go mod init gitlab.com/joeunderwood/tgbup
go mod edit -go=1.25
```

- [ ] **Step 3: Написать падающий тест**

`cmd/tgbup/main_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func testEnv() (Env, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return Env{
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errOut,
		Getenv: func(string) string { return "" },
	}, &out, &errOut
}

func TestRunWithoutArgsPrintsUsage(t *testing.T) {
	env, _, errOut := testEnv()
	if code := run(context.Background(), nil, env); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "usage: tgbup") {
		t.Fatalf("usage not printed: %q", errOut.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	env, _, errOut := testEnv()
	if code := run(context.Background(), []string{"nope"}, env); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), `unknown command "nope"`) {
		t.Fatalf("got %q", errOut.String())
	}
}

func TestRunVersion(t *testing.T) {
	env, out, _ := testEnv()
	if code := run(context.Background(), []string{"version"}, env); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if strings.TrimSpace(out.String()) != "dev" {
		t.Fatalf("got %q", out.String())
	}
}
```

- [ ] **Step 4: Убедиться, что тест падает**

Run: `go test ./cmd/tgbup/`
Expected: FAIL — `undefined: Env`, `undefined: run`.

- [ ] **Step 5: Реализация**

`cmd/tgbup/main.go`:

```go
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

var version = "dev"

const usage = `usage: tgbup <command> [flags]

commands:
  login     interactive Telegram login, stores the session
  sync      download new messages and media
  version   print version
`

type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], Env{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv}))
}

func run(ctx context.Context, args []string, env Env) int {
	if len(args) == 0 {
		fmt.Fprint(env.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(env.Stdout, version)
		return 0
	default:
		fmt.Fprintf(env.Stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
```

`Makefile`:

```make
.PHONY: build test lint

build:
	CGO_ENABLED=0 go build -ldflags "-X main.version=$$(git describe --tags --always --dirty)" -o bin/tgbup ./cmd/tgbup

test:
	gotestsum -- -race ./...

lint:
	golangci-lint run ./...
```

`.gitignore`:

```
/bin/
.DS_Store
*.part
secrets.env
```

`.golangci.yml`:

```yaml
version: "2"
linters:
  default: standard
```

- [ ] **Step 6: Прогнать тесты и линтер**

Run: `make test && make lint && make build`
Expected: тесты PASS, линтер без замечаний, появился `bin/tgbup`.

- [ ] **Step 7: Commit**

```bash
git add go.mod Makefile .gitignore .golangci.yml cmd/tgbup/main.go cmd/tgbup/main_test.go
git commit -m "chore: scaffold tgbup module and CLI entry point"
```

---

### Task 2: Конфиг, пути, секреты

**Files:**
- Create: `internal/config/config.go`, `internal/config/paths.go`, `internal/config/secrets.go`
- Test: `internal/config/config_test.go`, `internal/config/secrets_test.go`

**Interfaces:**
- Produces:
  - `type Config struct{ ArchiveDir string; Targets Targets; Media Media; Sync Sync; Backup Backup; Notify Notify }`
  - `type Targets struct{ Folders []string; Include, Exclude []PeerRef }`
  - `type Media struct{ MaxFileSize ByteSize; SkipKinds []string; Workers int }`
  - `type Sync struct{ RunTimeBudget, MaxFloodWait Duration }`
  - `type Backup struct{ Repositories []string; Retention Retention; PruneEvery, CheckEvery Duration; CheckReadSubset string }`, `type Retention struct{ Last, Daily, Weekly, Monthly int }`, `type Notify struct{ SummaryEvery Duration }`
  - `type ByteSize int64`, `type Duration time.Duration` + `func (d Duration) D() time.Duration`, `type PeerRef string` + `Username() (string, bool)`, `ID() (int64, bool)`
  - `var MediaKinds []string`
  - `func Default() Config`, `func Load(path, home string) (Config, error)`
  - `type Paths struct{ Home, Config, Secrets, StateDir string }`, `func ResolvePaths(getenv func(string) string) (Paths, error)`, `func (Paths) Session() string`, `func (Paths) Lock() string`
  - `type Secrets struct{ APIID int; APIHash, BotToken, BotChatID, ResticPassword, RcloneConfigPass string }`, `func LoadSecrets(path string, getenv func(string) string) (Secrets, error)`, `func (Secrets) RequireTelegram() error`

- [ ] **Step 1: Добавить зависимость**

```bash
go get go.yaml.in/yaml/v3
```

- [ ] **Step 2: Написать падающие тесты**

`internal/config/config_test.go`:

```go
package config_test

import (
	"os"
	"path/filepath"
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
		"no targets":   {`archive_dir: /a`, "set at least one of folders or include"},
		"unknown kind": {"targets: {folders: [x]}\nmedia: {skip_kinds: [gif]}", `unknown kind "gif"`},
		"bad workers":  {"targets: {folders: [x]}\nmedia: {workers: 0}", "media.workers must be 1..16"},
		"bad peer":     {"targets: {include: [abc]}", `targets: invalid peer "abc"`},
		"bad size":     {"targets: {folders: [x]}\nmedia: {max_file_size: lots}", `invalid size "LOTS"`},
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
	if p.Session() != "/h/.local/state/tgbup/session" || p.Lock() != "/h/.local/state/tgbup/run.lock" {
		t.Errorf("session/lock = %s %s", p.Session(), p.Lock())
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
```

`internal/config/secrets_test.go`:

```go
package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/joeunderwood/tgbup/internal/config"
)

func TestLoadSecretsPrecedence(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, "secrets.env", "# comment\nTG_API_ID=111\nTG_API_HASH=\"from-file\"\nBOT_TOKEN=file-token\n", 0o600)
	tokenFile := writeFile(t, dir, "token", "file-ref-token\n", 0o600)
	env := map[string]string{"TG_API_HASH": "from-env", "BOT_TOKEN_FILE": tokenFile}

	s, err := config.LoadSecrets(file, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if s.APIID != 111 || s.APIHash != "from-env" || s.BotToken != "file-ref-token" {
		t.Fatalf("secrets = %+v", s)
	}
	if err := s.RequireTelegram(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSecretsRejectsWorldReadableFile(t *testing.T) {
	file := writeFile(t, t.TempDir(), "secrets.env", "TG_API_ID=1\n", 0o644)
	_, err := config.LoadSecrets(file, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadSecretsMissingFileIsFine(t *testing.T) {
	s, err := config.LoadSecrets(filepath.Join(t.TempDir(), "none"), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RequireTelegram(); err == nil {
		t.Fatal("RequireTelegram passed without credentials")
	}
}

func TestLoadSecretsBadAPIID(t *testing.T) {
	file := writeFile(t, t.TempDir(), "secrets.env", "TG_API_ID=abc\n", 0o600)
	if _, err := config.LoadSecrets(file, func(string) string { return "" }); err == nil {
		t.Fatal("non-numeric TG_API_ID accepted")
	}
}
```

- [ ] **Step 3: Убедиться, что тесты падают**

Run: `go test ./internal/config/`
Expected: FAIL — пакет не компилируется (`undefined: config.Load` и т. д.).

- [ ] **Step 4: Реализация**

`internal/config/config.go`:

```go
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	ArchiveDir string  `yaml:"archive_dir"`
	Targets    Targets `yaml:"targets"`
	Media      Media   `yaml:"media"`
	Sync       Sync    `yaml:"sync"`
	Backup     Backup  `yaml:"backup"`
	Notify     Notify  `yaml:"notify"`
}

type Targets struct {
	Folders []string  `yaml:"folders"`
	Include []PeerRef `yaml:"include"`
	Exclude []PeerRef `yaml:"exclude"`
}

type Media struct {
	MaxFileSize ByteSize `yaml:"max_file_size"`
	SkipKinds   []string `yaml:"skip_kinds"`
	Workers     int      `yaml:"workers"`
}

type Sync struct {
	RunTimeBudget Duration `yaml:"run_time_budget"`
	MaxFloodWait  Duration `yaml:"max_flood_wait"`
}

type Backup struct {
	Repositories    []string  `yaml:"repositories"`
	Retention       Retention `yaml:"retention"`
	PruneEvery      Duration  `yaml:"prune_every"`
	CheckEvery      Duration  `yaml:"check_every"`
	CheckReadSubset string    `yaml:"check_read_subset"`
}

type Retention struct {
	Last    int `yaml:"last"`
	Daily   int `yaml:"daily"`
	Weekly  int `yaml:"weekly"`
	Monthly int `yaml:"monthly"`
}

type Notify struct {
	SummaryEvery Duration `yaml:"summary_every"`
}

var MediaKinds = []string{"photo", "video", "video_note", "voice", "audio", "document", "sticker", "animation"}

func Default() Config {
	return Config{
		ArchiveDir: "~/TelegramArchive",
		Media:      Media{Workers: 4},
		Sync:       Sync{RunTimeBudget: Duration(2 * time.Hour), MaxFloodWait: Duration(5 * time.Minute)},
		Backup: Backup{
			Retention:       Retention{Last: 4, Daily: 7, Weekly: 4, Monthly: 12},
			PruneEvery:      Duration(24 * time.Hour),
			CheckEvery:      Duration(30 * 24 * time.Hour),
			CheckReadSubset: "5%",
		},
		Notify: Notify{SummaryEvery: Duration(7 * 24 * time.Hour)},
	}
}

func Load(path, home string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.ArchiveDir = expandHome(cfg.ArchiveDir, home)
	for i, r := range cfg.Backup.Repositories {
		cfg.Backup.Repositories[i] = expandHome(r, home)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

func (c Config) validate() error {
	var errs []error
	if c.ArchiveDir == "" {
		errs = append(errs, errors.New("archive_dir is empty"))
	}
	if len(c.Targets.Folders) == 0 && len(c.Targets.Include) == 0 {
		errs = append(errs, errors.New("targets: set at least one of folders or include"))
	}
	for _, r := range append(slices.Clone(c.Targets.Include), c.Targets.Exclude...) {
		_, isUser := r.Username()
		_, isID := r.ID()
		if !isUser && !isID {
			errs = append(errs, fmt.Errorf("targets: invalid peer %q (want @username or numeric id)", string(r)))
		}
	}
	if c.Media.Workers < 1 || c.Media.Workers > 16 {
		errs = append(errs, fmt.Errorf("media.workers must be 1..16, got %d", c.Media.Workers))
	}
	for _, k := range c.Media.SkipKinds {
		if !slices.Contains(MediaKinds, k) {
			errs = append(errs, fmt.Errorf("media.skip_kinds: unknown kind %q (known: %s)", k, strings.Join(MediaKinds, ", ")))
		}
	}
	return errors.Join(errs...)
}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

type ByteSize int64

func (b *ByteSize) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseByteSize(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*b = ByteSize(v)
	return nil
}

func ParseByteSize(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	units := []struct {
		suffix string
		mult   float64
	}{{"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}}
	for _, u := range units {
		if num, ok := strings.CutSuffix(s, u.suffix); ok {
			n, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
			if err != nil || n < 0 {
				return 0, fmt.Errorf("invalid size %q", s)
			}
			return int64(n * u.mult), nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n, nil
}

type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = Duration(v)
	return nil
}

func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return v, nil
}

type PeerRef string

func (p *PeerRef) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: peer must be @username or numeric id", n.Line)
	}
	*p = PeerRef(n.Value)
	return nil
}

func (p PeerRef) Username() (string, bool) {
	s := string(p)
	if len(s) > 1 && s[0] == '@' {
		return strings.ToLower(s[1:]), true
	}
	return "", false
}

func (p PeerRef) ID() (int64, bool) {
	n, err := strconv.ParseInt(string(p), 10, 64)
	return n, err == nil
}
```

`internal/config/paths.go`:

```go
package config

import (
	"errors"
	"path/filepath"
)

type Paths struct {
	Home     string
	Config   string
	Secrets  string
	StateDir string
}

func ResolvePaths(getenv func(string) string) (Paths, error) {
	home := getenv("HOME")
	if home == "" {
		return Paths{}, errors.New("HOME is not set")
	}
	cfgDir := getenv("XDG_CONFIG_HOME")
	if cfgDir == "" {
		cfgDir = filepath.Join(home, ".config")
	}
	stateDir := getenv("XDG_STATE_HOME")
	if stateDir == "" {
		stateDir = filepath.Join(home, ".local", "state")
	}
	p := Paths{
		Home:     home,
		Config:   filepath.Join(cfgDir, "tgbup", "config.yaml"),
		Secrets:  filepath.Join(cfgDir, "tgbup", "secrets.env"),
		StateDir: filepath.Join(stateDir, "tgbup"),
	}
	if v := getenv("TGBUP_CONFIG"); v != "" {
		p.Config = v
	}
	if v := getenv("TGBUP_SECRETS"); v != "" {
		p.Secrets = v
	}
	if v := getenv("TGBUP_STATE_DIR"); v != "" {
		p.StateDir = v
	}
	return p, nil
}

func (p Paths) Session() string { return filepath.Join(p.StateDir, "session") }
func (p Paths) Lock() string    { return filepath.Join(p.StateDir, "run.lock") }
```

`internal/config/secrets.go`:

```go
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

type Secrets struct {
	APIID            int
	APIHash          string
	BotToken         string
	BotChatID        string
	ResticPassword   string
	RcloneConfigPass string
}

// LoadSecrets resolves each key as: env KEY, then file named by env KEY_FILE, then the secrets file.
func LoadSecrets(path string, getenv func(string) string) (Secrets, error) {
	file, err := readEnvFile(path)
	if err != nil {
		return Secrets{}, err
	}
	var errs []error
	get := func(key string) string {
		if v := getenv(key); v != "" {
			return v
		}
		if f := getenv(key + "_FILE"); f != "" {
			b, err := os.ReadFile(f)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s_FILE: %w", key, err))
				return ""
			}
			return strings.TrimSpace(string(b))
		}
		return file[key]
	}
	s := Secrets{
		APIHash:          get("TG_API_HASH"),
		BotToken:         get("BOT_TOKEN"),
		BotChatID:        get("BOT_CHAT_ID"),
		ResticPassword:   get("RESTIC_PASSWORD"),
		RcloneConfigPass: get("RCLONE_CONFIG_PASS"),
	}
	if v := get("TG_API_ID"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, errors.New("TG_API_ID must be a number"))
		}
		s.APIID = n
	}
	return s, errors.Join(errs...)
}

func (s Secrets) RequireTelegram() error {
	if s.APIID == 0 || s.APIHash == "" {
		return errors.New("TG_API_ID and TG_API_HASH are required (create an app at https://my.telegram.org)")
	}
	return nil
}

func readEnvFile(path string) (map[string]string, error) {
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is readable by group/others: chmod 600 it", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, i+1)
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out, nil
}
```

- [ ] **Step 5: Прогнать тесты**

Run: `go test ./internal/config/ -v`
Expected: PASS всех тестов.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/config
git commit -m "feat: load config, XDG paths and secrets for tgbup"
```

---

### Task 3: Store — открытие БД, миграции, meta

**Files:**
- Create: `internal/store/store.go`, `internal/store/migrations/001_init.sql`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Produces: `type Store struct{ db *sql.DB }`, `func Open(path string) (*Store, error)`, `func (*Store) Close() error`, `func (*Store) Meta(ctx, key string) (string, bool, error)`, `func (*Store) SetMeta(ctx, key, value string) error`. Имя драйвера `sqlite` (modernc).

- [ ] **Step 1: Добавить зависимость**

```bash
go get modernc.org/sqlite
```

- [ ] **Step 2: Написать падающий тест**

`internal/store/store_test.go`:

```go
package store

import (
	"context"
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
	for _, table := range []string{"chats", "topics", "users", "messages", "media", "message_media", "sync_cursors", "meta"} {
		if count(t, s, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table) != 1 {
			t.Errorf("table %s missing", table)
		}
	}
	v, ok, err := s.Meta(context.Background(), "schema_version")
	if err != nil || !ok || v != "1" {
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

func TestMetaMissingKey(t *testing.T) {
	s := openTest(t)
	_, ok, err := s.Meta(context.Background(), "nope")
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}
```

- [ ] **Step 3: Убедиться, что тест падает**

Run: `go test ./internal/store/`
Expected: FAIL — `undefined: Open`.

- [ ] **Step 4: Реализация**

`internal/store/migrations/001_init.sql`:

```sql
CREATE TABLE chats (
  id               INTEGER PRIMARY KEY,
  type             TEXT    NOT NULL,
  title            TEXT    NOT NULL DEFAULT '',
  username         TEXT    NOT NULL DEFAULT '',
  folders_json     TEXT    NOT NULL DEFAULT '[]',
  migrated_from_id INTEGER,
  status           TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inaccessible')),
  first_seen_at    INTEGER NOT NULL,
  last_synced_at   INTEGER
);

CREATE TABLE topics (
  chat_id    INTEGER NOT NULL REFERENCES chats(id),
  topic_id   INTEGER NOT NULL,
  title      TEXT    NOT NULL,
  closed     INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (chat_id, topic_id)
);

CREATE TABLE users (
  id         INTEGER PRIMARY KEY,
  first_name TEXT    NOT NULL DEFAULT '',
  last_name  TEXT    NOT NULL DEFAULT '',
  username   TEXT    NOT NULL DEFAULT '',
  is_bot     INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);

CREATE TABLE messages (
  chat_id             INTEGER NOT NULL REFERENCES chats(id),
  id                  INTEGER NOT NULL,
  topic_id            INTEGER,
  date                INTEGER NOT NULL,
  from_id             INTEGER,
  text                TEXT    NOT NULL DEFAULT '',
  entities_json       TEXT,
  reply_to_id         INTEGER,
  fwd_from_json       TEXT,
  grouped_id          INTEGER,
  reactions_json      TEXT,
  service_action_json TEXT,
  pinned              INTEGER NOT NULL DEFAULT 0,
  edit_date           INTEGER,
  raw                 BLOB    NOT NULL,
  tl_layer            INTEGER NOT NULL,
  PRIMARY KEY (chat_id, id)
) WITHOUT ROWID;

CREATE INDEX messages_by_topic ON messages (chat_id, topic_id, date);
CREATE INDEX messages_by_date  ON messages (chat_id, date);

CREATE TABLE media (
  key        TEXT    PRIMARY KEY,
  kind       TEXT    NOT NULL,
  file_name  TEXT    NOT NULL DEFAULT '',
  mime       TEXT    NOT NULL DEFAULT '',
  size       INTEGER NOT NULL DEFAULT 0,
  local_path TEXT,
  status     TEXT    NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'done', 'failed', 'skipped')),
  error      TEXT,
  attempts   INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX media_by_status ON media (status);

CREATE TABLE message_media (
  chat_id    INTEGER NOT NULL,
  message_id INTEGER NOT NULL,
  media_key  TEXT    NOT NULL REFERENCES media(key),
  PRIMARY KEY (chat_id, message_id, media_key),
  FOREIGN KEY (chat_id, message_id) REFERENCES messages (chat_id, id)
);

CREATE INDEX message_media_by_key ON message_media (media_key);

CREATE TABLE sync_cursors (
  chat_id       INTEGER PRIMARY KEY REFERENCES chats(id),
  newest_id     INTEGER NOT NULL DEFAULT 0,
  oldest_id     INTEGER NOT NULL DEFAULT 0,
  backfill_done INTEGER NOT NULL DEFAULT 0,
  updated_at    INTEGER NOT NULL
);
```

`internal/store/store.go`:

```go
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: SQLite has a single writer anyway, and media workers must not hit SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Meta(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return err
	}
	current := 0
	if v, ok, err := s.Meta(ctx, "schema_version"); err != nil {
		return err
	} else if ok {
		current, _ = strconv.Atoi(v)
	}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		version, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			return fmt.Errorf("bad migration file name %s", e.Name())
		}
		if version <= current {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", e.Name(), err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO meta (key, value) VALUES ('schema_version', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			strconv.Itoa(version)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 5: Прогнать тесты**

Run: `go test ./internal/store/ -v`
Expected: PASS. Если `journal_mode` не `wal` — проверить, что DSN начинается с `file:` и драйвер `modernc.org/sqlite`.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/store
git commit -m "feat: add SQLite store with embedded migrations"
```

---

### Task 4: Store — чаты, топики, пачки, курсоры, закрепы

**Files:**
- Create: `internal/store/types.go`, `internal/store/chats.go`, `internal/store/batch.go`
- Test: `internal/store/batch_test.go`

**Interfaces:**
- Consumes: `Store` из Task 3.
- Produces:
  - константы `ChatPrivate="private"`, `ChatGroup="group"`, `ChatSupergroup="supergroup"`, `ChatForum="forum"`, `ChatChannel="channel"`; `ChatActive="active"`, `ChatInaccessible="inaccessible"`; `MediaPending/MediaDone/MediaFailed/MediaSkipped = "pending"/"done"/"failed"/"skipped"`
  - `type Chat struct{ ID int64; Type, Title, Username string; Folders []string; MigratedFromID int64; Status string }`
  - `type Topic struct{ ChatID int64; TopicID int; Title string; Closed bool; CreatedAt int64 }`
  - `type User struct{ ID int64; FirstName, LastName, Username string; IsBot, Partial bool }` (`Partial` — «min»-пользователь, не перезаписывает существующую запись)
  - `type Message struct{ ChatID int64; ID, TopicID int; Date, FromID int64; Text, EntitiesJSON, FwdFromJSON, ReactionsJSON, ServiceActionJSON string; ReplyToID int; GroupedID int64; Pinned bool; EditDate int64; Raw []byte; TLLayer int; MediaKeys []string }`
  - `type Media struct{ Key, Kind, FileName, Mime string; Size int64 }`
  - `type Cursor struct{ NewestID, OldestID int; BackfillDone bool }`
  - `type Batch struct{ ChatID int64; Users []User; Messages []Message; Media []Media; Cursor Cursor }`
  - `func (*Store) UpsertChat(ctx, Chat) error`, `Chat(ctx, id int64) (Chat, bool, error)`, `ActiveChatIDs(ctx) ([]int64, error)`, `MarkInaccessible(ctx, id int64) (changed bool, err error)`, `UpsertTopics(ctx, []Topic) error`, `Topics(ctx, chatID int64) ([]Topic, error)`
  - `func (*Store) SaveBatch(ctx, Batch) error`, `GetCursor(ctx, chatID int64) (Cursor, bool, error)`, `SetPinned(ctx, chatID int64, ids []int) error`, `PinnedMessageIDs(ctx, chatID int64) ([]int, error)`, `CountMessages(ctx, chatID int64) (int, error)`

- [ ] **Step 1: Написать падающие тесты**

`internal/store/batch_test.go`:

```go
package store

import (
	"context"
	"slices"
	"testing"
)

const chatA = int64(-1000000000001)

func seedChat(t *testing.T, s *Store, id int64) {
	t.Helper()
	if err := s.UpsertChat(context.Background(), Chat{ID: id, Type: ChatSupergroup, Title: "A"}); err != nil {
		t.Fatal(err)
	}
}

func msg(id int, keys ...string) Message {
	return Message{ChatID: chatA, ID: id, Date: 1700000000 + int64(id), Text: "t", Raw: []byte{1}, TLLayer: 1, MediaKeys: keys}
}

func TestSaveBatchWritesEverything(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)

	err := s.SaveBatch(ctx, Batch{
		ChatID:   chatA,
		Users:    []User{{ID: 5, FirstName: "Ann", Username: "ann"}},
		Media:    []Media{{Key: "document:9", Kind: "document", FileName: "a.pdf", Mime: "application/pdf", Size: 10}},
		Messages: []Message{msg(1), msg(2, "document:9")},
		Cursor:   Cursor{NewestID: 2, OldestID: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountMessages(ctx, chatA); n != 2 {
		t.Errorf("messages = %d", n)
	}
	if count(t, s, `SELECT count(*) FROM message_media WHERE media_key='document:9'`) != 1 {
		t.Error("message_media not written")
	}
	if count(t, s, `SELECT count(*) FROM media WHERE key='document:9' AND status='pending'`) != 1 {
		t.Error("media not pending")
	}
	cur, ok, err := s.GetCursor(ctx, chatA)
	if err != nil || !ok || cur != (Cursor{NewestID: 2, OldestID: 1}) {
		t.Fatalf("cursor = %+v %v %v", cur, ok, err)
	}
	if count(t, s, `SELECT count(*) FROM chats WHERE id=? AND last_synced_at IS NOT NULL`, chatA) != 1 {
		t.Error("last_synced_at not set")
	}
}

func TestSaveBatchKeepsFirstVersionOfMessage(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)
	first := msg(1)
	first.Text = "original"
	edited := msg(1)
	edited.Text = "edited"

	for _, m := range []Message{first, edited} {
		if err := s.SaveBatch(ctx, Batch{ChatID: chatA, Messages: []Message{m}, Cursor: Cursor{NewestID: 1, OldestID: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	var text string
	s.db.QueryRow(`SELECT text FROM messages WHERE chat_id=? AND id=1`, chatA).Scan(&text)
	if text != "original" {
		t.Fatalf("text = %q", text)
	}
}

func TestSaveBatchIsAtomic(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)

	// media key is not in Batch.Media → FK violation on message_media → whole batch must roll back.
	err := s.SaveBatch(ctx, Batch{
		ChatID:   chatA,
		Users:    []User{{ID: 5}},
		Messages: []Message{msg(1), msg(2, "document:404")},
		Cursor:   Cursor{NewestID: 2, OldestID: 1},
	})
	if err == nil {
		t.Fatal("expected FK error")
	}
	if n, _ := s.CountMessages(ctx, chatA); n != 0 {
		t.Errorf("messages = %d after failed batch", n)
	}
	if count(t, s, `SELECT count(*) FROM users`) != 0 {
		t.Error("users written by failed batch")
	}
	if _, ok, _ := s.GetCursor(ctx, chatA); ok {
		t.Error("cursor moved by failed batch")
	}
}

func TestPartialUserDoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)
	full := Batch{ChatID: chatA, Users: []User{{ID: 5, FirstName: "Ann", Username: "ann"}}}
	partial := Batch{ChatID: chatA, Users: []User{{ID: 5, Partial: true}, {ID: 6, FirstName: "New", Partial: true}}}
	for _, b := range []Batch{full, partial} {
		if err := s.SaveBatch(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	if count(t, s, `SELECT count(*) FROM users WHERE id=5 AND username='ann'`) != 1 {
		t.Error("partial user overwrote full one")
	}
	if count(t, s, `SELECT count(*) FROM users WHERE id=6`) != 1 {
		t.Error("partial user not inserted when absent")
	}
}

func TestSetPinnedReplacesPreviousSet(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)
	if err := s.SaveBatch(ctx, Batch{ChatID: chatA, Messages: []Message{msg(1), msg(2), msg(3)}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(ctx, chatA, []int{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(ctx, chatA, []int{3, 99}); err != nil {
		t.Fatal(err)
	}
	got, err := s.PinnedMessageIDs(ctx, chatA)
	if err != nil || !slices.Equal(got, []int{3}) {
		t.Fatalf("pinned = %v %v", got, err)
	}
}

func TestChatStatusLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)

	changed, err := s.MarkInaccessible(ctx, chatA)
	if err != nil || !changed {
		t.Fatalf("first mark: %v %v", changed, err)
	}
	changed, _ = s.MarkInaccessible(ctx, chatA)
	if changed {
		t.Fatal("second mark reported change")
	}
	ids, _ := s.ActiveChatIDs(ctx)
	if len(ids) != 0 {
		t.Fatalf("active = %v", ids)
	}

	if err := s.UpsertChat(ctx, Chat{ID: chatA, Type: ChatForum, Title: "B", Folders: []string{"Семья"}, MigratedFromID: -42}); err != nil {
		t.Fatal(err)
	}
	c, ok, err := s.Chat(ctx, chatA)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if c.Status != ChatActive || c.Type != ChatForum || c.Title != "B" || c.MigratedFromID != -42 || !slices.Equal(c.Folders, []string{"Семья"}) {
		t.Fatalf("chat = %+v", c)
	}
}

func TestUpsertTopics(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)
	if err := s.UpsertTopics(ctx, []Topic{{ChatID: chatA, TopicID: 1, Title: "General"}, {ChatID: chatA, TopicID: 7, Title: "Old"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTopics(ctx, []Topic{{ChatID: chatA, TopicID: 7, Title: "Renamed", Closed: true}}); err != nil {
		t.Fatal(err)
	}
	ts, err := s.Topics(ctx, chatA)
	if err != nil || len(ts) != 2 || ts[1].Title != "Renamed" || !ts[1].Closed {
		t.Fatalf("topics = %+v %v", ts, err)
	}
}
```

- [ ] **Step 2: Убедиться, что тесты падают**

Run: `go test ./internal/store/`
Expected: FAIL — `undefined: Chat`, `undefined: SaveBatch` и т. д.

- [ ] **Step 3: Реализация**

`internal/store/types.go`:

```go
package store

const (
	ChatPrivate    = "private"
	ChatGroup      = "group"
	ChatSupergroup = "supergroup"
	ChatForum      = "forum"
	ChatChannel    = "channel"

	ChatActive       = "active"
	ChatInaccessible = "inaccessible"

	MediaPending = "pending"
	MediaDone    = "done"
	MediaFailed  = "failed"
	MediaSkipped = "skipped"
)

type Chat struct {
	ID             int64
	Type           string
	Title          string
	Username       string
	Folders        []string
	MigratedFromID int64
	Status         string
}

type Topic struct {
	ChatID    int64
	TopicID   int
	Title     string
	Closed    bool
	CreatedAt int64
}

type User struct {
	ID        int64
	FirstName string
	LastName  string
	Username  string
	IsBot     bool
	Partial   bool
}

type Message struct {
	ChatID            int64
	ID                int
	TopicID           int
	Date              int64
	FromID            int64
	Text              string
	EntitiesJSON      string
	FwdFromJSON       string
	ReactionsJSON     string
	ServiceActionJSON string
	ReplyToID         int
	GroupedID         int64
	Pinned            bool
	EditDate          int64
	Raw               []byte
	TLLayer           int
	MediaKeys         []string
}

type Media struct {
	Key      string
	Kind     string
	FileName string
	Mime     string
	Size     int64
}

type Cursor struct {
	NewestID     int
	OldestID     int
	BackfillDone bool
}

type Batch struct {
	ChatID   int64
	Users    []User
	Messages []Message
	Media    []Media
	Cursor   Cursor
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
```

`internal/store/chats.go`:

```go
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func (s *Store) UpsertChat(ctx context.Context, c Chat) error {
	if c.Folders == nil {
		c.Folders = []string{}
	}
	folders, err := json.Marshal(c.Folders)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO chats (id, type, title, username, folders_json, migrated_from_id, status, first_seen_at)
VALUES (?, ?, ?, ?, ?, ?, 'active', ?)
ON CONFLICT(id) DO UPDATE SET
  type = excluded.type,
  title = excluded.title,
  username = excluded.username,
  folders_json = excluded.folders_json,
  migrated_from_id = COALESCE(excluded.migrated_from_id, chats.migrated_from_id),
  status = 'active'`,
		c.ID, c.Type, c.Title, c.Username, string(folders), nullInt(c.MigratedFromID), time.Now().Unix())
	return err
}

func (s *Store) Chat(ctx context.Context, id int64) (Chat, bool, error) {
	var (
		c       Chat
		folders string
		migr    sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, type, title, username, folders_json, migrated_from_id, status FROM chats WHERE id = ?`, id).
		Scan(&c.ID, &c.Type, &c.Title, &c.Username, &folders, &migr, &c.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, false, nil
	}
	if err != nil {
		return Chat{}, false, err
	}
	c.MigratedFromID = migr.Int64
	if err := json.Unmarshal([]byte(folders), &c.Folders); err != nil {
		return Chat{}, false, err
	}
	return c, true, nil
}

func (s *Store) ActiveChatIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM chats WHERE status = 'active' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) MarkInaccessible(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE chats SET status = 'inaccessible' WHERE id = ? AND status = 'active'`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) UpsertTopics(ctx context.Context, topics []Topic) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range topics {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO topics (chat_id, topic_id, title, closed, created_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(chat_id, topic_id) DO UPDATE SET title = excluded.title, closed = excluded.closed`,
			t.ChatID, t.TopicID, t.Title, t.Closed, t.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Topics(ctx context.Context, chatID int64) ([]Topic, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT chat_id, topic_id, title, closed, created_at FROM topics WHERE chat_id = ? ORDER BY topic_id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Topic
	for rows.Next() {
		var t Topic
		if err := rows.Scan(&t.ChatID, &t.TopicID, &t.Title, &t.Closed, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
```

`internal/store/batch.go`:

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (s *Store) SaveBatch(ctx context.Context, b Batch) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()

	for _, u := range b.Users {
		q := `INSERT INTO users (id, first_name, last_name, username, is_bot, updated_at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET first_name = excluded.first_name, last_name = excluded.last_name,
  username = excluded.username, is_bot = excluded.is_bot, updated_at = excluded.updated_at`
		if u.Partial {
			q = `INSERT INTO users (id, first_name, last_name, username, is_bot, updated_at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING`
		}
		if _, err := tx.ExecContext(ctx, q, u.ID, u.FirstName, u.LastName, u.Username, u.IsBot, now); err != nil {
			return fmt.Errorf("save user %d: %w", u.ID, err)
		}
	}
	for _, m := range b.Media {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO media (key, kind, file_name, mime, size) VALUES (?, ?, ?, ?, ?) ON CONFLICT(key) DO NOTHING`,
			m.Key, m.Kind, m.FileName, m.Mime, m.Size); err != nil {
			return fmt.Errorf("save media %s: %w", m.Key, err)
		}
	}
	for _, m := range b.Messages {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO messages (chat_id, id, topic_id, date, from_id, text, entities_json, reply_to_id, fwd_from_json,
  grouped_id, reactions_json, service_action_json, pinned, edit_date, raw, tl_layer)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(chat_id, id) DO NOTHING`,
			m.ChatID, m.ID, nullInt(int64(m.TopicID)), m.Date, nullInt(m.FromID), m.Text, nullStr(m.EntitiesJSON),
			nullInt(int64(m.ReplyToID)), nullStr(m.FwdFromJSON), nullInt(m.GroupedID), nullStr(m.ReactionsJSON),
			nullStr(m.ServiceActionJSON), m.Pinned, nullInt(m.EditDate), m.Raw, m.TLLayer); err != nil {
			return fmt.Errorf("save message %d/%d: %w", m.ChatID, m.ID, err)
		}
		for _, k := range m.MediaKeys {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO message_media (chat_id, message_id, media_key) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
				m.ChatID, m.ID, k); err != nil {
				return fmt.Errorf("link media %s to %d/%d: %w", k, m.ChatID, m.ID, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO sync_cursors (chat_id, newest_id, oldest_id, backfill_done, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(chat_id) DO UPDATE SET newest_id = excluded.newest_id, oldest_id = excluded.oldest_id,
  backfill_done = excluded.backfill_done, updated_at = excluded.updated_at`,
		b.ChatID, b.Cursor.NewestID, b.Cursor.OldestID, b.Cursor.BackfillDone, now); err != nil {
		return fmt.Errorf("save cursor: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE chats SET last_synced_at = ? WHERE id = ?`, now, b.ChatID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetCursor(ctx context.Context, chatID int64) (Cursor, bool, error) {
	var c Cursor
	err := s.db.QueryRowContext(ctx,
		`SELECT newest_id, oldest_id, backfill_done FROM sync_cursors WHERE chat_id = ?`, chatID).
		Scan(&c.NewestID, &c.OldestID, &c.BackfillDone)
	if errors.Is(err, sql.ErrNoRows) {
		return Cursor{}, false, nil
	}
	if err != nil {
		return Cursor{}, false, err
	}
	return c, true, nil
}

func (s *Store) SetPinned(ctx context.Context, chatID int64, ids []int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET pinned = 0 WHERE chat_id = ? AND pinned = 1`, chatID); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET pinned = 1 WHERE chat_id = ? AND id = ?`, chatID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) PinnedMessageIDs(ctx context.Context, chatID int64) ([]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM messages WHERE chat_id = ? AND pinned = 1 ORDER BY id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) CountMessages(ctx context.Context, chatID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE chat_id = ?`, chatID).Scan(&n)
	return n, err
}
```

- [ ] **Step 4: Прогнать тесты**

Run: `go test ./internal/store/ -v -race`
Expected: PASS всех тестов (включая Task 3).

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat: store chats, topics and message batches atomically with sync cursors"
```

---

### Task 5: Convert — сообщения

**Files:**
- Create: `internal/convert/ids.go`, `internal/convert/message.go`
- Test: `internal/convert/message_test.go`

**Interfaces:**
- Consumes: `store.Message`, `store.Media`, `store.User`.
- Produces:
  - `func PeerID(tg.PeerClass) int64`, `func InputPeerID(tg.InputPeerClass) (int64, bool)`, `func ChatID(id int64) int64`, `func ChannelID(id int64) int64`
  - `type Options struct{ IsForum bool; SelfID int64 }`
  - `type Result struct{ Message store.Message; Media []store.Media }`
  - `func Message(mc tg.MessageClass, opts Options) (Result, bool, error)` — `false` для `MessageEmpty`
  - `func Encode(mc tg.MessageClass) ([]byte, error)`
  - `func Users(us []tg.UserClass) []store.User`

- [ ] **Step 1: Добавить зависимость**

```bash
go get github.com/gotd/td
```

- [ ] **Step 2: Написать падающие тесты**

`internal/convert/message_test.go`:

```go
package convert_test

import (
	"strings"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	"gitlab.com/joeunderwood/tgbup/internal/convert"
)

func TestPeerIDs(t *testing.T) {
	cases := []struct {
		peer tg.PeerClass
		want int64
	}{
		{&tg.PeerUser{UserID: 5}, 5},
		{&tg.PeerChat{ChatID: 7}, -7},
		{&tg.PeerChannel{ChannelID: 777}, -1000000000777},
	}
	for _, c := range cases {
		if got := convert.PeerID(c.peer); got != c.want {
			t.Errorf("PeerID(%T) = %d, want %d", c.peer, got, c.want)
		}
	}
	if id, ok := convert.InputPeerID(&tg.InputPeerChannel{ChannelID: 777}); !ok || id != -1000000000777 {
		t.Errorf("InputPeerID = %d %v", id, ok)
	}
	if _, ok := convert.InputPeerID(&tg.InputPeerSelf{}); ok {
		t.Error("InputPeerSelf must not resolve")
	}
}

func TestRegularMessage(t *testing.T) {
	m := &tg.Message{ID: 42, PeerID: &tg.PeerChannel{ChannelID: 777}, Date: 1700000000, Message: "см. доку", Pinned: true}
	m.SetFromID(&tg.PeerUser{UserID: 5})
	m.SetEntities([]tg.MessageEntityClass{
		&tg.MessageEntityTextURL{Offset: 3, Length: 4, URL: "https://example.com/doc"},
		&tg.MessageEntityMentionName{Offset: 0, Length: 2, UserID: 9},
	})
	m.SetGroupedID(99)
	m.SetEditDate(1700000100)

	res, ok, err := convert.Message(m, convert.Options{SelfID: 1})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	got := res.Message
	if got.ChatID != -1000000000777 || got.ID != 42 || got.Date != 1700000000 || got.FromID != 5 {
		t.Errorf("ids = %+v", got)
	}
	if !got.Pinned || got.GroupedID != 99 || got.EditDate != 1700000100 || got.TopicID != 0 {
		t.Errorf("flags = %+v", got)
	}
	for _, want := range []string{`"url":"https://example.com/doc"`, `"type":"messageEntityTextUrl"`, `"user_id":9`} {
		if !strings.Contains(got.EntitiesJSON, want) {
			t.Errorf("entities %s missing %s", got.EntitiesJSON, want)
		}
	}
	if got.TLLayer != tg.Layer {
		t.Errorf("layer = %d", got.TLLayer)
	}
	decoded, err := tg.DecodeMessage(&bin.Buffer{Buf: got.Raw})
	if err != nil {
		t.Fatal(err)
	}
	if d := decoded.(*tg.Message); d.Message != "см. доку" || d.ID != 42 {
		t.Errorf("raw round-trip = %+v", d)
	}
}

func TestSenderResolution(t *testing.T) {
	in := &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 50}}
	res, _, _ := convert.Message(in, convert.Options{SelfID: 1})
	if res.Message.FromID != 50 {
		t.Errorf("incoming private from = %d, want peer 50", res.Message.FromID)
	}
	out := &tg.Message{ID: 2, PeerID: &tg.PeerUser{UserID: 50}, Out: true}
	res, _, _ = convert.Message(out, convert.Options{SelfID: 1})
	if res.Message.FromID != 1 {
		t.Errorf("outgoing from = %d, want self 1", res.Message.FromID)
	}
}

func forumReply(msgID, topID int, forum bool) *tg.MessageReplyHeader {
	h := &tg.MessageReplyHeader{ForumTopic: forum}
	if msgID != 0 {
		h.SetReplyToMsgID(msgID)
	}
	if topID != 0 {
		h.SetReplyToTopID(topID)
	}
	return h
}

func TestThreading(t *testing.T) {
	cases := []struct {
		name          string
		forum         bool
		reply         tg.MessageReplyHeaderClass
		topic, replyTo int
	}{
		{"plain chat reply", false, forumReply(10, 0, false), 0, 10},
		{"forum, no reply header → General", true, nil, 1, 0},
		{"forum, post in topic root", true, forumReply(100, 0, true), 100, 0},
		{"forum, reply inside topic", true, forumReply(150, 100, true), 100, 150},
		{"forum, reply without forum flag → General", true, forumReply(20, 0, false), 1, 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &tg.Message{ID: 200, PeerID: &tg.PeerChannel{ChannelID: 1}}
			if c.reply != nil {
				m.SetReplyTo(c.reply)
			}
			res, _, err := convert.Message(m, convert.Options{IsForum: c.forum})
			if err != nil {
				t.Fatal(err)
			}
			if res.Message.TopicID != c.topic || res.Message.ReplyToID != c.replyTo {
				t.Errorf("topic=%d reply=%d, want %d %d", res.Message.TopicID, res.Message.ReplyToID, c.topic, c.replyTo)
			}
		})
	}
}

func TestServiceMessages(t *testing.T) {
	pin := &tg.MessageService{ID: 300, PeerID: &tg.PeerChannel{ChannelID: 1}, Date: 5, Action: &tg.MessageActionPinMessage{}}
	pin.SetFromID(&tg.PeerUser{UserID: 5})
	pin.SetReplyTo(forumReply(42, 0, false))
	res, ok, err := convert.Message(pin, convert.Options{})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if !strings.Contains(res.Message.ServiceActionJSON, `"type":"messageActionPinMessage"`) {
		t.Errorf("action = %s", res.Message.ServiceActionJSON)
	}
	if res.Message.ReplyToID != 42 || res.Message.FromID != 5 {
		t.Errorf("pin target/from = %d/%d", res.Message.ReplyToID, res.Message.FromID)
	}

	create := &tg.MessageService{ID: 500, PeerID: &tg.PeerChannel{ChannelID: 1}, Action: &tg.MessageActionTopicCreate{Title: "Рецепты"}}
	res, _, _ = convert.Message(create, convert.Options{IsForum: true})
	if res.Message.TopicID != 500 {
		t.Errorf("topic create topic = %d, want own id", res.Message.TopicID)
	}
	if !strings.Contains(res.Message.ServiceActionJSON, "Рецепты") {
		t.Errorf("action data lost: %s", res.Message.ServiceActionJSON)
	}
}

func TestForwardAndReactions(t *testing.T) {
	m := &tg.Message{ID: 1, PeerID: &tg.PeerChat{ChatID: 3}}
	fwd := tg.MessageFwdHeader{Date: 1600000000}
	fwd.SetFromID(&tg.PeerChannel{ChannelID: 8})
	fwd.SetChannelPost(77)
	m.SetFwdFrom(fwd)
	m.SetReactions(tg.MessageReactions{Results: []tg.ReactionCount{
		{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 3},
		{Reaction: &tg.ReactionCustomEmoji{DocumentID: 123}, Count: 1},
	}})
	res, _, err := convert.Message(m, convert.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Message.FwdFromJSON, `"from_id":-1000000000008`) || !strings.Contains(res.Message.FwdFromJSON, `"channel_post":77`) {
		t.Errorf("fwd = %s", res.Message.FwdFromJSON)
	}
	if !strings.Contains(res.Message.ReactionsJSON, `"emoji":"👍","count":3`) || !strings.Contains(res.Message.ReactionsJSON, `"custom_emoji_id":123`) {
		t.Errorf("reactions = %s", res.Message.ReactionsJSON)
	}
}

func TestEmptyMessageSkipped(t *testing.T) {
	_, ok, err := convert.Message(&tg.MessageEmpty{ID: 5}, convert.Options{})
	if ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestUsers(t *testing.T) {
	us := convert.Users([]tg.UserClass{
		&tg.User{ID: 1, FirstName: "A", Username: "a", Bot: true},
		&tg.User{ID: 2, Min: true},
		&tg.UserEmpty{ID: 3},
	})
	if len(us) != 2 || !us[0].IsBot || us[0].Username != "a" || !us[1].Partial {
		t.Fatalf("users = %+v", us)
	}
}
```

- [ ] **Step 3: Убедиться, что тесты падают**

Run: `go test ./internal/convert/`
Expected: FAIL — пакет не компилируется.

- [ ] **Step 4: Реализация**

`internal/convert/ids.go`:

```go
package convert

import "github.com/gotd/td/tg"

const channelIDOffset = 1000000000000

func ChatID(id int64) int64    { return -id }
func ChannelID(id int64) int64 { return -channelIDOffset - id }

func PeerID(p tg.PeerClass) int64 {
	switch v := p.(type) {
	case *tg.PeerUser:
		return v.UserID
	case *tg.PeerChat:
		return ChatID(v.ChatID)
	case *tg.PeerChannel:
		return ChannelID(v.ChannelID)
	}
	return 0
}

func InputPeerID(p tg.InputPeerClass) (int64, bool) {
	switch v := p.(type) {
	case *tg.InputPeerUser:
		return v.UserID, true
	case *tg.InputPeerChat:
		return ChatID(v.ChatID), true
	case *tg.InputPeerChannel:
		return ChannelID(v.ChannelID), true
	}
	return 0, false
}
```

`internal/convert/message.go`:

```go
package convert

import (
	"encoding/json"
	"fmt"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	"gitlab.com/joeunderwood/tgbup/internal/store"
)

type Options struct {
	IsForum bool
	SelfID  int64
}

type Result struct {
	Message store.Message
	Media   []store.Media
}

func Message(mc tg.MessageClass, opts Options) (Result, bool, error) {
	switch m := mc.(type) {
	case *tg.Message:
		return regular(m, opts)
	case *tg.MessageService:
		return service(m, opts)
	default:
		return Result{}, false, nil
	}
}

func Encode(mc tg.MessageClass) ([]byte, error) {
	var b bin.Buffer
	if err := mc.Encode(&b); err != nil {
		return nil, err
	}
	return b.Copy(), nil
}

func regular(m *tg.Message, opts Options) (Result, bool, error) {
	raw, err := Encode(m)
	if err != nil {
		return Result{}, false, fmt.Errorf("encode message %d: %w", m.ID, err)
	}
	from, hasFrom := m.GetFromID()
	out := store.Message{
		ChatID:    PeerID(m.PeerID),
		ID:        m.ID,
		Date:      int64(m.Date),
		FromID:    senderID(from, hasFrom, m.Out, m.PeerID, opts.SelfID),
		Text:      m.Message,
		GroupedID: m.GroupedID,
		Pinned:    m.Pinned,
		EditDate:  int64(m.EditDate),
		Raw:       raw,
		TLLayer:   tg.Layer,
	}
	reply, _ := m.GetReplyTo()
	out.TopicID, out.ReplyToID = threading(opts.IsForum, reply, m.ID, false)

	if len(m.Entities) > 0 {
		if out.EntitiesJSON, err = entitiesJSON(m.Entities); err != nil {
			return Result{}, false, err
		}
	}
	if fwd, ok := m.GetFwdFrom(); ok {
		if out.FwdFromJSON, err = forwardJSON(fwd); err != nil {
			return Result{}, false, err
		}
	}
	if r, ok := m.GetReactions(); ok && len(r.Results) > 0 {
		if out.ReactionsJSON, err = reactionsJSON(r); err != nil {
			return Result{}, false, err
		}
	}
	return Result{Message: out}, true, nil
}

func service(m *tg.MessageService, opts Options) (Result, bool, error) {
	raw, err := Encode(m)
	if err != nil {
		return Result{}, false, fmt.Errorf("encode service message %d: %w", m.ID, err)
	}
	action, err := json.Marshal(struct {
		Type string                `json:"type"`
		Data tg.MessageActionClass `json:"data"`
	}{m.Action.TypeName(), m.Action})
	if err != nil {
		action, _ = json.Marshal(map[string]string{"type": m.Action.TypeName()})
	}
	_, createsTopic := m.Action.(*tg.MessageActionTopicCreate)
	from, hasFrom := m.GetFromID()
	out := store.Message{
		ChatID:            PeerID(m.PeerID),
		ID:                m.ID,
		Date:              int64(m.Date),
		FromID:            senderID(from, hasFrom, m.Out, m.PeerID, opts.SelfID),
		ServiceActionJSON: string(action),
		Raw:               raw,
		TLLayer:           tg.Layer,
	}
	reply, _ := m.GetReplyTo()
	out.TopicID, out.ReplyToID = threading(opts.IsForum, reply, m.ID, createsTopic)
	return Result{Message: out}, true, nil
}

func senderID(from tg.PeerClass, hasFrom, out bool, peer tg.PeerClass, selfID int64) int64 {
	if hasFrom {
		return PeerID(from)
	}
	if out {
		return selfID
	}
	return PeerID(peer)
}

// In forums reply_to_top_id is set only when replying to a non-root message;
// otherwise reply_to_msg_id is the topic root itself, not a real reply.
func threading(isForum bool, rc tg.MessageReplyHeaderClass, msgID int, createsTopic bool) (topicID, replyToID int) {
	h, _ := rc.(*tg.MessageReplyHeader)
	if h != nil {
		replyToID, _ = h.GetReplyToMsgID()
	}
	if !isForum {
		return 0, replyToID
	}
	if createsTopic {
		return msgID, 0
	}
	if h == nil || !h.ForumTopic {
		return 1, replyToID
	}
	if top, ok := h.GetReplyToTopID(); ok {
		return top, replyToID
	}
	return replyToID, 0
}

// Offsets and lengths are in UTF-16 code units, as in the Telegram API.
type entityJSON struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	URL    string `json:"url,omitempty"`
	UserID int64  `json:"user_id,omitempty"`
}

func entitiesJSON(es []tg.MessageEntityClass) (string, error) {
	out := make([]entityJSON, 0, len(es))
	for _, e := range es {
		j := entityJSON{Type: e.TypeName(), Offset: e.GetOffset(), Length: e.GetLength()}
		switch v := e.(type) {
		case *tg.MessageEntityTextURL:
			j.URL = v.URL
		case *tg.MessageEntityMentionName:
			j.UserID = v.UserID
		}
		out = append(out, j)
	}
	b, err := json.Marshal(out)
	return string(b), err
}

type fwdJSON struct {
	FromID         int64  `json:"from_id,omitempty"`
	FromName       string `json:"from_name,omitempty"`
	Date           int    `json:"date"`
	ChannelPost    int    `json:"channel_post,omitempty"`
	PostAuthor     string `json:"post_author,omitempty"`
	SavedFromPeer  int64  `json:"saved_from_peer,omitempty"`
	SavedFromMsgID int    `json:"saved_from_msg_id,omitempty"`
}

func forwardJSON(h tg.MessageFwdHeader) (string, error) {
	j := fwdJSON{FromName: h.FromName, Date: h.Date, ChannelPost: h.ChannelPost, PostAuthor: h.PostAuthor}
	if p, ok := h.GetFromID(); ok {
		j.FromID = PeerID(p)
	}
	if p, ok := h.GetSavedFromPeer(); ok {
		j.SavedFromPeer = PeerID(p)
	}
	j.SavedFromMsgID, _ = h.GetSavedFromMsgID()
	b, err := json.Marshal(j)
	return string(b), err
}

type reactionJSON struct {
	Emoji         string `json:"emoji,omitempty"`
	CustomEmojiID int64  `json:"custom_emoji_id,omitempty"`
	Paid          bool   `json:"paid,omitempty"`
	Count         int    `json:"count"`
}

func reactionsJSON(r tg.MessageReactions) (string, error) {
	out := make([]reactionJSON, 0, len(r.Results))
	for _, rc := range r.Results {
		j := reactionJSON{Count: rc.Count}
		switch v := rc.Reaction.(type) {
		case *tg.ReactionEmoji:
			j.Emoji = v.Emoticon
		case *tg.ReactionCustomEmoji:
			j.CustomEmojiID = v.DocumentID
		case *tg.ReactionPaid:
			j.Paid = true
		}
		out = append(out, j)
	}
	b, err := json.Marshal(out)
	return string(b), err
}

func Users(us []tg.UserClass) []store.User {
	out := make([]store.User, 0, len(us))
	for _, uc := range us {
		u, ok := uc.(*tg.User)
		if !ok {
			continue
		}
		out = append(out, store.User{
			ID: u.ID, FirstName: u.FirstName, LastName: u.LastName, Username: u.Username, IsBot: u.Bot, Partial: u.Min,
		})
	}
	return out
}
```

- [ ] **Step 5: Прогнать тесты**

Run: `go test ./internal/convert/ -v`
Expected: PASS. Если `reactions` не совпадают из-за порядка полей JSON — порядок задаётся порядком полей структуры `reactionJSON`, не менять его.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/convert
git commit -m "feat: convert Telegram messages, service events and users to store rows"
```

---

### Task 6: Convert — медиа и место файла

**Files:**
- Create: `internal/convert/media.go`
- Modify: `internal/convert/message.go` (в `regular` заполнить `Result.Media` и `MediaKeys`)
- Test: `internal/convert/media_test.go`

**Interfaces:**
- Consumes: `Result`, `regular` из Task 5.
- Produces: `func ExtractMedia(m *tg.Message) (store.Media, bool)`, `func FileLocation(mc tg.MessageClass) (tg.InputFileLocationClass, error)`. Ключ медиа: `photo:<id>` или `document:<id>`. Виды: `photo`, `video`, `video_note`, `voice`, `audio`, `document`, `sticker`, `animation`.

- [ ] **Step 1: Написать падающие тесты**

`internal/convert/media_test.go`:

```go
package convert_test

import (
	"testing"

	"github.com/gotd/td/tg"

	"gitlab.com/joeunderwood/tgbup/internal/convert"
)

func photoMessage() *tg.Message {
	p := &tg.Photo{ID: 777, AccessHash: 2, FileReference: []byte("r"), Sizes: []tg.PhotoSizeClass{
		&tg.PhotoStrippedSize{Type: "i", Bytes: []byte{1}},
		&tg.PhotoSize{Type: "m", W: 320, H: 240, Size: 1000},
		&tg.PhotoSizeProgressive{Type: "y", W: 1280, H: 960, Sizes: []int{100, 5000, 20000}},
	}}
	mm := &tg.MessageMediaPhoto{}
	mm.SetPhoto(p)
	m := &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 1}}
	m.SetMedia(mm)
	return m
}

func docMessage(mm *tg.MessageMediaDocument) *tg.Message {
	m := &tg.Message{ID: 2, PeerID: &tg.PeerUser{UserID: 1}}
	m.SetMedia(mm)
	return m
}

func doc(id int64, mime string, attrs ...tg.DocumentAttributeClass) *tg.Document {
	return &tg.Document{ID: id, AccessHash: 3, FileReference: []byte("f"), MimeType: mime, Size: 4096, Attributes: attrs}
}

func withDoc(d *tg.Document, set func(*tg.MessageMediaDocument)) *tg.MessageMediaDocument {
	mm := &tg.MessageMediaDocument{}
	mm.SetDocument(d)
	if set != nil {
		set(mm)
	}
	return mm
}

func TestExtractPhotoPicksLargestSize(t *testing.T) {
	got, ok := convert.ExtractMedia(photoMessage())
	if !ok {
		t.Fatal("no media")
	}
	if got.Key != "photo:777" || got.Kind != "photo" || got.Size != 20000 || got.Mime != "image/jpeg" {
		t.Fatalf("media = %+v", got)
	}
	loc, err := convert.FileLocation(photoMessage())
	if err != nil {
		t.Fatal(err)
	}
	pl, ok := loc.(*tg.InputPhotoFileLocation)
	if !ok || pl.ID != 777 || pl.ThumbSize != "y" || string(pl.FileReference) != "r" {
		t.Fatalf("location = %#v", loc)
	}
}

func TestExtractDocumentKinds(t *testing.T) {
	cases := []struct {
		name string
		mm   *tg.MessageMediaDocument
		kind string
		file string
	}{
		{"pdf", withDoc(doc(1, "application/pdf", &tg.DocumentAttributeFilename{FileName: "отчёт.pdf"}), nil), "document", "отчёт.pdf"},
		{"voice flag", withDoc(doc(2, "audio/ogg", &tg.DocumentAttributeAudio{Voice: true}), func(m *tg.MessageMediaDocument) { m.Voice = true }), "voice", ""},
		{"music", withDoc(doc(3, "audio/mpeg", &tg.DocumentAttributeAudio{}, &tg.DocumentAttributeFilename{FileName: "a.mp3"}), nil), "audio", "a.mp3"},
		{"round video", withDoc(doc(4, "video/mp4", &tg.DocumentAttributeVideo{RoundMessage: true}), func(m *tg.MessageMediaDocument) { m.Round = true }), "video_note", ""},
		{"video", withDoc(doc(5, "video/mp4", &tg.DocumentAttributeVideo{}), nil), "video", ""},
		{"gif", withDoc(doc(6, "video/mp4", &tg.DocumentAttributeVideo{}, &tg.DocumentAttributeAnimated{}), nil), "animation", ""},
		{"sticker", withDoc(doc(7, "image/webp", &tg.DocumentAttributeSticker{Alt: "😀"}), nil), "sticker", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := convert.ExtractMedia(docMessage(c.mm))
			if !ok || got.Kind != c.kind || got.FileName != c.file || got.Size != 4096 {
				t.Fatalf("media = %+v ok=%v, want kind %s file %q", got, ok, c.kind, c.file)
			}
		})
	}
	loc, err := convert.FileLocation(docMessage(cases[0].mm))
	if err != nil {
		t.Fatal(err)
	}
	if dl, ok := loc.(*tg.InputDocumentFileLocation); !ok || dl.ID != 1 || dl.AccessHash != 3 {
		t.Fatalf("location = %#v", loc)
	}
}

func TestNonFileMediaIgnored(t *testing.T) {
	m := &tg.Message{ID: 3, PeerID: &tg.PeerUser{UserID: 1}}
	m.SetMedia(&tg.MessageMediaGeo{Geo: &tg.GeoPoint{Lat: 1, Long: 2}})
	if _, ok := convert.ExtractMedia(m); ok {
		t.Fatal("geo treated as file")
	}
	if _, err := convert.FileLocation(m); err == nil {
		t.Fatal("FileLocation for geo must fail")
	}
}

func TestMessageResultCarriesMedia(t *testing.T) {
	res, ok, err := convert.Message(photoMessage(), convert.Options{})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if len(res.Media) != 1 || res.Media[0].Key != "photo:777" || len(res.Message.MediaKeys) != 1 || res.Message.MediaKeys[0] != "photo:777" {
		t.Fatalf("result = %+v", res)
	}
}
```

- [ ] **Step 2: Убедиться, что тесты падают**

Run: `go test ./internal/convert/`
Expected: FAIL — `undefined: convert.ExtractMedia`.

- [ ] **Step 3: Реализация**

`internal/convert/media.go`:

```go
package convert

import (
	"errors"
	"fmt"

	"github.com/gotd/td/tg"

	"gitlab.com/joeunderwood/tgbup/internal/store"
)

func ExtractMedia(m *tg.Message) (store.Media, bool) {
	mc, ok := m.GetMedia()
	if !ok {
		return store.Media{}, false
	}
	switch v := mc.(type) {
	case *tg.MessageMediaPhoto:
		p, ok := photoOf(v)
		if !ok {
			return store.Media{}, false
		}
		_, size, ok := largestPhotoSize(p)
		if !ok {
			return store.Media{}, false
		}
		return store.Media{Key: fmt.Sprintf("photo:%d", p.ID), Kind: "photo", Mime: "image/jpeg", Size: size}, true
	case *tg.MessageMediaDocument:
		d, ok := documentOf(v)
		if !ok {
			return store.Media{}, false
		}
		return store.Media{
			Key:      fmt.Sprintf("document:%d", d.ID),
			Kind:     documentKind(v, d),
			FileName: documentFileName(d),
			Mime:     d.MimeType,
			Size:     d.Size,
		}, true
	}
	return store.Media{}, false
}

func FileLocation(mc tg.MessageClass) (tg.InputFileLocationClass, error) {
	m, ok := mc.(*tg.Message)
	if !ok {
		return nil, errors.New("not a regular message")
	}
	media, ok := m.GetMedia()
	if !ok {
		return nil, errors.New("message has no media")
	}
	switch v := media.(type) {
	case *tg.MessageMediaPhoto:
		p, ok := photoOf(v)
		if !ok {
			return nil, errors.New("photo is empty")
		}
		thumb, _, ok := largestPhotoSize(p)
		if !ok {
			return nil, errors.New("photo has no downloadable size")
		}
		return &tg.InputPhotoFileLocation{ID: p.ID, AccessHash: p.AccessHash, FileReference: p.FileReference, ThumbSize: thumb}, nil
	case *tg.MessageMediaDocument:
		d, ok := documentOf(v)
		if !ok {
			return nil, errors.New("document is empty")
		}
		return &tg.InputDocumentFileLocation{ID: d.ID, AccessHash: d.AccessHash, FileReference: d.FileReference}, nil
	}
	return nil, fmt.Errorf("media %T has no file", media)
}

func photoOf(v *tg.MessageMediaPhoto) (*tg.Photo, bool) {
	pc, ok := v.GetPhoto()
	if !ok {
		return nil, false
	}
	p, ok := pc.(*tg.Photo)
	return p, ok
}

func documentOf(v *tg.MessageMediaDocument) (*tg.Document, bool) {
	dc, ok := v.GetDocument()
	if !ok {
		return nil, false
	}
	d, ok := dc.(*tg.Document)
	return d, ok
}

func largestPhotoSize(p *tg.Photo) (thumbType string, size int64, ok bool) {
	best := -1
	for _, sc := range p.Sizes {
		var (
			typ     string
			area, n int
		)
		switch s := sc.(type) {
		case *tg.PhotoSize:
			typ, area, n = s.Type, s.W*s.H, s.Size
		case *tg.PhotoSizeProgressive:
			if len(s.Sizes) == 0 {
				continue
			}
			typ, area, n = s.Type, s.W*s.H, s.Sizes[len(s.Sizes)-1]
		default:
			continue
		}
		if area > best {
			best, thumbType, size, ok = area, typ, int64(n), true
		}
	}
	return thumbType, size, ok
}

func documentKind(mm *tg.MessageMediaDocument, d *tg.Document) string {
	var sticker, animated, round, voice, video, audio bool
	round, voice = mm.Round, mm.Voice
	for _, a := range d.Attributes {
		switch v := a.(type) {
		case *tg.DocumentAttributeSticker:
			sticker = true
		case *tg.DocumentAttributeAnimated:
			animated = true
		case *tg.DocumentAttributeVideo:
			video = true
			round = round || v.RoundMessage
		case *tg.DocumentAttributeAudio:
			audio = true
			voice = voice || v.Voice
		}
	}
	switch {
	case sticker:
		return "sticker"
	case animated:
		return "animation"
	case round:
		return "video_note"
	case voice:
		return "voice"
	case video:
		return "video"
	case audio:
		return "audio"
	}
	return "document"
}

func documentFileName(d *tg.Document) string {
	for _, a := range d.Attributes {
		if f, ok := a.(*tg.DocumentAttributeFilename); ok {
			return f.FileName
		}
	}
	return ""
}
```

В `internal/convert/message.go`, в функции `regular`, перед `return Result{Message: out}, true, nil` заменить возврат на:

```go
	res := Result{Message: out}
	if md, ok := ExtractMedia(m); ok {
		res.Media = []store.Media{md}
		res.Message.MediaKeys = []string{md.Key}
	}
	return res, true, nil
```

- [ ] **Step 4: Прогнать тесты**

Run: `go test ./internal/convert/ -v`
Expected: PASS (включая тесты Task 5).

- [ ] **Step 5: Commit**

```bash
git add internal/convert
git commit -m "feat: extract downloadable media and file locations from messages"
```

---

### Task 7: tgclient — API над Telegram и классификация ошибок

**Files:**
- Create: `internal/tgclient/api.go`, `internal/tgclient/errors.go`
- Test: `internal/tgclient/api_test.go`, `internal/tgclient/errors_test.go`

**Interfaces:**
- Produces:
  - `type Page struct{ Messages []tg.MessageClass; Users []tg.UserClass }`
  - `type Topic struct{ ID int; Title string; Closed bool; Date int }`
  - `type Telegram interface { HistoryBefore(ctx, peer tg.InputPeerClass, beforeID, limit int) (Page, error); HistoryAfter(ctx, peer tg.InputPeerClass, afterID, limit int) (Page, error); PinnedIDs(ctx, peer tg.InputPeerClass) ([]int, error); ForumTopics(ctx, peer tg.InputPeerClass) ([]Topic, error); Message(ctx, peer tg.InputPeerClass, id int) (tg.MessageClass, error); FileChunk(ctx, loc tg.InputFileLocationClass, offset int64, limit int) ([]byte, error) }`
  - `type API struct{ raw *tg.Client }`, `func NewAPI(raw *tg.Client) *API` (реализует `Telegram`)
  - `type ErrKind int` с `KindOther`, `KindFlood`, `KindInaccessible`, `KindAuth`, `KindFileReference`, `KindCanceled`; `func Classify(err error) ErrKind`
- Контракт: `HistoryBefore` возвращает сообщения с `id < beforeID` (или самые новые при `beforeID == 0`) по убыванию ID; `HistoryAfter` — с `id > afterID` по возрастанию, не больше `limit`, **непрерывным окном сразу над `afterID`**.

- [ ] **Step 1: Написать падающие тесты**

`internal/tgclient/api_test.go`:

```go
package tgclient_test

import (
	"context"
	"slices"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgmock"

	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

var peer = &tg.InputPeerChannel{ChannelID: 1, AccessHash: 2}

func msgs(ids ...int) []tg.MessageClass {
	out := make([]tg.MessageClass, len(ids))
	for i, id := range ids {
		out[i] = &tg.Message{ID: id, PeerID: &tg.PeerChannel{ChannelID: 1}}
	}
	return out
}

func ids(p tgclient.Page) []int {
	var out []int
	for _, m := range p.Messages {
		out = append(out, m.GetID())
	}
	return out
}

func TestHistoryAfterRequestsWindowAboveCursor(t *testing.T) {
	mock := tgmock.NewRequire(t)
	mock.ExpectFunc(func(b bin.Encoder) {
		req, ok := b.(*tg.MessagesGetHistoryRequest)
		if !ok {
			t.Fatalf("request %T", b)
		}
		if req.OffsetID != 11 || req.AddOffset != -3 || req.Limit != 3 || req.MinID != 10 {
			t.Fatalf("request = %+v", req)
		}
	}).ThenResult(&tg.MessagesChannelMessages{Messages: msgs(13, 12, 11, 10)})

	page, err := tgclient.NewAPI(tg.NewClient(mock)).HistoryAfter(context.Background(), peer, 10, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(page); !slices.Equal(got, []int{11, 12, 13}) {
		t.Fatalf("ids = %v", got)
	}
}

func TestHistoryBeforeFiltersAndSortsDesc(t *testing.T) {
	mock := tgmock.NewRequire(t)
	mock.ExpectFunc(func(b bin.Encoder) {
		req := b.(*tg.MessagesGetHistoryRequest)
		if req.OffsetID != 50 || req.Limit != 100 || req.AddOffset != 0 {
			t.Fatalf("request = %+v", req)
		}
	}).ThenResult(&tg.MessagesChannelMessages{Messages: msgs(48, 49, 50)})

	page, err := tgclient.NewAPI(tg.NewClient(mock)).HistoryBefore(context.Background(), peer, 50, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(page); !slices.Equal(got, []int{49, 48}) {
		t.Fatalf("ids = %v", got)
	}
}

func TestPinnedIDsPages(t *testing.T) {
	mock := tgmock.NewRequire(t)
	first := make([]int, 100)
	for i := range first {
		first[i] = 300 - i
	}
	mock.ExpectFunc(func(b bin.Encoder) {
		req := b.(*tg.MessagesSearchRequest)
		if _, ok := req.Filter.(*tg.InputMessagesFilterPinned); !ok || req.OffsetID != 0 {
			t.Fatalf("request = %+v", req)
		}
	}).ThenResult(&tg.MessagesChannelMessages{Messages: msgs(first...)})
	mock.ExpectFunc(func(b bin.Encoder) {
		if req := b.(*tg.MessagesSearchRequest); req.OffsetID != 201 {
			t.Fatalf("second offset = %d", req.OffsetID)
		}
	}).ThenResult(&tg.MessagesChannelMessages{Messages: msgs(5)})

	got, err := tgclient.NewAPI(tg.NewClient(mock)).PinnedIDs(context.Background(), peer)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 101 || got[100] != 5 {
		t.Fatalf("pinned = %d items, last %d", len(got), got[len(got)-1])
	}
}

// peer and from_id are required TL fields: a nil value fails encoding in the mock.
func topic(id int, title string, closed bool, date int) *tg.ForumTopic {
	return &tg.ForumTopic{
		ID: id, Title: title, Closed: closed, Date: date,
		Peer: &tg.PeerChannel{ChannelID: 1}, FromID: &tg.PeerUser{UserID: 1},
	}
}

func TestForumTopics(t *testing.T) {
	mock := tgmock.NewRequire(t)
	mock.ExpectFunc(func(b bin.Encoder) {
		if _, ok := b.(*tg.MessagesGetForumTopicsRequest); !ok {
			t.Fatalf("request %T", b)
		}
	}).ThenResult(&tg.MessagesForumTopics{Topics: []tg.ForumTopicClass{
		topic(1, "General", false, 10),
		topic(7, "Рецепты", true, 20),
		&tg.ForumTopicDeleted{ID: 9},
	}})
	got, err := tgclient.NewAPI(tg.NewClient(mock)).ForumTopics(context.Background(), peer)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1] != (tgclient.Topic{ID: 7, Title: "Рецепты", Closed: true, Date: 20}) {
		t.Fatalf("topics = %+v", got)
	}
}

func TestFileChunk(t *testing.T) {
	mock := tgmock.NewRequire(t)
	loc := &tg.InputDocumentFileLocation{ID: 5}
	mock.ExpectFunc(func(b bin.Encoder) {
		req := b.(*tg.UploadGetFileRequest)
		if req.Offset != 524288 || req.Limit != 524288 || req.CDNSupported {
			t.Fatalf("request = %+v", req)
		}
	}).ThenResult(&tg.UploadFile{Type: &tg.StorageFilePartial{}, Bytes: []byte("abc")})
	got, err := tgclient.NewAPI(tg.NewClient(mock)).FileChunk(context.Background(), loc, 524288, 524288)
	if err != nil || string(got) != "abc" {
		t.Fatalf("chunk = %q %v", got, err)
	}
}
```

`internal/tgclient/errors_test.go`:

```go
package tgclient_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gotd/td/tgerr"

	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		want tgclient.ErrKind
	}{
		{tgerr.New(420, "FLOOD_WAIT_30"), tgclient.KindFlood},
		{fmt.Errorf("wrapped: %w", tgerr.New(420, "FLOOD_WAIT_900")), tgclient.KindFlood},
		{tgerr.New(401, "AUTH_KEY_UNREGISTERED"), tgclient.KindAuth},
		{tgerr.New(401, "SESSION_REVOKED"), tgclient.KindAuth},
		{tgerr.New(400, "CHANNEL_PRIVATE"), tgclient.KindInaccessible},
		{tgerr.New(403, "CHAT_FORBIDDEN"), tgclient.KindInaccessible},
		{tgerr.New(400, "FILE_REFERENCE_EXPIRED"), tgclient.KindFileReference},
		{fmt.Errorf("x: %w", context.Canceled), tgclient.KindCanceled},
		{errors.New("boom"), tgclient.KindOther},
	}
	for _, c := range cases {
		if got := tgclient.Classify(c.err); got != c.want {
			t.Errorf("Classify(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Убедиться, что тесты падают**

Run: `go test ./internal/tgclient/`
Expected: FAIL — пакет не компилируется.

- [ ] **Step 3: Реализация**

`internal/tgclient/api.go`:

```go
package tgclient

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/gotd/td/tg"
)

type Page struct {
	Messages []tg.MessageClass
	Users    []tg.UserClass
}

type Topic struct {
	ID     int
	Title  string
	Closed bool
	Date   int
}

type Telegram interface {
	HistoryBefore(ctx context.Context, peer tg.InputPeerClass, beforeID, limit int) (Page, error)
	HistoryAfter(ctx context.Context, peer tg.InputPeerClass, afterID, limit int) (Page, error)
	PinnedIDs(ctx context.Context, peer tg.InputPeerClass) ([]int, error)
	ForumTopics(ctx context.Context, peer tg.InputPeerClass) ([]Topic, error)
	Message(ctx context.Context, peer tg.InputPeerClass, id int) (tg.MessageClass, error)
	FileChunk(ctx context.Context, loc tg.InputFileLocationClass, offset int64, limit int) ([]byte, error)
}

type API struct {
	raw *tg.Client
}

func NewAPI(raw *tg.Client) *API { return &API{raw: raw} }

func (a *API) HistoryBefore(ctx context.Context, peer tg.InputPeerClass, beforeID, limit int) (Page, error) {
	res, err := a.raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, OffsetID: beforeID, Limit: limit})
	if err != nil {
		return Page{}, err
	}
	p, err := toPage(res)
	if err != nil {
		return Page{}, err
	}
	if beforeID > 0 {
		p.Messages = slices.DeleteFunc(p.Messages, func(m tg.MessageClass) bool { return m.GetID() >= beforeID })
	}
	slices.SortFunc(p.Messages, func(x, y tg.MessageClass) int { return y.GetID() - x.GetID() })
	return p, nil
}

// A negative add_offset of -limit moves the window to the messages right above
// offset_id, which gives contiguous forward paging from a cursor.
func (a *API) HistoryAfter(ctx context.Context, peer tg.InputPeerClass, afterID, limit int) (Page, error) {
	res, err := a.raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer: peer, OffsetID: afterID + 1, AddOffset: -limit, Limit: limit, MinID: afterID,
	})
	if err != nil {
		return Page{}, err
	}
	p, err := toPage(res)
	if err != nil {
		return Page{}, err
	}
	p.Messages = slices.DeleteFunc(p.Messages, func(m tg.MessageClass) bool { return m.GetID() <= afterID })
	slices.SortFunc(p.Messages, func(x, y tg.MessageClass) int { return x.GetID() - y.GetID() })
	return p, nil
}

func (a *API) PinnedIDs(ctx context.Context, peer tg.InputPeerClass) ([]int, error) {
	const limit = 100
	var ids []int
	offset := 0
	for {
		res, err := a.raw.MessagesSearch(ctx, &tg.MessagesSearchRequest{
			Peer: peer, Filter: &tg.InputMessagesFilterPinned{}, OffsetID: offset, Limit: limit,
		})
		if err != nil {
			return nil, err
		}
		p, err := toPage(res)
		if err != nil {
			return nil, err
		}
		for _, m := range p.Messages {
			ids = append(ids, m.GetID())
			if offset == 0 || m.GetID() < offset {
				offset = m.GetID()
			}
		}
		if len(p.Messages) < limit {
			return ids, nil
		}
	}
}

func (a *API) ForumTopics(ctx context.Context, peer tg.InputPeerClass) ([]Topic, error) {
	req := &tg.MessagesGetForumTopicsRequest{Peer: peer, Limit: 100}
	var out []Topic
	for {
		res, err := a.raw.MessagesGetForumTopics(ctx, req)
		if err != nil {
			return nil, err
		}
		var last *tg.ForumTopic
		for _, tc := range res.Topics {
			t, ok := tc.(*tg.ForumTopic)
			if !ok {
				continue
			}
			out = append(out, Topic{ID: t.ID, Title: t.Title, Closed: t.Closed, Date: t.Date})
			last = t
		}
		if len(res.Topics) < req.Limit || last == nil {
			return out, nil
		}
		req.OffsetDate, req.OffsetID, req.OffsetTopic = last.Date, last.TopMessage, last.ID
	}
}

func (a *API) Message(ctx context.Context, peer tg.InputPeerClass, id int) (tg.MessageClass, error) {
	ids := []tg.InputMessageClass{&tg.InputMessageID{ID: id}}
	var (
		res tg.MessagesMessagesClass
		err error
	)
	if ch, ok := peer.(*tg.InputPeerChannel); ok {
		res, err = a.raw.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash}, ID: ids,
		})
	} else {
		res, err = a.raw.MessagesGetMessages(ctx, ids)
	}
	if err != nil {
		return nil, err
	}
	p, err := toPage(res)
	if err != nil {
		return nil, err
	}
	for _, m := range p.Messages {
		if _, empty := m.(*tg.MessageEmpty); !empty && m.GetID() == id {
			return m, nil
		}
	}
	return nil, fmt.Errorf("message %d not found", id)
}

func (a *API) FileChunk(ctx context.Context, loc tg.InputFileLocationClass, offset int64, limit int) ([]byte, error) {
	res, err := a.raw.UploadGetFile(ctx, &tg.UploadGetFileRequest{Location: loc, Offset: offset, Limit: limit})
	if err != nil {
		return nil, err
	}
	f, ok := res.(*tg.UploadFile)
	if !ok {
		return nil, fmt.Errorf("unexpected upload.getFile result %T", res)
	}
	return f.Bytes, nil
}

func toPage(res tg.MessagesMessagesClass) (Page, error) {
	m, ok := res.AsModified()
	if !ok {
		return Page{}, errors.New("unexpected messages.messagesNotModified")
	}
	return Page{Messages: m.GetMessages(), Users: m.GetUsers()}, nil
}
```

`internal/tgclient/errors.go`:

```go
package tgclient

import (
	"context"
	"errors"
	"strings"

	"github.com/gotd/td/tgerr"
)

type ErrKind int

const (
	KindOther ErrKind = iota
	KindFlood
	KindInaccessible
	KindAuth
	KindFileReference
	KindCanceled
)

func (k ErrKind) String() string {
	return [...]string{"other", "flood", "inaccessible", "auth", "file_reference", "canceled"}[k]
}

func Classify(err error) ErrKind {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return KindCanceled
	}
	if _, ok := tgerr.AsFloodWait(err); ok {
		return KindFlood
	}
	if tgerr.Is(err, "AUTH_KEY_UNREGISTERED", "AUTH_KEY_INVALID", "AUTH_KEY_DUPLICATED",
		"SESSION_REVOKED", "SESSION_EXPIRED", "USER_DEACTIVATED", "USER_DEACTIVATED_BAN") {
		return KindAuth
	}
	if tgerr.Is(err, "CHANNEL_PRIVATE", "CHANNEL_INVALID", "CHAT_FORBIDDEN", "CHAT_ID_INVALID",
		"PEER_ID_INVALID", "USER_BANNED_IN_CHANNEL", "CHANNEL_PUBLIC_GROUP_NA") {
		return KindInaccessible
	}
	if rpc, ok := tgerr.As(err); ok && strings.HasPrefix(rpc.Type, "FILE_REFERENCE_") {
		return KindFileReference
	}
	return KindOther
}
```

- [ ] **Step 4: Прогнать тесты**

Run: `go test ./internal/tgclient/ -v`
Expected: PASS. Если `tgerr.New(420, "FLOOD_WAIT_30")` не парсит аргумент — проверить в `tgerr/error.go` сигнатуру конструктора и использовать её (например `&tgerr.Error{Code: 420, Type: "FLOOD_WAIT", Argument: 30}`).

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/tgclient
git commit -m "feat: add Telegram history, pinned, topics and file chunk API with error classes"
```

---

### Task 8: tgclient — выбор чатов (папки + include − exclude)

**Files:**
- Create: `internal/tgclient/targets.go`
- Test: `internal/tgclient/targets_test.go`

**Interfaces:**
- Consumes: `config.Targets`, `config.PeerRef`, `convert.ChatID/ChannelID/InputPeerID`, `store.Chat*` константы, `API` из Task 7.
- Produces:
  - `type Dialog struct{ ID int64; Peer tg.InputPeerClass; Type, Title, Username string; Contact, Bot, Archived bool }`
  - `type Folder struct{ Title string; Pinned, Include, Exclude []int64; Contacts, NonContacts, Groups, Broadcasts, Bots, ExcludeArchived bool }`
  - `type Target struct{ Chat store.Chat; Peer tg.InputPeerClass }`, `func (Target) IsForum() bool`
  - `func SelectTargets(dialogs []Dialog, folders []Folder, sel config.Targets) (targets []Target, warnings []string, err error)`
  - `func (*API) Dialogs(ctx) ([]Dialog, error)`, `func (*API) Folders(ctx) ([]Folder, error)`, `func (*API) AddMigrated(ctx, []Target) ([]Target, []string)`

- [ ] **Step 1: Написать падающие тесты**

`internal/tgclient/targets_test.go`:

```go
package tgclient_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgmock"

	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/store"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

var dialogs = []tgclient.Dialog{
	{ID: 10, Type: store.ChatPrivate, Title: "Мама", Contact: true},
	{ID: 11, Type: store.ChatPrivate, Title: "Незнакомец"},
	{ID: 12, Type: store.ChatPrivate, Title: "Бот", Bot: true},
	{ID: -20, Type: store.ChatGroup, Title: "Старая группа"},
	{ID: -1000000000030, Type: store.ChatForum, Title: "Семейный форум", Username: "FamForum"},
	{ID: -1000000000040, Type: store.ChatChannel, Title: "Новости"},
	{ID: -1000000000050, Type: store.ChatSupergroup, Title: "Архивная", Archived: true},
}

func chatIDs(ts []tgclient.Target) []int64 {
	var out []int64
	for _, t := range ts {
		out = append(out, t.Chat.ID)
	}
	return out
}

func TestSelectByFolderRules(t *testing.T) {
	folders := []tgclient.Folder{
		{Title: "Семья", Include: []int64{10}, Pinned: []int64{-1000000000030}},
		{Title: "Группы", Groups: true, ExcludeArchived: true, Exclude: []int64{-20}},
		{Title: "Люди", Contacts: true, NonContacts: true},
	}
	got, warns, err := tgclient.SelectTargets(dialogs, folders, config.Targets{Folders: []string{"Семья", "Группы", "Люди"}})
	if err != nil || len(warns) != 0 {
		t.Fatal(err, warns)
	}
	// -20 is excluded by "Группы", -…050 is archived, bot 12 matches no rule.
	want := []int64{-1000000000030, 10, 11}
	if !slices.Equal(chatIDs(got), want) {
		t.Fatalf("targets = %v, want %v", chatIDs(got), want)
	}
	for _, tg := range got {
		if tg.Chat.ID == -1000000000030 && !slices.Equal(tg.Chat.Folders, []string{"Семья", "Группы"}) {
			t.Errorf("forum folders = %v", tg.Chat.Folders)
		}
		if tg.Chat.ID == -1000000000030 && !tg.IsForum() {
			t.Error("forum not detected")
		}
	}
}

func TestSelectIncludeExcludeAndUnknownPeers(t *testing.T) {
	folders := []tgclient.Folder{
		// 999 and self-like peers are in the folder but not among dialogs: must be ignored, not crash.
		{Title: "Все", Include: []int64{999}, Groups: true, Broadcasts: true},
	}
	sel := config.Targets{
		Folders: []string{"Все"},
		Include: []config.PeerRef{"@famforum", "12", "@nobody"},
		Exclude: []config.PeerRef{"-1000000000040"},
	}
	got, warns, err := tgclient.SelectTargets(dialogs, folders, sel)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{-1000000000050, -1000000000030, -20, 12}
	if !slices.Equal(chatIDs(got), want) {
		t.Fatalf("targets = %v, want %v", chatIDs(got), want)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "@nobody") {
		t.Fatalf("warnings = %v", warns)
	}
}

func TestSelectUnknownFolderIsError(t *testing.T) {
	_, _, err := tgclient.SelectTargets(dialogs, nil, config.Targets{Folders: []string{"Работа"}})
	if err == nil || !strings.Contains(err.Error(), `"Работа"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestFoldersFromTelegram(t *testing.T) {
	mock := tgmock.NewRequire(t)
	mock.ExpectCall(&tg.MessagesGetDialogFiltersRequest{}).ThenResult(&tg.MessagesDialogFilters{Filters: []tg.DialogFilterClass{
		&tg.DialogFilterDefault{},
		&tg.DialogFilter{
			ID: 2, Title: tg.TextWithEntities{Text: "Семья"}, Groups: true,
			IncludePeers: []tg.InputPeerClass{&tg.InputPeerUser{UserID: 10}, &tg.InputPeerSelf{}},
			ExcludePeers: []tg.InputPeerClass{&tg.InputPeerChat{ChatID: 20}},
			PinnedPeers:  []tg.InputPeerClass{&tg.InputPeerChannel{ChannelID: 30}},
		},
		&tg.DialogFilterChatlist{ID: 3, Title: tg.TextWithEntities{Text: "Общая"}, IncludePeers: []tg.InputPeerClass{&tg.InputPeerUser{UserID: 11}}},
	}})
	got, err := tgclient.NewAPI(tg.NewClient(mock)).Folders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("folders = %+v", got)
	}
	f := got[0]
	if f.Title != "Семья" || !f.Groups || !slices.Equal(f.Include, []int64{10}) || !slices.Equal(f.Exclude, []int64{-20}) || !slices.Equal(f.Pinned, []int64{-1000000000030}) {
		t.Fatalf("folder = %+v", f)
	}
	if got[1].Title != "Общая" || !slices.Equal(got[1].Include, []int64{11}) {
		t.Fatalf("chatlist = %+v", got[1])
	}
}
```

- [ ] **Step 2: Убедиться, что тесты падают**

Run: `go test ./internal/tgclient/`
Expected: FAIL — `undefined: tgclient.SelectTargets`.

- [ ] **Step 3: Реализация**

`internal/tgclient/targets.go`:

```go
package tgclient

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/tg"

	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/convert"
	"gitlab.com/joeunderwood/tgbup/internal/store"
)

type Dialog struct {
	ID       int64
	Peer     tg.InputPeerClass
	Type     string
	Title    string
	Username string
	Contact  bool
	Bot      bool
	Archived bool
}

// Folder mirrors a Telegram dialog filter. exclude_muted/exclude_read are ignored on
// purpose: they change constantly and would make the archived set flap.
type Folder struct {
	Title           string
	Pinned          []int64
	Include         []int64
	Exclude         []int64
	Contacts        bool
	NonContacts     bool
	Groups          bool
	Broadcasts      bool
	Bots            bool
	ExcludeArchived bool
}

type Target struct {
	Chat store.Chat
	Peer tg.InputPeerClass
}

func (t Target) IsForum() bool { return t.Chat.Type == store.ChatForum }

func (f Folder) matches(d Dialog) bool {
	if slices.Contains(f.Exclude, d.ID) {
		return false
	}
	if slices.Contains(f.Pinned, d.ID) || slices.Contains(f.Include, d.ID) {
		return true
	}
	if f.ExcludeArchived && d.Archived {
		return false
	}
	switch d.Type {
	case store.ChatPrivate:
		if d.Bot {
			return f.Bots
		}
		if d.Contact {
			return f.Contacts
		}
		return f.NonContacts
	case store.ChatGroup, store.ChatSupergroup, store.ChatForum:
		return f.Groups
	case store.ChatChannel:
		return f.Broadcasts
	}
	return false
}

func SelectTargets(dialogs []Dialog, folders []Folder, sel config.Targets) ([]Target, []string, error) {
	byTitle := map[string]Folder{}
	for _, f := range folders {
		byTitle[f.Title] = f
	}
	var (
		chosen []Folder
		errs   []error
	)
	for _, name := range sel.Folders {
		f, ok := byTitle[name]
		if !ok {
			errs = append(errs, fmt.Errorf("telegram folder %q not found", name))
			continue
		}
		chosen = append(chosen, f)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, nil, err
	}

	find := func(ref config.PeerRef) (int64, bool) {
		for _, d := range dialogs {
			if id, ok := ref.ID(); ok && d.ID == id {
				return d.ID, true
			}
			if u, ok := ref.Username(); ok && d.Username != "" && strings.EqualFold(d.Username, u) {
				return d.ID, true
			}
		}
		return 0, false
	}
	var warnings []string
	include, exclude := map[int64]bool{}, map[int64]bool{}
	for _, r := range sel.Include {
		id, ok := find(r)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("include %s: chat not found among your dialogs", string(r)))
			continue
		}
		include[id] = true
	}
	for _, r := range sel.Exclude {
		if id, ok := find(r); ok {
			exclude[id] = true
		}
	}

	var out []Target
	for _, d := range dialogs {
		if exclude[d.ID] {
			continue
		}
		var in []string
		for _, f := range chosen {
			if f.matches(d) {
				in = append(in, f.Title)
			}
		}
		if len(in) == 0 && !include[d.ID] {
			continue
		}
		out = append(out, Target{
			Chat: store.Chat{ID: d.ID, Type: d.Type, Title: d.Title, Username: d.Username, Folders: in},
			Peer: d.Peer,
		})
	}
	slices.SortFunc(out, func(a, b Target) int {
		switch {
		case a.Chat.ID < b.Chat.ID:
			return -1
		case a.Chat.ID > b.Chat.ID:
			return 1
		}
		return 0
	})
	return out, warnings, nil
}

func (a *API) Dialogs(ctx context.Context) ([]Dialog, error) {
	var out []Dialog
	for _, folderID := range []int{0, 1} {
		err := query.NewQuery(a.raw).GetDialogs().FolderID(folderID).BatchSize(100).
			ForEach(ctx, func(ctx context.Context, e dialogs.Elem) error {
				if d, ok := dialogFromElem(e, folderID == 1); ok {
					out = append(out, d)
				}
				return nil
			})
		if err != nil {
			return nil, fmt.Errorf("list dialogs (folder %d): %w", folderID, err)
		}
	}
	return out, nil
}

func dialogFromElem(e dialogs.Elem, archived bool) (Dialog, bool) {
	d := Dialog{Peer: e.Peer, Archived: archived}
	switch p := e.Peer.(type) {
	case *tg.InputPeerUser:
		u, ok := e.Entities.User(p.UserID)
		if !ok || u.Deleted {
			return Dialog{}, false
		}
		d.ID, d.Type, d.Username, d.Contact, d.Bot = p.UserID, store.ChatPrivate, u.Username, u.Contact, u.Bot
		d.Title = strings.TrimSpace(u.FirstName + " " + u.LastName)
	case *tg.InputPeerChat:
		c, ok := e.Entities.Chat(p.ChatID)
		if !ok || c.Deactivated {
			return Dialog{}, false
		}
		d.ID, d.Type, d.Title = convert.ChatID(p.ChatID), store.ChatGroup, c.Title
	case *tg.InputPeerChannel:
		c, ok := e.Entities.Channel(p.ChannelID)
		if !ok {
			return Dialog{}, false
		}
		d.ID, d.Title, d.Username = convert.ChannelID(p.ChannelID), c.Title, c.Username
		switch {
		case c.Forum:
			d.Type = store.ChatForum
		case c.Megagroup:
			d.Type = store.ChatSupergroup
		default:
			d.Type = store.ChatChannel
		}
	default:
		return Dialog{}, false
	}
	return d, true
}

func (a *API) Folders(ctx context.Context) ([]Folder, error) {
	res, err := a.raw.MessagesGetDialogFilters(ctx)
	if err != nil {
		return nil, fmt.Errorf("get dialog filters: %w", err)
	}
	var out []Folder
	for _, fc := range res.Filters {
		switch f := fc.(type) {
		case *tg.DialogFilter:
			out = append(out, Folder{
				Title: f.Title.Text, Pinned: peerIDs(f.PinnedPeers), Include: peerIDs(f.IncludePeers), Exclude: peerIDs(f.ExcludePeers),
				Contacts: f.Contacts, NonContacts: f.NonContacts, Groups: f.Groups, Broadcasts: f.Broadcasts, Bots: f.Bots,
				ExcludeArchived: f.ExcludeArchived,
			})
		case *tg.DialogFilterChatlist:
			out = append(out, Folder{Title: f.Title.Text, Pinned: peerIDs(f.PinnedPeers), Include: peerIDs(f.IncludePeers)})
		}
	}
	return out, nil
}

func peerIDs(ps []tg.InputPeerClass) []int64 {
	var out []int64
	for _, p := range ps {
		if id, ok := convert.InputPeerID(p); ok {
			out = append(out, id)
		}
	}
	return out
}

// AddMigrated links supergroups to the basic group they were upgraded from and adds
// that old group as a target, so its pre-migration history is archived too.
func (a *API) AddMigrated(ctx context.Context, targets []Target) ([]Target, []string) {
	have := map[int64]bool{}
	for _, t := range targets {
		have[t.Chat.ID] = true
	}
	var warnings []string
	out := slices.Clone(targets)
	for i, t := range targets {
		ch, ok := t.Peer.(*tg.InputPeerChannel)
		if !ok || (t.Chat.Type != store.ChatSupergroup && t.Chat.Type != store.ChatForum) {
			continue
		}
		full, err := a.raw.ChannelsGetFullChannel(ctx, &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash})
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("chat %d: cannot check migration: %v", t.Chat.ID, err))
			continue
		}
		cf, ok := full.FullChat.(*tg.ChannelFull)
		if !ok {
			continue
		}
		oldRaw, ok := cf.GetMigratedFromChatID()
		if !ok {
			continue
		}
		old := convert.ChatID(oldRaw)
		out[i].Chat.MigratedFromID = old
		if have[old] {
			continue
		}
		have[old] = true
		out = append(out, Target{
			Chat: store.Chat{ID: old, Type: store.ChatGroup, Title: t.Chat.Title, Folders: t.Chat.Folders},
			Peer: &tg.InputPeerChat{ChatID: oldRaw},
		})
	}
	return out, warnings
}
```

- [ ] **Step 4: Прогнать тесты**

Run: `go test ./internal/tgclient/ -v`
Expected: PASS (включая тесты Task 7).

- [ ] **Step 5: Commit**

```bash
git add internal/tgclient
git commit -m "feat: select archive targets from Telegram folders, include and exclude lists"
```

---

### Task 9: tgclient — подключение, сессия, takeout и `tgbup login`

**Files:**
- Create: `internal/tgclient/client.go`, `internal/tgclient/login.go`, `cmd/tgbup/setup.go`, `cmd/tgbup/login.go`
- Modify: `cmd/tgbup/main.go` (ветка `login` в `run`)
- Test: `internal/tgclient/login_test.go`

**Interfaces:**
- Consumes: `config.Paths`, `config.Secrets`, `config.Config`.
- Produces:
  - `type Options struct{ APIID int; APIHash, SessionPath, Version string; MaxFloodWait time.Duration }`
  - `func Run(ctx, Options, fn func(ctx context.Context, c *telegram.Client) error) error`
  - `var ErrNotLoggedIn`, `func RequireAuth(ctx, *telegram.Client) error`, `func SelfID(ctx, *tg.Client) (int64, error)`
  - `func WithTakeout(ctx, c *telegram.Client, maxFileSize int64, fn func(ctx context.Context, raw *tg.Client) error) error`
  - `func Login(ctx, c *telegram.Client, in io.Reader, out io.Writer, readPassword func() (string, error)) error`
  - в `cmd/tgbup`: `type setup struct{ paths config.Paths; cfg config.Config; secrets config.Secrets }`, `func loadSetup(env Env, requireConfig bool) (setup, error)`, `func (setup) clientOptions() tgclient.Options`

- [ ] **Step 1: Добавить зависимости**

```bash
go get github.com/gotd/contrib golang.org/x/time golang.org/x/term
```

- [ ] **Step 2: Написать падающий тест (терминальный аутентификатор)**

`internal/tgclient/login_test.go`:

```go
package tgclient

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

func TestTerminalAuthPrompts(t *testing.T) {
	var out bytes.Buffer
	a := terminalAuth{
		in:           bufio.NewReader(strings.NewReader(" +79990001122 \n 12345\n")),
		out:          &out,
		readPassword: func() (string, error) { return "secret", nil },
	}
	ctx := context.Background()

	phone, err := a.Phone(ctx)
	if err != nil || phone != "+79990001122" {
		t.Fatalf("phone = %q %v", phone, err)
	}
	code, err := a.Code(ctx, &tg.AuthSentCode{})
	if err != nil || code != "12345" {
		t.Fatalf("code = %q %v", code, err)
	}
	pass, err := a.Password(ctx)
	if err != nil || pass != "secret" {
		t.Fatalf("password = %q %v", pass, err)
	}
	for _, want := range []string{"Phone number", "Login code", "2FA password"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("prompt %q missing in %q", want, out.String())
		}
	}
	if _, err := a.SignUp(ctx); err == nil {
		t.Error("SignUp must refuse")
	}
	var su *auth.SignUpRequired
	if err := a.AcceptTermsOfService(ctx, tg.HelpTermsOfService{}); !errors.As(err, &su) {
		t.Errorf("AcceptTermsOfService = %v", err)
	}
}
```

- [ ] **Step 3: Убедиться, что тест падает**

Run: `go test ./internal/tgclient/ -run TestTerminalAuthPrompts`
Expected: FAIL — `undefined: terminalAuth`.

- [ ] **Step 4: Реализация**

`internal/tgclient/client.go`:

```go
package tgclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/contrib/middleware/ratelimit"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/takeout"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"golang.org/x/time/rate"
)

type Options struct {
	APIID        int
	APIHash      string
	SessionPath  string
	Version      string
	MaxFloodWait time.Duration
}

var ErrNotLoggedIn = errors.New("telegram session is not authorized: run `tgbup login`")

func Run(ctx context.Context, o Options, fn func(ctx context.Context, c *telegram.Client) error) error {
	if err := os.MkdirAll(filepath.Dir(o.SessionPath), 0o700); err != nil {
		return err
	}
	client := telegram.NewClient(o.APIID, o.APIHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: o.SessionPath},
		NoUpdates:      true,
		Middlewares: []telegram.Middleware{
			floodwait.NewSimpleWaiter().WithMaxWait(o.MaxFloodWait),
			ratelimit.New(rate.Every(100*time.Millisecond), 5),
		},
		Device: telegram.DeviceConfig{DeviceModel: "tgbup", SystemVersion: runtime.GOOS, AppVersion: o.Version},
	})
	return client.Run(ctx, func(ctx context.Context) error { return fn(ctx, client) })
}

func RequireAuth(ctx context.Context, c *telegram.Client) error {
	st, err := c.Auth().Status(ctx)
	if err != nil {
		return fmt.Errorf("auth status: %w", err)
	}
	if !st.Authorized {
		return ErrNotLoggedIn
	}
	return nil
}

func SelfID(ctx context.Context, raw *tg.Client) (int64, error) {
	us, err := raw.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
	if err != nil {
		return 0, fmt.Errorf("get self: %w", err)
	}
	for _, uc := range us {
		if u, ok := uc.(*tg.User); ok {
			return u.ID, nil
		}
	}
	return 0, errors.New("get self: empty response")
}

func WithTakeout(ctx context.Context, c *telegram.Client, maxFileSize int64, fn func(ctx context.Context, raw *tg.Client) error) error {
	cfg := takeout.Config{
		MessageUsers: true, MessageChats: true, MessageMegagroups: true, MessageChannels: true,
		Files: true, FileMaxSize: maxFileSize,
	}
	err := takeout.Run(ctx, c, cfg, func(ctx context.Context, tc *takeout.Client) error {
		return fn(ctx, tg.NewClient(tc))
	})
	if rpc, ok := tgerr.As(err); ok && rpc.Type == "TAKEOUT_INIT_DELAY" {
		return fmt.Errorf("telegram wants you to confirm the data export: open Telegram, allow the request "+
			"in the service notifications (or wait %s), then rerun: %w", time.Duration(rpc.Argument)*time.Second, err)
	}
	return err
}
```

`internal/tgclient/login.go`:

```go
package tgclient

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

type terminalAuth struct {
	in           *bufio.Reader
	out          io.Writer
	readPassword func() (string, error)
}

func (a terminalAuth) prompt(q string) (string, error) {
	fmt.Fprint(a.out, q)
	s, err := a.in.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && s != "") {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

func (a terminalAuth) Phone(context.Context) (string, error) {
	return a.prompt("Phone number (international format, e.g. +79990001122): ")
}

func (a terminalAuth) Code(context.Context, *tg.AuthSentCode) (string, error) {
	return a.prompt("Login code from Telegram: ")
}

func (a terminalAuth) Password(context.Context) (string, error) {
	fmt.Fprint(a.out, "2FA password: ")
	return a.readPassword()
}

func (a terminalAuth) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("no Telegram account for this number: sign up in an official app first")
}

func (a terminalAuth) AcceptTermsOfService(_ context.Context, tos tg.HelpTermsOfService) error {
	return &auth.SignUpRequired{TermsOfService: tos}
}

func Login(ctx context.Context, c *telegram.Client, in io.Reader, out io.Writer, readPassword func() (string, error)) error {
	flow := auth.NewFlow(terminalAuth{in: bufio.NewReader(in), out: out, readPassword: readPassword}, auth.SendCodeOptions{})
	return c.Auth().IfNecessary(ctx, flow)
}
```

`cmd/tgbup/setup.go`:

```go
package main

import (
	"errors"
	"io/fs"

	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

type setup struct {
	paths   config.Paths
	cfg     config.Config
	secrets config.Secrets
}

func loadSetup(env Env, requireConfig bool) (setup, error) {
	p, err := config.ResolvePaths(env.Getenv)
	if err != nil {
		return setup{}, err
	}
	cfg, err := config.Load(p.Config, p.Home)
	if err != nil {
		if requireConfig || !errors.Is(err, fs.ErrNotExist) {
			return setup{}, err
		}
		cfg = config.Default()
	}
	sec, err := config.LoadSecrets(p.Secrets, env.Getenv)
	if err != nil {
		return setup{}, err
	}
	if err := sec.RequireTelegram(); err != nil {
		return setup{}, err
	}
	return setup{paths: p, cfg: cfg, secrets: sec}, nil
}

func (s setup) clientOptions() tgclient.Options {
	return tgclient.Options{
		APIID:        s.secrets.APIID,
		APIHash:      s.secrets.APIHash,
		SessionPath:  s.paths.Session(),
		Version:      version,
		MaxFloodWait: s.cfg.Sync.MaxFloodWait.D(),
	}
}
```

`cmd/tgbup/login.go`:

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/gotd/td/telegram"
	"golang.org/x/term"

	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

func cmdLogin(ctx context.Context, env Env) int {
	st, err := loadSetup(env, false)
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup login:", err)
		return 1
	}
	readPassword := func() (string, error) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(env.Stdout)
		return string(b), err
	}
	err = tgclient.Run(ctx, st.clientOptions(), func(ctx context.Context, c *telegram.Client) error {
		if err := tgclient.Login(ctx, c, env.Stdin, env.Stdout, readPassword); err != nil {
			return err
		}
		id, err := tgclient.SelfID(ctx, c.API())
		if err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "logged in as user %d; session saved to %s\n", id, st.paths.Session())
		return nil
	})
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup login:", err)
		return 1
	}
	return 0
}
```

В `cmd/tgbup/main.go` в `switch args[0]` добавить ветку перед `default`:

```go
	case "login":
		return cmdLogin(ctx, env)
```

- [ ] **Step 5: Прогнать тесты и сборку**

Run: `go test ./... && make build`
Expected: PASS, бинарник собирается.

- [ ] **Step 6: Ручная проверка логина** (нужен реальный аккаунт; выполняет человек)

1. На https://my.telegram.org → API development tools создать приложение, записать `api_id`/`api_hash`.
2. `mkdir -p ~/.config/tgbup && printf 'TG_API_ID=...\nTG_API_HASH=...\n' > ~/.config/tgbup/secrets.env && chmod 600 ~/.config/tgbup/secrets.env`
3. `./bin/tgbup login` → телефон, код, при наличии 2FA — пароль (не отображается).
Expected: `logged in as user <id>`; файл `~/.local/state/tgbup/session` с правами `-rw-------`. Повторный `./bin/tgbup login` ничего не спрашивает и сразу печатает ту же строку.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/tgclient cmd/tgbup
git commit -m "feat: connect to Telegram with file session, flood/rate limits and add tgbup login"
```

---

### Task 10: Syncer

**Files:**
- Create: `internal/syncer/syncer.go`
- Test: `internal/syncer/syncer_test.go`

**Interfaces:**
- Consumes: `tgclient.Telegram`, `tgclient.Target`, `tgclient.Classify`, `convert.Message/Users`, `store.*`.
- Produces:
  - `type Syncer struct{ TG tgclient.Telegram; Store *store.Store; SelfID int64; Budget time.Duration; PageSize int; Now func() time.Time; Log *slog.Logger }`
  - `type Report struct{ Chats, NewMessages int; Deferred, NewlyInaccessible []int64; Failed map[int64]error; AuthErr error }`, `func (Report) OK() bool`
  - `func (*Syncer) Run(ctx, targets []tgclient.Target, dialogIDs map[int64]bool) Report`
- Алгоритм курсора (spec §7): нет курсора → `HistoryBefore(0)`; далее каждый запуск: `HistoryAfter(newest)` до пустой/неполной страницы, затем `HistoryBefore(oldest)` пока не `backfill_done` и не истёк бюджет; `backfill_done` ставится только на пустой странице.

- [ ] **Step 1: Написать падающие тесты**

`internal/syncer/syncer_test.go`:

```go
package syncer_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"gitlab.com/joeunderwood/tgbup/internal/convert"
	"gitlab.com/joeunderwood/tgbup/internal/store"
	"gitlab.com/joeunderwood/tgbup/internal/syncer"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

type fakeTG struct {
	mu        sync.Mutex
	ids       map[int64][]int // chat → ascending message ids
	empty     map[int64]map[int]bool
	errs      map[int64]error
	failAfter map[int64]int
	calls     map[int64]int
	before    int
	pinned    map[int64][]int
	topics    map[int64][]tgclient.Topic
	replyTop  map[int]int // message id → topic id (forum)
}

func newFake() *fakeTG {
	return &fakeTG{ids: map[int64][]int{}, empty: map[int64]map[int]bool{}, errs: map[int64]error{},
		failAfter: map[int64]int{}, calls: map[int64]int{}, pinned: map[int64][]int{}, topics: map[int64][]tgclient.Topic{}, replyTop: map[int]int{}}
}

func (f *fakeTG) add(chat int64, from, to int) {
	for i := from; i <= to; i++ {
		f.ids[chat] = append(f.ids[chat], i)
	}
}

func key(p tg.InputPeerClass) int64 { id, _ := convert.InputPeerID(p); return id }

func (f *fakeTG) message(chat int64, id int) tg.MessageClass {
	if f.empty[chat][id] {
		return &tg.MessageEmpty{ID: id}
	}
	m := &tg.Message{ID: id, PeerID: &tg.PeerChannel{ChannelID: -chat - 1000000000000}, Date: 1700000000 + id, Message: "m"}
	m.SetFromID(&tg.PeerUser{UserID: 5})
	if top, ok := f.replyTop[id]; ok {
		h := &tg.MessageReplyHeader{ForumTopic: true}
		h.SetReplyToMsgID(top)
		m.SetReplyTo(h)
	}
	return m
}

func (f *fakeTG) check(chat int64) error {
	f.calls[chat]++
	if err := f.errs[chat]; err != nil {
		return err
	}
	if n, ok := f.failAfter[chat]; ok && f.calls[chat] > n {
		return errors.New("connection reset")
	}
	return nil
}

func (f *fakeTG) page(chat int64, ids []int) tgclient.Page {
	p := tgclient.Page{Users: []tg.UserClass{&tg.User{ID: 5, FirstName: "Ann"}}}
	for _, id := range ids {
		p.Messages = append(p.Messages, f.message(chat, id))
	}
	return p
}

func (f *fakeTG) HistoryBefore(_ context.Context, peer tg.InputPeerClass, beforeID, limit int) (tgclient.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	chat := key(peer)
	f.before++
	if err := f.check(chat); err != nil {
		return tgclient.Page{}, err
	}
	var out []int
	all := f.ids[chat]
	for i := len(all) - 1; i >= 0 && len(out) < limit; i-- {
		if beforeID == 0 || all[i] < beforeID {
			out = append(out, all[i])
		}
	}
	return f.page(chat, out), nil
}

func (f *fakeTG) HistoryAfter(_ context.Context, peer tg.InputPeerClass, afterID, limit int) (tgclient.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	chat := key(peer)
	if err := f.check(chat); err != nil {
		return tgclient.Page{}, err
	}
	var out []int
	for _, id := range f.ids[chat] {
		if id > afterID && len(out) < limit {
			out = append(out, id)
		}
	}
	return f.page(chat, out), nil
}

func (f *fakeTG) PinnedIDs(_ context.Context, peer tg.InputPeerClass) ([]int, error) {
	return f.pinned[key(peer)], nil
}

func (f *fakeTG) ForumTopics(_ context.Context, peer tg.InputPeerClass) ([]tgclient.Topic, error) {
	return f.topics[key(peer)], nil
}

func (f *fakeTG) Message(context.Context, tg.InputPeerClass, int) (tg.MessageClass, error) {
	return nil, errors.New("not used")
}

func (f *fakeTG) FileChunk(context.Context, tg.InputFileLocationClass, int64, int) ([]byte, error) {
	return nil, errors.New("not used")
}

func target(n int64, typ string) tgclient.Target {
	id := convert.ChannelID(n)
	return tgclient.Target{Chat: store.Chat{ID: id, Type: typ, Title: "chat"}, Peer: &tg.InputPeerChannel{ChannelID: n}}
}

type env struct {
	s   *syncer.Syncer
	st  *store.Store
	tg  *fakeTG
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "a.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{st: st, tg: newFake(), now: time.Unix(1700000000, 0)}
	e.s = &syncer.Syncer{
		TG: e.tg, Store: st, SelfID: 1, Budget: time.Hour, PageSize: 100,
		Now: func() time.Time { return e.now }, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return e
}

func dialogsOf(ts ...tgclient.Target) map[int64]bool {
	m := map[int64]bool{}
	for _, t := range ts {
		m[t.Chat.ID] = true
	}
	return m
}

func (e *env) count(t *testing.T, chat int64) int {
	t.Helper()
	n, err := e.st.CountMessages(context.Background(), chat)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestInitialSyncBackfillsEverything(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 250)

	r := e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	if !r.OK() || r.Chats != 1 {
		t.Fatalf("report = %+v", r)
	}
	if n := e.count(t, a.Chat.ID); n != 250 {
		t.Fatalf("messages = %d", n)
	}
	cur, _, _ := e.st.GetCursor(context.Background(), a.Chat.ID)
	if cur != (store.Cursor{NewestID: 250, OldestID: 1, BackfillDone: true}) {
		t.Fatalf("cursor = %+v", cur)
	}
}

func TestIncrementalFetchesOnlyNew(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 120)
	e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))

	e.tg.add(a.Chat.ID, 121, 130)
	e.tg.before = 0
	r := e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	if r.NewMessages != 10 || e.count(t, a.Chat.ID) != 130 {
		t.Fatalf("new=%d total=%d", r.NewMessages, e.count(t, a.Chat.ID))
	}
	if e.tg.before != 0 {
		t.Fatalf("backfill called %d times after it was done", e.tg.before)
	}
}

func TestBudgetStopsBackfillAndNextRunResumes(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 450)
	e.s.Budget = 0 // deadline already reached: the first page still can't be fetched
	e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	if n := e.count(t, a.Chat.ID); n != 0 {
		t.Fatalf("messages with zero budget = %d", n)
	}

	calls := 0
	e.s.Budget = time.Hour
	e.s.Now = func() time.Time { calls++; return e.now.Add(time.Duration(calls) * 25 * time.Minute) }
	e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	partial := e.count(t, a.Chat.ID)
	if partial == 0 || partial == 450 {
		t.Fatalf("expected partial backfill, got %d", partial)
	}

	e.s.Now = func() time.Time { return e.now }
	e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	if n := e.count(t, a.Chat.ID); n != 450 {
		t.Fatalf("after resume = %d", n)
	}
}

func TestInterruptedSyncResumesWithoutGaps(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 450)
	e.tg.failAfter[a.Chat.ID] = 2

	r := e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	if r.OK() || r.Failed[a.Chat.ID] == nil {
		t.Fatalf("expected failure, report = %+v", r)
	}
	if n := e.count(t, a.Chat.ID); n != 200 {
		t.Fatalf("committed before failure = %d, want 200", n)
	}

	delete(e.tg.failAfter, a.Chat.ID)
	e.tg.add(a.Chat.ID, 451, 460)
	if r := e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a)); !r.OK() {
		t.Fatalf("second run = %+v", r)
	}
	if n := e.count(t, a.Chat.ID); n != 460 {
		t.Fatalf("after resume = %d, want 460", n)
	}
}

func TestFloodDefersChatAndContinues(t *testing.T) {
	e := newEnv(t)
	a, b := target(1, store.ChatSupergroup), target(2, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 5)
	e.tg.add(b.Chat.ID, 1, 5)
	e.tg.errs[a.Chat.ID] = tgerr.New(420, "FLOOD_WAIT_3600")

	r := e.s.Run(context.Background(), []tgclient.Target{a, b}, dialogsOf(a, b))
	if !r.OK() || !slices.Equal(r.Deferred, []int64{a.Chat.ID}) {
		t.Fatalf("report = %+v", r)
	}
	if e.count(t, b.Chat.ID) != 5 {
		t.Fatal("second chat not synced")
	}
}

func TestInaccessibleReportedOnce(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.errs[a.Chat.ID] = tgerr.New(400, "CHANNEL_PRIVATE")

	r := e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	if !slices.Equal(r.NewlyInaccessible, []int64{a.Chat.ID}) {
		t.Fatalf("first = %+v", r)
	}
	r = e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	if len(r.NewlyInaccessible) != 0 {
		t.Fatalf("second = %+v", r)
	}
}

func TestChatMissingFromDialogsBecomesInaccessible(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 3)
	e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))

	r := e.s.Run(context.Background(), nil, map[int64]bool{})
	if !slices.Equal(r.NewlyInaccessible, []int64{a.Chat.ID}) {
		t.Fatalf("report = %+v", r)
	}
	if e.count(t, a.Chat.ID) != 3 {
		t.Fatal("archived messages lost")
	}
}

func TestAuthErrorStopsRun(t *testing.T) {
	e := newEnv(t)
	a, b := target(1, store.ChatSupergroup), target(2, store.ChatSupergroup)
	e.tg.errs[a.Chat.ID] = tgerr.New(401, "AUTH_KEY_UNREGISTERED")
	e.tg.add(b.Chat.ID, 1, 5)

	r := e.s.Run(context.Background(), []tgclient.Target{a, b}, dialogsOf(a, b))
	if r.AuthErr == nil || r.OK() {
		t.Fatalf("report = %+v", r)
	}
	if e.tg.calls[b.Chat.ID] != 0 {
		t.Fatal("run continued after auth error")
	}
}

func TestForumTopicsAndPinned(t *testing.T) {
	e := newEnv(t)
	f := target(3, store.ChatForum)
	e.tg.add(f.Chat.ID, 1, 10)
	e.tg.replyTop[5] = 3
	e.tg.topics[f.Chat.ID] = []tgclient.Topic{{ID: 1, Title: "General"}, {ID: 3, Title: "Рецепты", Date: 7}}
	e.tg.pinned[f.Chat.ID] = []int{2, 9}

	if r := e.s.Run(context.Background(), []tgclient.Target{f}, dialogsOf(f)); !r.OK() {
		t.Fatalf("report = %+v", r)
	}
	ctx := context.Background()
	topics, _ := e.st.Topics(ctx, f.Chat.ID)
	if len(topics) != 2 || topics[1].Title != "Рецепты" || topics[1].CreatedAt != 7 {
		t.Fatalf("topics = %+v", topics)
	}
	pinned, _ := e.st.PinnedMessageIDs(ctx, f.Chat.ID)
	if !slices.Equal(pinned, []int{2, 9}) {
		t.Fatalf("pinned = %v", pinned)
	}
}

func TestEmptyMessagesSkippedButCursorAdvances(t *testing.T) {
	e := newEnv(t)
	a := target(1, store.ChatSupergroup)
	e.tg.add(a.Chat.ID, 1, 10)
	e.tg.empty[a.Chat.ID] = map[int]bool{10: true, 4: true}

	e.s.Run(context.Background(), []tgclient.Target{a}, dialogsOf(a))
	if n := e.count(t, a.Chat.ID); n != 8 {
		t.Fatalf("messages = %d, want 8", n)
	}
	cur, _, _ := e.st.GetCursor(context.Background(), a.Chat.ID)
	if cur.NewestID != 10 {
		t.Fatalf("cursor newest = %d, want 10 (past the empty message)", cur.NewestID)
	}
}
```

- [ ] **Step 2: Убедиться, что тесты падают**

Run: `go test ./internal/syncer/`
Expected: FAIL — пакет не компилируется.

- [ ] **Step 3: Реализация**

`internal/syncer/syncer.go`:

```go
package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/gotd/td/tg"

	"gitlab.com/joeunderwood/tgbup/internal/convert"
	"gitlab.com/joeunderwood/tgbup/internal/store"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

type Syncer struct {
	TG       tgclient.Telegram
	Store    *store.Store
	SelfID   int64
	Budget   time.Duration
	PageSize int
	Now      func() time.Time
	Log      *slog.Logger
}

type Report struct {
	Chats             int
	NewMessages       int
	Deferred          []int64
	NewlyInaccessible []int64
	Failed            map[int64]error
	AuthErr           error
}

func (r Report) OK() bool { return len(r.Failed) == 0 && r.AuthErr == nil }

func (s *Syncer) Run(ctx context.Context, targets []tgclient.Target, dialogIDs map[int64]bool) Report {
	r := Report{Failed: map[int64]error{}}
	deadline := s.Now().Add(s.Budget)

	known := map[int64]bool{}
	for id := range dialogIDs {
		known[id] = true
	}
	for _, t := range targets {
		known[t.Chat.ID] = true
	}
	if err := s.markMissing(ctx, known, &r); err != nil {
		r.Failed[0] = fmt.Errorf("mark missing chats: %w", err)
	}

	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			r.Failed[t.Chat.ID] = err
			return r
		}
		n, err := s.syncChat(ctx, t, deadline)
		r.NewMessages += n
		if err == nil {
			r.Chats++
			continue
		}
		switch tgclient.Classify(err) {
		case tgclient.KindFlood:
			s.Log.Warn("chat deferred by flood wait", "chat", t.Chat.ID, "err", err)
			r.Deferred = append(r.Deferred, t.Chat.ID)
		case tgclient.KindInaccessible:
			changed, merr := s.Store.MarkInaccessible(ctx, t.Chat.ID)
			switch {
			case merr != nil:
				r.Failed[t.Chat.ID] = merr
			case changed:
				r.NewlyInaccessible = append(r.NewlyInaccessible, t.Chat.ID)
			}
		case tgclient.KindAuth:
			r.AuthErr = err
			return r
		default:
			s.Log.Error("chat sync failed", "chat", t.Chat.ID, "err", err)
			r.Failed[t.Chat.ID] = err
		}
	}
	return r
}

func (s *Syncer) markMissing(ctx context.Context, known map[int64]bool, r *Report) error {
	ids, err := s.Store.ActiveChatIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if known[id] {
			continue
		}
		changed, err := s.Store.MarkInaccessible(ctx, id)
		if err != nil {
			return err
		}
		if changed {
			r.NewlyInaccessible = append(r.NewlyInaccessible, id)
		}
	}
	return nil
}

func (s *Syncer) syncChat(ctx context.Context, t tgclient.Target, deadline time.Time) (int, error) {
	if err := s.Store.UpsertChat(ctx, t.Chat); err != nil {
		return 0, err
	}
	if t.IsForum() {
		if err := s.syncTopics(ctx, t); err != nil {
			return 0, err
		}
	}
	cur, found, err := s.Store.GetCursor(ctx, t.Chat.ID)
	if err != nil {
		return 0, err
	}
	total := 0

	if found && (cur.NewestID > 0 || cur.BackfillDone) {
		for {
			page, err := s.TG.HistoryAfter(ctx, t.Peer, cur.NewestID, s.PageSize)
			if err != nil {
				return total, err
			}
			if len(page.Messages) == 0 {
				break
			}
			cur.NewestID = max(cur.NewestID, maxID(page.Messages))
			n, err := s.save(ctx, t, page, cur)
			total += n
			if err != nil {
				return total, err
			}
			if len(page.Messages) < s.PageSize {
				break
			}
		}
	}

	for !cur.BackfillDone && s.Now().Before(deadline) {
		page, err := s.TG.HistoryBefore(ctx, t.Peer, cur.OldestID, s.PageSize)
		if err != nil {
			return total, err
		}
		if len(page.Messages) == 0 {
			cur.BackfillDone = true
			if err := s.Store.SaveBatch(ctx, store.Batch{ChatID: t.Chat.ID, Cursor: cur}); err != nil {
				return total, err
			}
			break
		}
		if cur.NewestID == 0 {
			cur.NewestID = maxID(page.Messages)
		}
		cur.OldestID = minID(page.Messages)
		n, err := s.save(ctx, t, page, cur)
		total += n
		if err != nil {
			return total, err
		}
	}

	pinned, err := s.TG.PinnedIDs(ctx, t.Peer)
	if err != nil {
		return total, err
	}
	return total, s.Store.SetPinned(ctx, t.Chat.ID, pinned)
}

func (s *Syncer) syncTopics(ctx context.Context, t tgclient.Target) error {
	topics, err := s.TG.ForumTopics(ctx, t.Peer)
	if err != nil {
		return err
	}
	rows := make([]store.Topic, 0, len(topics))
	for _, tp := range topics {
		rows = append(rows, store.Topic{ChatID: t.Chat.ID, TopicID: tp.ID, Title: tp.Title, Closed: tp.Closed, CreatedAt: int64(tp.Date)})
	}
	return s.Store.UpsertTopics(ctx, rows)
}

func (s *Syncer) save(ctx context.Context, t tgclient.Target, page tgclient.Page, cur store.Cursor) (int, error) {
	b := store.Batch{ChatID: t.Chat.ID, Users: convert.Users(page.Users), Cursor: cur}
	seen := map[string]bool{}
	opts := convert.Options{IsForum: t.IsForum(), SelfID: s.SelfID}
	for _, mc := range page.Messages {
		res, ok, err := convert.Message(mc, opts)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		b.Messages = append(b.Messages, res.Message)
		for _, m := range res.Media {
			if !seen[m.Key] {
				seen[m.Key] = true
				b.Media = append(b.Media, m)
			}
		}
	}
	return len(b.Messages), s.Store.SaveBatch(ctx, b)
}

func maxID(ms []tg.MessageClass) int {
	out := 0
	for _, m := range ms {
		out = max(out, m.GetID())
	}
	return out
}

func minID(ms []tg.MessageClass) int {
	out := ms[0].GetID()
	for _, m := range ms {
		out = min(out, m.GetID())
	}
	return out
}
```

- [ ] **Step 4: Прогнать тесты**

Run: `go test ./internal/syncer/ -v -race`
Expected: PASS всех тестов. В `TestInterruptedSyncResumesWithoutGaps` первая выгрузка коммитит ровно 2 страницы (200), третья падает; повтор дотягивает всё без дублей.

- [ ] **Step 5: Commit**

```bash
git add internal/syncer
git commit -m "feat: incremental per-chat sync with resumable backfill and error handling"
```

---

### Task 11: Загрузка медиа

**Files:**
- Create: `internal/store/media.go`, `internal/media/path.go`, `internal/media/downloader.go`
- Test: `internal/store/media_test.go`, `internal/media/downloader_test.go`

**Interfaces:**
- Consumes: `store` (Task 4), `convert.FileLocation/Encode` (Tasks 5–6), `tgclient.Telegram/Classify` (Task 7).
- Produces:
  - store: `type MediaJob struct{ Key, Kind, FileName, Mime string; Size int64; Attempts int; ChatID int64; MessageID int }`, `type MediaInfo struct{ Status, LocalPath, Error string; Attempts int }`; `PendingMedia(ctx) ([]MediaJob, error)`, `MediaDone(ctx, key, localPath string) error`, `MediaSkipped(ctx, key, reason string) error`, `MediaAttemptFailed(ctx, key, errMsg string, maxAttempts int) error`, `MediaInfo(ctx, key string) (MediaInfo, error)`, `MessageRaw(ctx, chatID int64, id int) ([]byte, error)`, `UpdateMessageRaw(ctx, chatID int64, id int, raw []byte) error`
  - media: `const ChunkSize = 512 * 1024`, `const MaxAttempts = 5`, `func RelPath(j store.MediaJob) string`, `func Ext(fileName, mimeType, kind string) string`
  - `type Downloader struct{ TG tgclient.Telegram; Store *store.Store; ArchiveDir string; Peers map[int64]tg.InputPeerClass; Workers int; MaxFileSize int64; SkipKinds []string; Log *slog.Logger }`
  - `type Report struct{ Done, Skipped, Failed, Deferred int; Errors []error; AuthErr error }`, `func (*Downloader) Run(ctx) (Report, error)`

- [ ] **Step 1: Написать падающий тест store**

`internal/store/media_test.go`:

```go
package store

import (
	"context"
	"testing"
)

func TestMediaQueueLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChat(t, s, chatA)
	err := s.SaveBatch(ctx, Batch{
		ChatID:   chatA,
		Media:    []Media{{Key: "document:1", Kind: "document", Size: 10}, {Key: "photo:2", Kind: "photo"}},
		Messages: []Message{msg(1, "document:1"), msg(2, "photo:2"), msg(3, "document:1")},
	})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := s.PendingMedia(ctx)
	if err != nil || len(jobs) != 2 || jobs[0].Key != "document:1" || jobs[0].ChatID != chatA {
		t.Fatalf("jobs = %+v %v", jobs, err)
	}

	for i := 0; i < 5; i++ {
		if err := s.MediaAttemptFailed(ctx, "document:1", "boom", 5); err != nil {
			t.Fatal(err)
		}
	}
	info, _ := s.MediaInfo(ctx, "document:1")
	if info.Status != MediaFailed || info.Attempts != 5 || info.Error != "boom" {
		t.Fatalf("info = %+v", info)
	}
	if err := s.MediaDone(ctx, "photo:2", "media/photo/2.jpg"); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := s.PendingMedia(ctx); len(jobs) != 0 {
		t.Fatalf("pending after done/failed = %+v", jobs)
	}

	if err := s.UpdateMessageRaw(ctx, chatA, 1, []byte{9, 9}); err != nil {
		t.Fatal(err)
	}
	raw, err := s.MessageRaw(ctx, chatA, 1)
	if err != nil || len(raw) != 2 {
		t.Fatalf("raw = %v %v", raw, err)
	}
}
```

- [ ] **Step 2: Убедиться, что тест падает**

Run: `go test ./internal/store/ -run TestMediaQueueLifecycle`
Expected: FAIL — `undefined: s.PendingMedia`.

- [ ] **Step 3: Реализация store**

`internal/store/media.go`:

```go
package store

import (
	"context"
	"database/sql"
	"fmt"
)

type MediaJob struct {
	Key       string
	Kind      string
	FileName  string
	Mime      string
	Size      int64
	Attempts  int
	ChatID    int64
	MessageID int
}

type MediaInfo struct {
	Status    string
	LocalPath string
	Error     string
	Attempts  int
}

func (s *Store) PendingMedia(ctx context.Context) ([]MediaJob, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT m.key, m.kind, m.file_name, m.mime, m.size, m.attempts, min(mm.chat_id), min(mm.message_id)
FROM media m JOIN message_media mm ON mm.media_key = m.key
WHERE m.status = 'pending'
GROUP BY m.key
ORDER BY m.rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MediaJob
	for rows.Next() {
		var j MediaJob
		if err := rows.Scan(&j.Key, &j.Kind, &j.FileName, &j.Mime, &j.Size, &j.Attempts, &j.ChatID, &j.MessageID); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) MediaDone(ctx context.Context, key, localPath string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE media SET status = 'done', local_path = ?, error = NULL WHERE key = ?`, localPath, key)
	return err
}

func (s *Store) MediaSkipped(ctx context.Context, key, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE media SET status = 'skipped', error = ? WHERE key = ?`, reason, key)
	return err
}

func (s *Store) MediaAttemptFailed(ctx context.Context, key, errMsg string, maxAttempts int) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE media SET attempts = attempts + 1, error = ?,
  status = CASE WHEN attempts + 1 >= ? THEN 'failed' ELSE 'pending' END
WHERE key = ?`, errMsg, maxAttempts, key)
	return err
}

func (s *Store) MediaInfo(ctx context.Context, key string) (MediaInfo, error) {
	var (
		i         MediaInfo
		path, msg sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `SELECT status, local_path, error, attempts FROM media WHERE key = ?`, key).
		Scan(&i.Status, &path, &msg, &i.Attempts)
	i.LocalPath, i.Error = path.String, msg.String
	return i, err
}

func (s *Store) MessageRaw(ctx context.Context, chatID int64, id int) ([]byte, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT raw FROM messages WHERE chat_id = ? AND id = ?`, chatID, id).Scan(&raw)
	if err != nil {
		return nil, fmt.Errorf("message %d/%d: %w", chatID, id, err)
	}
	return raw, nil
}

func (s *Store) UpdateMessageRaw(ctx context.Context, chatID int64, id int, raw []byte) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET raw = ? WHERE chat_id = ? AND id = ?`, raw, chatID, id)
	return err
}
```

Run: `go test ./internal/store/ -v`
Expected: PASS.

- [ ] **Step 4: Написать падающие тесты загрузчика**

`internal/media/downloader_test.go`:

```go
package media_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"gitlab.com/joeunderwood/tgbup/internal/convert"
	"gitlab.com/joeunderwood/tgbup/internal/media"
	"gitlab.com/joeunderwood/tgbup/internal/store"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

const chatN = int64(1)

var chatID = convert.ChannelID(chatN)

type fakeFiles struct {
	mu        sync.Mutex
	data      map[int64][]byte
	offsets   []int64
	fail      error
	expired   bool // FILE_REFERENCE_EXPIRED until the reference is "fresh"
	onChunk   func(n int)
	refetches int
}

func (f *fakeFiles) FileChunk(ctx context.Context, loc tg.InputFileLocationClass, offset int64, limit int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.fail != nil {
		return nil, f.fail
	}
	d := loc.(*tg.InputDocumentFileLocation)
	if f.expired && string(d.FileReference) != "fresh" {
		return nil, tgerr.New(400, "FILE_REFERENCE_EXPIRED")
	}
	f.offsets = append(f.offsets, offset)
	if f.onChunk != nil {
		f.onChunk(len(f.offsets))
	}
	b := f.data[d.ID]
	if offset >= int64(len(b)) {
		return nil, nil
	}
	end := min(offset+int64(limit), int64(len(b)))
	return b[offset:end], nil
}

func (f *fakeFiles) Message(_ context.Context, _ tg.InputPeerClass, id int) (tg.MessageClass, error) {
	f.refetches++
	return docMessage(id, 555, "fresh", int64(len(f.data[555]))), nil
}

func (f *fakeFiles) HistoryBefore(context.Context, tg.InputPeerClass, int, int) (tgclient.Page, error) {
	return tgclient.Page{}, errors.New("unused")
}

func (f *fakeFiles) HistoryAfter(context.Context, tg.InputPeerClass, int, int) (tgclient.Page, error) {
	return tgclient.Page{}, errors.New("unused")
}

func (f *fakeFiles) PinnedIDs(context.Context, tg.InputPeerClass) ([]int, error) { return nil, nil }

func (f *fakeFiles) ForumTopics(context.Context, tg.InputPeerClass) ([]tgclient.Topic, error) {
	return nil, nil
}

func docMessage(id int, docID int64, ref string, size int64) *tg.Message {
	d := &tg.Document{ID: docID, AccessHash: 1, FileReference: []byte(ref), MimeType: "application/pdf", Size: size,
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "../../отчёт 2026.pdf"}}}
	mm := &tg.MessageMediaDocument{}
	mm.SetDocument(d)
	m := &tg.Message{ID: id, PeerID: &tg.PeerChannel{ChannelID: chatN}, Date: 1}
	m.SetMedia(mm)
	return m
}
```

Продолжение того же файла:

```go
type env struct {
	st   *store.Store
	dir  string
	tg   *fakeFiles
	d    *media.Downloader
	data []byte
}

func newEnv(t *testing.T, size int) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "archive.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.UpsertChat(ctx, store.Chat{ID: chatID, Type: store.ChatSupergroup}); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(rand.IntN(256))
	}
	res, _, err := convert.Message(docMessage(7, 555, "old", int64(size)), convert.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveBatch(ctx, store.Batch{ChatID: chatID, Messages: []store.Message{res.Message}, Media: res.Media}); err != nil {
		t.Fatal(err)
	}
	f := &fakeFiles{data: map[int64][]byte{555: data}}
	return &env{st: st, dir: dir, tg: f, data: data, d: &media.Downloader{
		TG: f, Store: st, ArchiveDir: dir, Workers: 2,
		Peers: map[int64]tg.InputPeerClass{chatID: &tg.InputPeerChannel{ChannelID: chatN}},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
}

func (e *env) final() string { return filepath.Join(e.dir, "media", "document", "555.pdf") }

func (e *env) info(t *testing.T) store.MediaInfo {
	t.Helper()
	i, err := e.st.MediaInfo(context.Background(), "document:555")
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestDownloadsMultiChunkFile(t *testing.T) {
	e := newEnv(t, media.ChunkSize*2+1234)
	r, err := e.d.Run(context.Background())
	if err != nil || r.Done != 1 {
		t.Fatalf("report = %+v %v", r, err)
	}
	got, err := os.ReadFile(e.final())
	if err != nil || !bytes.Equal(got, e.data) {
		t.Fatalf("file mismatch: %v", err)
	}
	if i := e.info(t); i.Status != store.MediaDone || i.LocalPath != filepath.Join("media", "document", "555.pdf") {
		t.Fatalf("info = %+v", i)
	}
}

func TestResumesFromAlignedPartFile(t *testing.T) {
	e := newEnv(t, media.ChunkSize*3)
	if err := os.MkdirAll(filepath.Dir(e.final()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.final()+".part", e.data[:media.ChunkSize+1000], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.tg.offsets[0] != media.ChunkSize {
		t.Fatalf("first offset = %d, want %d", e.tg.offsets[0], media.ChunkSize)
	}
	got, _ := os.ReadFile(e.final())
	if !bytes.Equal(got, e.data) {
		t.Fatal("resumed file differs")
	}
}

func TestSkipsByKindAndSize(t *testing.T) {
	e := newEnv(t, 100)
	e.d.SkipKinds = []string{"document"}
	r, _ := e.d.Run(context.Background())
	if r.Skipped != 1 || e.info(t).Status != store.MediaSkipped {
		t.Fatalf("kind skip: %+v %+v", r, e.info(t))
	}

	e = newEnv(t, 100)
	e.d.MaxFileSize = 50
	r, _ = e.d.Run(context.Background())
	if r.Skipped != 1 || !strings.Contains(e.info(t).Error, "max_file_size") {
		t.Fatalf("size skip: %+v %+v", r, e.info(t))
	}
}

func TestExpiredFileReferenceIsRefreshed(t *testing.T) {
	e := newEnv(t, 1000)
	e.tg.expired = true
	r, err := e.d.Run(context.Background())
	if err != nil || r.Done != 1 || e.tg.refetches != 1 {
		t.Fatalf("report = %+v refetches=%d err=%v", r, e.tg.refetches, err)
	}
}

func TestFailuresBecomeFailedAfterMaxAttempts(t *testing.T) {
	e := newEnv(t, 1000)
	e.tg.fail = errors.New("boom")
	for i := 0; i < media.MaxAttempts; i++ {
		if _, err := e.d.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if i := e.info(t); i.Status != store.MediaFailed || i.Attempts != media.MaxAttempts {
		t.Fatalf("info = %+v", i)
	}
	e.tg.fail = nil
	r, _ := e.d.Run(context.Background())
	if r.Done+r.Failed+r.Skipped+r.Deferred != 0 {
		t.Fatalf("failed media retried: %+v", r)
	}
}

func TestFloodAndMissingPeerAreDeferredWithoutAttempts(t *testing.T) {
	e := newEnv(t, 1000)
	e.tg.fail = tgerr.New(420, "FLOOD_WAIT_900")
	r, _ := e.d.Run(context.Background())
	if r.Deferred != 1 || e.info(t).Attempts != 0 || e.info(t).Status != store.MediaPending {
		t.Fatalf("flood: %+v %+v", r, e.info(t))
	}

	e.tg.fail = nil
	e.d.Peers = map[int64]tg.InputPeerClass{}
	r, _ = e.d.Run(context.Background())
	if r.Deferred != 1 || e.info(t).Status != store.MediaPending {
		t.Fatalf("missing peer: %+v %+v", r, e.info(t))
	}
}

func TestCancelLeavesPartAndPending(t *testing.T) {
	e := newEnv(t, media.ChunkSize*3)
	ctx, cancel := context.WithCancel(context.Background())
	e.tg.onChunk = func(n int) {
		if n == 1 {
			cancel()
		}
	}
	r, _ := e.d.Run(ctx)
	if r.Failed != 0 {
		t.Fatalf("cancel counted as failure: %+v", r)
	}
	if i := e.info(t); i.Status != store.MediaPending || i.Attempts != 0 {
		t.Fatalf("info = %+v", i)
	}
	st, err := os.Stat(e.final() + ".part")
	if err != nil || st.Size() != media.ChunkSize {
		t.Fatalf("part = %v %v", st, err)
	}
}

func TestRelPathIgnoresHostileFileName(t *testing.T) {
	got := media.RelPath(store.MediaJob{Key: "document:555", Kind: "document", FileName: "../../etc/passwd", Mime: "application/pdf"})
	if got != filepath.Join("media", "document", "555.pdf") {
		t.Fatalf("path = %s", got)
	}
	if e := media.Ext("x.tar.GZ", "", "document"); e != ".gz" {
		t.Errorf("ext = %s", e)
	}
	if e := media.Ext("evil.p;h p", "", "voice"); e != ".ogg" {
		t.Errorf("unsafe ext accepted: %s", e)
	}
}
```

- [ ] **Step 5: Убедиться, что тесты падают**

Run: `go test ./internal/media/`
Expected: FAIL — пакет `media` не существует.

- [ ] **Step 6: Реализация**

`internal/media/path.go`:

```go
package media

import (
	"path/filepath"
	"regexp"
	"strings"

	"gitlab.com/joeunderwood/tgbup/internal/store"
)

var safeExt = regexp.MustCompile(`^\.[a-z0-9]{1,10}$`)

var mimeExt = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif",
	"video/mp4": ".mp4", "video/webm": ".webm", "video/quicktime": ".mov",
	"audio/ogg": ".ogg", "audio/mpeg": ".mp3", "audio/mp4": ".m4a",
	"application/pdf": ".pdf", "application/zip": ".zip", "application/x-tgsticker": ".tgs",
}

var kindExt = map[string]string{
	"photo": ".jpg", "voice": ".ogg", "video_note": ".mp4", "video": ".mp4", "animation": ".mp4", "sticker": ".webp",
}

// RelPath builds the on-disk path only from the Telegram id and a whitelisted
// extension, never from the sender-controlled file name.
func RelPath(j store.MediaJob) string {
	_, id, _ := strings.Cut(j.Key, ":")
	return filepath.Join("media", j.Kind, id+Ext(j.FileName, j.Mime, j.Kind))
}

func Ext(fileName, mimeType, kind string) string {
	if e := strings.ToLower(filepath.Ext(fileName)); safeExt.MatchString(e) {
		return e
	}
	if e, ok := mimeExt[mimeType]; ok {
		return e
	}
	if e, ok := kindExt[kind]; ok {
		return e
	}
	return ".bin"
}
```

`internal/media/downloader.go`:

```go
package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	"gitlab.com/joeunderwood/tgbup/internal/convert"
	"gitlab.com/joeunderwood/tgbup/internal/store"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

// upload.getFile needs offset and limit to be multiples of 4 KiB with 1 MiB divisible
// by limit; 512 KiB aligned offsets never cross a 1 MiB boundary.
const ChunkSize = 512 * 1024

const MaxAttempts = 5

type Downloader struct {
	TG          tgclient.Telegram
	Store       *store.Store
	ArchiveDir  string
	Peers       map[int64]tg.InputPeerClass
	Workers     int
	MaxFileSize int64
	SkipKinds   []string
	Log         *slog.Logger
}

type Report struct {
	Done     int
	Skipped  int
	Failed   int
	Deferred int
	Errors   []error
	AuthErr  error
}

type outcome int

const (
	outDone outcome = iota
	outSkipped
	outDeferred
)

func (d *Downloader) Run(ctx context.Context) (Report, error) {
	jobs, err := d.Store.PendingMedia(ctx)
	if err != nil {
		return Report{}, err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var (
		mu sync.Mutex
		r  Report
		wg sync.WaitGroup
	)
	ch := make(chan store.MediaJob)
	for range max(d.Workers, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				out, err := d.handle(ctx, j)
				kind := tgclient.KindOther
				if err != nil {
					kind = tgclient.Classify(err)
				}
				countsAsAttempt := err != nil &&
					(kind == tgclient.KindOther || kind == tgclient.KindFileReference || kind == tgclient.KindInaccessible)
				if countsAsAttempt {
					if ferr := d.Store.MediaAttemptFailed(context.WithoutCancel(ctx), j.Key, err.Error(), MaxAttempts); ferr != nil {
						err = errors.Join(err, ferr)
					}
				}
				mu.Lock()
				switch {
				case err == nil && out == outDone:
					r.Done++
				case err == nil && out == outSkipped:
					r.Skipped++
				case err == nil, kind == tgclient.KindFlood, kind == tgclient.KindCanceled:
					r.Deferred++
				case kind == tgclient.KindAuth:
					r.AuthErr = err
					cancel(err)
				default:
					r.Failed++
					r.Errors = append(r.Errors, fmt.Errorf("media %s: %w", j.Key, err))
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for _, j := range jobs {
		select {
		case ch <- j:
		case <-ctx.Done():
			break feed
		}
	}
	close(ch)
	wg.Wait()
	return r, nil
}

func (d *Downloader) handle(ctx context.Context, j store.MediaJob) (outcome, error) {
	if slices.Contains(d.SkipKinds, j.Kind) {
		return outSkipped, d.Store.MediaSkipped(ctx, j.Key, "kind "+j.Kind+" is skipped by config")
	}
	if d.MaxFileSize > 0 && j.Size > d.MaxFileSize {
		return outSkipped, d.Store.MediaSkipped(ctx, j.Key, fmt.Sprintf("size %d exceeds max_file_size %d", j.Size, d.MaxFileSize))
	}
	peer, ok := d.Peers[j.ChatID]
	if !ok {
		return outDeferred, nil
	}
	rel := RelPath(j)
	final := filepath.Join(d.ArchiveDir, rel)
	if _, err := os.Stat(final); err == nil {
		return outDone, d.Store.MediaDone(ctx, j.Key, rel)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	if err := d.fetch(ctx, j, peer, final); err != nil {
		return 0, err
	}
	return outDone, d.Store.MediaDone(ctx, j.Key, rel)
}

func (d *Downloader) fetch(ctx context.Context, j store.MediaJob, peer tg.InputPeerClass, final string) error {
	raw, err := d.Store.MessageRaw(ctx, j.ChatID, j.MessageID)
	if err != nil {
		return err
	}
	msg, err := tg.DecodeMessage(&bin.Buffer{Buf: raw})
	if err != nil {
		return fmt.Errorf("decode stored message: %w", err)
	}
	loc, err := convert.FileLocation(msg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return err
	}
	part := final + ".part"
	for refreshed := false; ; refreshed = true {
		err := download(ctx, d.TG, loc, part)
		if err == nil {
			break
		}
		if refreshed || tgclient.Classify(err) != tgclient.KindFileReference {
			return err
		}
		fresh, err := d.TG.Message(ctx, peer, j.MessageID)
		if err != nil {
			return fmt.Errorf("refresh file reference: %w", err)
		}
		raw, err := convert.Encode(fresh)
		if err != nil {
			return err
		}
		if err := d.Store.UpdateMessageRaw(ctx, j.ChatID, j.MessageID, raw); err != nil {
			return err
		}
		if loc, err = convert.FileLocation(fresh); err != nil {
			return err
		}
	}
	if j.Size > 0 {
		st, err := os.Stat(part)
		if err != nil {
			return err
		}
		if st.Size() != j.Size {
			os.Remove(part)
			return fmt.Errorf("size mismatch: got %d bytes, want %d", st.Size(), j.Size)
		}
	}
	return os.Rename(part, final)
}

func download(ctx context.Context, t tgclient.Telegram, loc tg.InputFileLocationClass, part string) error {
	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	offset := st.Size() - st.Size()%ChunkSize
	if err := f.Truncate(offset); err != nil {
		return err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	for {
		chunk, err := t.FileChunk(ctx, loc, offset, ChunkSize)
		if err != nil {
			return err
		}
		if _, err := f.Write(chunk); err != nil {
			return err
		}
		offset += int64(len(chunk))
		if len(chunk) < ChunkSize {
			return f.Sync()
		}
	}
}
```

- [ ] **Step 7: Прогнать тесты**

Run: `go test ./internal/media/ ./internal/store/ -v -race`
Expected: PASS всех тестов. `TestCancelLeavesPartAndPending`: отмена после первого чанка → в `.part` ровно `ChunkSize` байт, попытка не засчитана.

- [ ] **Step 8: Commit**

```bash
git add internal/store/media.go internal/store/media_test.go internal/media
git commit -m "feat: download media with resumable chunks, retries and reference refresh"
```

---

### Task 12: `tgbup sync`, блокировка, README и боевой smoke

**Files:**
- Create: `cmd/tgbup/lock.go`, `cmd/tgbup/sync.go`, `README.md`
- Modify: `cmd/tgbup/main.go` (ветка `sync`, строка usage уже есть)
- Test: `cmd/tgbup/lock_test.go`

**Interfaces:**
- Consumes: всё из Tasks 2–11.
- Produces: `func acquireLock(path string) (release func(), err error)`, `var errLocked`, `func cmdSync(ctx, args []string, env Env) int`. Код выхода: 0 — всё ок (FloodWait-отсрочки и «уже идёт другой запуск» — не ошибка), 1 — любая ошибка чата/медиа/авторизации, 2 — неверные флаги.

- [ ] **Step 1: Написать падающий тест блокировки**

`cmd/tgbup/lock_test.go`:

```go
package main

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLockIsExclusiveAndReleasable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "run.lock")
	release, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLock(path); !errors.Is(err, errLocked) {
		t.Fatalf("second lock err = %v, want errLocked", err)
	}
	release()
	release2, err := acquireLock(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	release2()
}
```

- [ ] **Step 2: Убедиться, что тест падает**

Run: `go test ./cmd/tgbup/ -run TestLock`
Expected: FAIL — `undefined: acquireLock`.

- [ ] **Step 3: Реализация**

`cmd/tgbup/lock.go`:

```go
package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

var errLocked = errors.New("another tgbup run is in progress")

func acquireLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
```

`cmd/tgbup/sync.go`:

```go
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"

	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/media"
	"gitlab.com/joeunderwood/tgbup/internal/store"
	"gitlab.com/joeunderwood/tgbup/internal/syncer"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

type syncSummary struct {
	sync  syncer.Report
	media media.Report
}

func (s syncSummary) ok() bool {
	return s.sync.OK() && s.media.AuthErr == nil && s.media.Failed == 0
}

func (s syncSummary) print(w io.Writer) {
	fmt.Fprintf(w, "chats_synced=%d new_messages=%d deferred=%d newly_inaccessible=%v chat_errors=%d\n",
		s.sync.Chats, s.sync.NewMessages, len(s.sync.Deferred), s.sync.NewlyInaccessible, len(s.sync.Failed))
	for id, err := range s.sync.Failed {
		fmt.Fprintf(w, "  chat %d: %v\n", id, err)
	}
	fmt.Fprintf(w, "media_done=%d media_skipped=%d media_deferred=%d media_failed=%d\n",
		s.media.Done, s.media.Skipped, s.media.Deferred, s.media.Failed)
	for _, err := range s.media.Errors {
		fmt.Fprintf(w, "  %v\n", err)
	}
}

func cmdSync(ctx context.Context, args []string, env Env) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	useTakeout := fs.Bool("takeout", false, "use a Telegram takeout session (initial full export)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	log := slog.New(slog.NewTextHandler(env.Stderr, nil))

	st, err := loadSetup(env, true)
	if err != nil {
		log.Error("setup failed", "err", err)
		return 1
	}
	release, err := acquireLock(st.paths.Lock())
	if errors.Is(err, errLocked) {
		log.Info("skipping: another run is in progress")
		return 0
	}
	if err != nil {
		log.Error("lock failed", "err", err)
		return 1
	}
	defer release()

	db, err := store.Open(filepath.Join(st.cfg.ArchiveDir, "archive.sqlite"))
	if err != nil {
		log.Error("open archive", "err", err)
		return 1
	}
	defer db.Close()

	var sum syncSummary
	err = tgclient.Run(ctx, st.clientOptions(), func(ctx context.Context, c *telegram.Client) error {
		if err := tgclient.RequireAuth(ctx, c); err != nil {
			return err
		}
		body := func(ctx context.Context, raw *tg.Client) error {
			var err error
			sum, err = runSync(ctx, raw, db, st.cfg, log)
			return err
		}
		if *useTakeout {
			return tgclient.WithTakeout(ctx, c, int64(st.cfg.Media.MaxFileSize), body)
		}
		return body(ctx, c.API())
	})
	sum.print(env.Stdout)
	if err != nil {
		log.Error("sync failed", "err", err)
		return 1
	}
	if !sum.ok() {
		return 1
	}
	return 0
}

func runSync(ctx context.Context, raw *tg.Client, db *store.Store, cfg config.Config, log *slog.Logger) (syncSummary, error) {
	api := tgclient.NewAPI(raw)
	self, err := tgclient.SelfID(ctx, raw)
	if err != nil {
		return syncSummary{}, err
	}
	dialogs, err := api.Dialogs(ctx)
	if err != nil {
		return syncSummary{}, err
	}
	folders, err := api.Folders(ctx)
	if err != nil {
		return syncSummary{}, err
	}
	targets, warnings, err := tgclient.SelectTargets(dialogs, folders, cfg.Targets)
	if err != nil {
		return syncSummary{}, err
	}
	targets, migrationWarnings := api.AddMigrated(ctx, targets)
	for _, w := range append(warnings, migrationWarnings...) {
		log.Warn(w)
	}
	log.Info("targets selected", "count", len(targets))

	dialogIDs := map[int64]bool{}
	for _, d := range dialogs {
		dialogIDs[d.ID] = true
	}
	peers := map[int64]tg.InputPeerClass{}
	for _, t := range targets {
		peers[t.Chat.ID] = t.Peer
	}

	s := syncer.Syncer{
		TG: api, Store: db, SelfID: self, Budget: cfg.Sync.RunTimeBudget.D(), PageSize: 100, Now: time.Now, Log: log,
	}
	sum := syncSummary{sync: s.Run(ctx, targets, dialogIDs)}
	if sum.sync.AuthErr != nil {
		return sum, sum.sync.AuthErr
	}

	d := media.Downloader{
		TG: api, Store: db, ArchiveDir: cfg.ArchiveDir, Peers: peers, Workers: cfg.Media.Workers,
		MaxFileSize: int64(cfg.Media.MaxFileSize), SkipKinds: cfg.Media.SkipKinds, Log: log,
	}
	sum.media, err = d.Run(ctx)
	if err != nil {
		return sum, err
	}
	return sum, sum.media.AuthErr
}
```

В `cmd/tgbup/main.go` в `switch args[0]` добавить:

```go
	case "sync":
		return cmdSync(ctx, args[1:], env)
```

`README.md`:

````markdown
# tgbup

Автономный бэкап Telegram-чатов, групп и форумов в SQLite + файлы. Дизайн: `docs/superpowers/specs/2026-09-28-tgbup-design.md`.

## Установка

```bash
brew install go   # нужен Go 1.25+
make build        # → bin/tgbup
```

## Настройка

1. Создайте приложение на https://my.telegram.org → API development tools.
2. Секреты:
   ```bash
   mkdir -p ~/.config/tgbup
   printf 'TG_API_ID=12345\nTG_API_HASH=abcdef\n' > ~/.config/tgbup/secrets.env
   chmod 600 ~/.config/tgbup/secrets.env
   ```
3. Конфиг `~/.config/tgbup/config.yaml`:
   ```yaml
   archive_dir: ~/TelegramArchive
   targets:
     folders: ["Семья", "Бизнес"]   # папки Telegram
     include: ["@some_group"]        # плюс отдельные чаты
     exclude: []
   ```
4. `bin/tgbup login`
5. Первая полная выгрузка: `bin/tgbup sync --takeout` (Telegram попросит подтвердить экспорт в клиенте — подтвердите и запустите снова).
6. Дальше: `bin/tgbup sync` (по расписанию — см. план 3).

Пути переопределяются через `TGBUP_CONFIG`, `TGBUP_SECRETS`, `TGBUP_STATE_DIR`.
````

- [ ] **Step 4: Прогнать всё**

Run: `make test && make lint && make build`
Expected: PASS, линтер чистый, `bin/tgbup` собран.

- [ ] **Step 5: Ручной smoke на живом аккаунте** (выполняет человек; исполнитель готовит команды и проверяет результаты)

Подготовка: создать тестовую **форум-группу** «tgbup-smoke» (Settings → Topics), в ней топик «Тест»; туда отправить: текст со скрытой ссылкой, ответ на сообщение, пересылку из канала, альбом из 2 фото, PDF, голосовое, кружок, стикер, опрос, геопозицию; закрепить одно сообщение. Добавить группу в папку Telegram «tgbup-test» и в конфиге указать `folders: ["tgbup-test"]`.

1. **Первая выгрузка:** `bin/tgbup sync --takeout` → подтвердить экспорт в Telegram → повторить.
   Expected: код 0, `chats_synced=1`, `media_failed=0`.
2. **Проверка данных:**
   ```bash
   sqlite3 ~/TelegramArchive/archive.sqlite "select type,title from chats; select topic_id,title from topics; select count(*), sum(service_action_json is not null), sum(pinned) from messages; select kind,status from media;"
   ```
   Expected: тип `forum`; топики General и «Тест»; есть служебные (создание топика, закреп); `sum(pinned)=1`; медиа всех видов со статусом `done`; файлы лежат в `~/TelegramArchive/media/<kind>/`.
3. **Инкремент (Review Focus №1):** отправить в группу 3 новых сообщения, выполнить `bin/tgbup sync`.
   Expected: `new_messages=3`, без takeout-запроса; `select max(id) from messages` вырос ровно на новые ID, пропусков нет (`select id from messages order by id` — последние три идут подряд с учётом ID служебных).
4. **Прерывание:** отправить файл ≥ 50 МБ, запустить `bin/tgbup sync` и нажать Ctrl+C во время загрузки медиа; затем снова `bin/tgbup sync`.
   Expected: после прерывания есть `.part`, статус `pending`, `attempts=0`; второй запуск докачивает, `.part` исчезает.
5. **Блокировка:** в двух терминалах одновременно `bin/tgbup sync`.
   Expected: второй пишет `skipping: another run is in progress` и выходит с кодом 0.

Если любой пункт не сходится — не коммитить, описать расхождение.

- [ ] **Step 6: Commit**

```bash
git add cmd/tgbup README.md
git commit -m "feat: add tgbup sync command with run lock and setup README"
```

---

## Покрытие spec (самопроверка)

| Spec | Задача |
|---|---|
| §3 стек, лицензии | Tasks 1–3, 5, 9 (зависимости из разрешённого списка) |
| §4 раскладка, пути, секреты, chmod 600 | Task 2, 12 |
| §5 команды `login`, `sync`, пакеты `config/tg/store/convert/syncer/media` | Tasks 2–12 (`render`, `backup`, `notify`, `run` — планы 2–3) |
| §6 схема, все сообщения, медиа по ID, ссылки в entities, топики, закрепы, миграция группы, без отслеживания правок | Tasks 3–6, 8, 10 |
| §7 поток: lock, резолв целей, новое → дозагрузка в бюджете, пачка = транзакция, закрепы, медиа с докачкой, `--takeout` | Tasks 4, 7, 8, 10–12 |
| §8 ошибки: FloodWait короткий/длинный, file reference, 5 попыток, недоступный чат, отозванная сессия, изоляция ошибок чатов, код выхода | Tasks 7, 9, 10, 11, 12 (integrity/restic — план 3) |
| §12 тесты: convert-фикстуры, store-атомарность, syncer на фейке, ручной smoke | Tasks 4–6, 10–12 (render golden, restic — планы 2–3) |
| §13 развёртывание шаги 1–4 (без restic/launchd) | Tasks 1, 9, 12 |

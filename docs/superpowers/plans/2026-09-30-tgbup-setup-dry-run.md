# tgbup setup и sync --dry-run: план реализации

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `tgbup sync --dry-run` показывает, что и сколько будет заархивировано, а `tgbup setup` интерактивно выбирает чаты (папки Telegram — навигация) и записывает явный список ID в конфиг.

**Architecture:** Логика выбора — чистый пакет `internal/selector` поверх существующих `tgclient.Dialog/Folder/SelectTargets`. Запись конфига — `config.WriteTargets`, правка узла `targets` через дерево `yaml.v3`. `cmd/tgbup` получает общий вывод таблицы плана, флаг `--dry-run` и команду `setup` с диалогом на нумерованных списках, который тестируется скриптом ввода без Telegram.

**Tech Stack:** Go ≥ 1.25, `github.com/gotd/td` (+ `tgmock`), `go.yaml.in/yaml/v3`, `text/tabwriter`. Новых зависимостей нет.

**Spec:** `docs/superpowers/specs/2026-09-30-tgbup-setup-dry-run-design.md`

## Global Constraints

- Новых зависимостей не добавлять.
- `setup` пишет в `targets` только `include` с числовыми ID; `folders` и `exclude` удаляются; остальные секции и комментарии сохраняются.
- Запись конфига атомарна (временный файл в том же каталоге → rename), права 0600, результат проходит `config.Load` до замены файла.
- `--dry-run`: не берёт `run.lock`, не открывает/не создаёт `archive.sqlite`, ничего не скачивает; коды выхода 0 / 1 (предупреждения или ошибки) / 2 (флаги); `--dry-run` + `--takeout` → 2.
- Счётчик сообщений — один `messages.getHistory(limit=1)` на чат.
- Тексты интерфейса `setup` — на русском, как в spec §4.
- Комментарии в коде — только неочевидное «почему». Conventional commits с attribution-трейлером харнесса; **каждый коммит сразу пушится в `origin main`** (договорённость в проекте).

## Review Focus

1. **Названия чатов с переводами строк, `#`, эмодзи, кавычками** в комментариях YAML — конфиг должен оставаться валидным и читаемым. Тест в Task 3.
2. **Чат в двух папках** — отметка по ID видна в обеих группах, переключение в одной отражается в другой. Тест в Task 2.
3. **Конфиг с папкой, которой больше нет в Telegram** — `setup` стартует с предупреждением, а не падает. Тест в Task 2 (`Initial`).
4. **Мусорный ввод номеров** (`0`, `3-1`, `1-`, `99`, `abc`) — понятная ошибка, выбор не меняется. Тест в Task 2 и Task 5.
5. **Ctrl+D посреди диалога** — выход без записи. Тест в Task 5.

---

## Структура файлов

```
internal/tgclient/api.go            + HistoryCount
internal/tgclient/targets.go        matches → Matches (экспорт для selector)
internal/selector/selector.go       Groups, Selection, ParseIndices, Search, Initial
internal/config/write.go            WriteTargets
cmd/tgbup/plan.go                   planItem/planRow, planRows, printPlan, groupDigits
cmd/tgbup/sync.go                   --dry-run, общий selectChats
cmd/tgbup/setupcmd.go               cmdSetup + setupDialog
cmd/tgbup/main.go                   ветка "setup", usage
e2e/e2e_test.go                     dry-run проверка
README.md                           setup / dry-run
```

---

### Task 1: `HistoryCount`

**Files:**
- Modify: `internal/tgclient/api.go`
- Test: `internal/tgclient/api_test.go`

**Interfaces:**
- Produces: `func (a *API) HistoryCount(ctx context.Context, peer tg.InputPeerClass) (int, error)`

- [ ] **Step 1: Падающий тест** — добавить в `internal/tgclient/api_test.go`:

```go
func TestHistoryCount(t *testing.T) {
	cases := []struct {
		name string
		res  tg.MessagesMessagesClass
		want int
	}{
		{"slice", &tg.MessagesMessagesSlice{Count: 4812, Messages: msgs(9)}, 4812},
		{"channel", &tg.MessagesChannelMessages{Count: 220, Messages: msgs(9)}, 220},
		{"full", &tg.MessagesMessages{Messages: msgs(3, 2, 1)}, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mock := tgmock.NewRequire(t)
			mock.ExpectFunc(func(b bin.Encoder) {
				if req := b.(*tg.MessagesGetHistoryRequest); req.Limit != 1 {
					t.Fatalf("limit = %d, want 1", req.Limit)
				}
			}).ThenResult(c.res)
			got, err := tgclient.NewAPI(tg.NewClient(mock)).HistoryCount(context.Background(), peer)
			if err != nil || got != c.want {
				t.Fatalf("count = %d %v, want %d", got, err, c.want)
			}
		})
	}
}
```

- [ ] **Step 2:** `go test ./internal/tgclient/ -run TestHistoryCount` → FAIL: `HistoryCount undefined`.

- [ ] **Step 3: Реализация** — в `internal/tgclient/api.go` после `HistoryAfter`:

```go
// HistoryCount returns the chat's total message count from a one-message page.
func (a *API) HistoryCount(ctx context.Context, peer tg.InputPeerClass) (int, error) {
	res, err := a.raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, Limit: 1})
	if err != nil {
		return 0, err
	}
	switch r := res.(type) {
	case *tg.MessagesMessagesSlice:
		return r.Count, nil
	case *tg.MessagesChannelMessages:
		return r.Count, nil
	case *tg.MessagesMessages:
		return len(r.Messages), nil
	}
	return 0, fmt.Errorf("unexpected history result %T", res)
}
```

- [ ] **Step 4:** `go test -race ./internal/tgclient/` → PASS; `golangci-lint run ./...` → 0 issues.

- [ ] **Step 5: Commit + push**

```bash
git add internal/tgclient/api.go internal/tgclient/api_test.go
git commit -m "feat: count a chat's messages with one history request"
git push origin main
```

---

### Task 2: пакет `selector`

**Files:**
- Modify: `internal/tgclient/targets.go` (метод `matches` → `Matches`, все вызовы)
- Create: `internal/selector/selector.go`
- Test: `internal/selector/selector_test.go`

**Interfaces:**
- Consumes: `tgclient.Dialog`, `tgclient.Folder`, `tgclient.SelectTargets(dialogs, folders, config.Targets) ([]Target, []string, error)`, `config.Targets`.
- Produces:
  - `func (f tgclient.Folder) Matches(d tgclient.Dialog) bool`
  - `const OutsideFolders = "Вне папок"`, `const ArchiveGroup = "Архив"`
  - `type Group struct{ Title string; Chats []tgclient.Dialog }`, `func Groups(dialogs []tgclient.Dialog, folders []tgclient.Folder) []Group`
  - `type Selection map[int64]bool` с методами `Count([]tgclient.Dialog) int`, `Toggle([]tgclient.Dialog, []int)`, `SetAll([]tgclient.Dialog, bool)`, `IDs() []int64`
  - `func ParseIndices(input string, n int) ([]int, error)` — 0-based, уникальные, по возрастанию
  - `func Search(dialogs []tgclient.Dialog, q string) []tgclient.Dialog`
  - `func Initial(dialogs []tgclient.Dialog, folders []tgclient.Folder, sel config.Targets) (Selection, []string)`

- [ ] **Step 1: Экспорт `Matches`**

```bash
sed -i '' 's/func (f Folder) matches(/func (f Folder) Matches(/; s/f\.matches(d)/f.Matches(d)/' internal/tgclient/targets.go
go test ./internal/tgclient/
```
Expected: PASS (чистое переименование).

- [ ] **Step 2: Падающий тест** — `internal/selector/selector_test.go`:

```go
package selector_test

import (
	"slices"
	"strings"
	"testing"

	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/selector"
	"gitlab.com/joeunderwood/tgbup/internal/store"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

var (
	forum = tgclient.Dialog{ID: -1000000000001, Type: store.ChatForum, Title: "Семейный форум", Username: "FamForum"}
	mama  = tgclient.Dialog{ID: 10, Type: store.ChatPrivate, Title: "Мама", Contact: true}
	dacha = tgclient.Dialog{ID: -20, Type: store.ChatGroup, Title: "Дача"}
	old   = tgclient.Dialog{ID: 11, Type: store.ChatPrivate, Title: "Старый", Archived: true}

	dialogs = []tgclient.Dialog{forum, mama, dacha, old}
	folders = []tgclient.Folder{
		{Title: "Семья", Include: []int64{forum.ID, mama.ID}},
		{Title: "Форумы", Include: []int64{forum.ID}},
	}
)

func ids(ds []tgclient.Dialog) []int64 {
	var out []int64
	for _, d := range ds {
		out = append(out, d.ID)
	}
	return out
}

func TestGroups(t *testing.T) {
	gs := selector.Groups(dialogs, folders)
	var titles []string
	for _, g := range gs {
		titles = append(titles, g.Title)
	}
	if !slices.Equal(titles, []string{"Семья", "Форумы", selector.OutsideFolders, selector.ArchiveGroup}) {
		t.Fatalf("groups = %v", titles)
	}
	if !slices.Equal(ids(gs[0].Chats), []int64{forum.ID, mama.ID}) || !slices.Equal(ids(gs[1].Chats), []int64{forum.ID}) {
		t.Errorf("folder chats = %v / %v", ids(gs[0].Chats), ids(gs[1].Chats))
	}
	if !slices.Equal(ids(gs[2].Chats), []int64{dacha.ID}) || !slices.Equal(ids(gs[3].Chats), []int64{old.ID}) {
		t.Errorf("outside/archive = %v / %v", ids(gs[2].Chats), ids(gs[3].Chats))
	}
}

func TestSelectionIsSharedAcrossFolders(t *testing.T) {
	gs := selector.Groups(dialogs, folders)
	sel := selector.Selection{}
	sel.Toggle(gs[1].Chats, []int{0}) // forum via "Форумы"
	if sel.Count(gs[0].Chats) != 1 {
		t.Fatalf("forum selected in one folder is not seen in the other")
	}
	sel.Toggle(gs[0].Chats, []int{0}) // unselect it via "Семья"
	if sel.Count(gs[1].Chats) != 0 {
		t.Fatalf("unselecting in one folder is not reflected in the other")
	}
	sel.SetAll(gs[0].Chats, true)
	if !slices.Equal(sel.IDs(), []int64{forum.ID, mama.ID}) {
		t.Fatalf("ids = %v", sel.IDs())
	}
	sel.SetAll(gs[0].Chats, false)
	if len(sel.IDs()) != 0 {
		t.Fatalf("ids after clear = %v", sel.IDs())
	}
}

func TestParseIndices(t *testing.T) {
	got, err := selector.ParseIndices(" 1, 3 ,5-7,3 ", 8)
	if err != nil || !slices.Equal(got, []int{0, 2, 4, 5, 6}) {
		t.Fatalf("got %v %v", got, err)
	}
	for _, bad := range []string{"0", "3-1", "1-", "9", "abc", "", " , "} {
		if _, err := selector.ParseIndices(bad, 8); err == nil {
			t.Errorf("ParseIndices(%q) accepted", bad)
		}
	}
}

func TestSearch(t *testing.T) {
	if got := ids(selector.Search(dialogs, "МАМ")); !slices.Equal(got, []int64{mama.ID}) {
		t.Errorf("title search = %v", got)
	}
	if got := ids(selector.Search(dialogs, "famforum")); !slices.Equal(got, []int64{forum.ID}) {
		t.Errorf("username search = %v", got)
	}
}

func TestInitialFromConfig(t *testing.T) {
	sel, warns := selector.Initial(dialogs, folders, config.Targets{
		Folders: []string{"Семья"}, Include: []config.PeerRef{"-20"}, Exclude: []config.PeerRef{"10"},
	})
	if !slices.Equal(sel.IDs(), []int64{forum.ID, dacha.ID}) || len(warns) != 0 {
		t.Fatalf("initial = %v %v", sel.IDs(), warns)
	}

	sel, warns = selector.Initial(dialogs, folders, config.Targets{Folders: []string{"Удалённая"}})
	if len(sel) != 0 || len(warns) != 1 || !strings.Contains(warns[0], "Удалённая") {
		t.Fatalf("missing folder: %v %v", sel.IDs(), warns)
	}
}
```

- [ ] **Step 3:** `go test ./internal/selector/` → FAIL: пакета нет.

- [ ] **Step 4: Реализация** — `internal/selector/selector.go`:

```go
// Package selector holds the chat selection logic behind `tgbup setup`:
// grouping dialogs by Telegram folder, parsing number input and search.
package selector

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

const (
	OutsideFolders = "Вне папок"
	ArchiveGroup   = "Архив"
)

type Group struct {
	Title string
	Chats []tgclient.Dialog
}

// Groups lists every Telegram folder, then non-archived chats in no folder,
// then archived chats. A chat in several folders appears in each of them.
func Groups(dialogs []tgclient.Dialog, folders []tgclient.Folder) []Group {
	var out []Group
	inFolder := map[int64]bool{}
	for _, f := range folders {
		g := Group{Title: f.Title}
		for _, d := range dialogs {
			if f.Matches(d) {
				g.Chats = append(g.Chats, d)
				inFolder[d.ID] = true
			}
		}
		out = append(out, g)
	}
	var outside, archived []tgclient.Dialog
	for _, d := range dialogs {
		switch {
		case d.Archived:
			archived = append(archived, d)
		case !inFolder[d.ID]:
			outside = append(outside, d)
		}
	}
	return append(out, Group{Title: OutsideFolders, Chats: outside}, Group{Title: ArchiveGroup, Chats: archived})
}

// Selection is keyed by chat id, so a chat looks the same in every group it is in.
type Selection map[int64]bool

func (s Selection) Count(chats []tgclient.Dialog) int {
	n := 0
	for _, c := range chats {
		if s[c.ID] {
			n++
		}
	}
	return n
}

func (s Selection) Toggle(chats []tgclient.Dialog, idx []int) {
	for _, i := range idx {
		id := chats[i].ID
		if s[id] {
			delete(s, id)
		} else {
			s[id] = true
		}
	}
}

func (s Selection) SetAll(chats []tgclient.Dialog, on bool) {
	for _, c := range chats {
		if on {
			s[c.ID] = true
		} else {
			delete(s, c.ID)
		}
	}
}

func (s Selection) IDs() []int64 {
	out := make([]int64, 0, len(s))
	for id := range s {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// ParseIndices turns "1,3,5-7" (1-based, as shown on screen) into sorted unique 0-based indices.
func ParseIndices(input string, n int) ([]int, error) {
	seen := map[int]bool{}
	for _, part := range strings.Split(input, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi := part, part
		if a, b, ok := strings.Cut(part, "-"); ok {
			lo, hi = strings.TrimSpace(a), strings.TrimSpace(b)
		}
		from, err1 := strconv.Atoi(lo)
		to, err2 := strconv.Atoi(hi)
		if err1 != nil || err2 != nil || from < 1 || to > n || from > to {
			return nil, fmt.Errorf("неверный номер или диапазон %q (доступно 1–%d)", part, n)
		}
		for i := from; i <= to; i++ {
			seen[i-1] = true
		}
	}
	if len(seen) == 0 {
		return nil, errors.New("не указано ни одного номера")
	}
	out := make([]int, 0, len(seen))
	for i := range seen {
		out = append(out, i)
	}
	slices.Sort(out)
	return out, nil
}

func Search(dialogs []tgclient.Dialog, q string) []tgclient.Dialog {
	q = strings.ToLower(strings.TrimSpace(q))
	var out []tgclient.Dialog
	for _, d := range dialogs {
		if strings.Contains(strings.ToLower(d.Title), q) || (d.Username != "" && strings.Contains(strings.ToLower(d.Username), q)) {
			out = append(out, d)
		}
	}
	return out
}

// Initial marks what the current config selects, so setup starts from it. A
// config that no longer resolves (e.g. a deleted folder) yields an empty
// selection plus a warning instead of an error.
func Initial(dialogs []tgclient.Dialog, folders []tgclient.Folder, sel config.Targets) (Selection, []string) {
	targets, warnings, err := tgclient.SelectTargets(dialogs, folders, sel)
	if err != nil {
		return Selection{}, []string{err.Error()}
	}
	s := Selection{}
	for _, t := range targets {
		s[t.Chat.ID] = true
	}
	return s, warnings
}
```

- [ ] **Step 5:** `go test -race ./internal/selector/ ./internal/tgclient/` → PASS; `golangci-lint run ./...` → 0 issues.

- [ ] **Step 6: Commit + push**

```bash
git add internal/tgclient/targets.go internal/selector
git commit -m "feat: add chat selection logic for tgbup setup"
git push origin main
```

---

### Task 3: `config.WriteTargets`

**Files:**
- Create: `internal/config/write.go`
- Test: `internal/config/write_test.go`

**Interfaces:**
- Consumes: `config.Load(path, home string) (Config, error)`.
- Produces: `func WriteTargets(path string, ids []int64, titles map[int64]string) error`

- [ ] **Step 1: Падающий тест** — `internal/config/write_test.go`:

```go
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
```

- [ ] **Step 2:** `go test ./internal/config/ -run WriteTargets` → FAIL: `WriteTargets undefined`.

- [ ] **Step 3: Реализация** — `internal/config/write.go`:

```go
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// WriteTargets replaces the targets section with an explicit include list of
// chat ids (titles become comments) and leaves every other section and its
// comments as they are.
func WriteTargets(path string, ids []int64, titles map[int64]string) error {
	if len(ids) == 0 {
		return errors.New("no chats selected")
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		data = []byte("archive_dir: ~/TelegramArchive\n")
	case err != nil:
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: top level is not a mapping", path)
	}
	targets := targetsNode(ids, titles)
	replaced := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "targets" {
			root.Content[i+1] = targets
			replaced = true
		}
	}
	if !replaced {
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "targets"}, targets)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return replaceFile(path, buf.Bytes())
}

func targetsNode(ids []int64, titles map[int64]string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, id := range ids {
		n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(id, 10)}
		// Fields collapses newlines so a title can never break out of its comment.
		if title := strings.Join(strings.Fields(titles[id]), " "); title != "" {
			n.LineComment = "# " + title
		}
		seq.Content = append(seq.Content, n)
	}
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "include"}, seq,
	}}
}

// replaceFile validates the new config from a temp file next to path and only
// then renames it over path, so a bad or half-written config never replaces a good one.
func replaceFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if _, err := Load(tmp.Name(), "/"); err != nil {
		return fmt.Errorf("refusing to write an invalid config: %w", err)
	}
	return os.Rename(tmp.Name(), path)
}
```

- [ ] **Step 4:** `go test -race ./internal/config/` → PASS; `golangci-lint run ./...` → 0 issues.

- [ ] **Step 5: Commit + push**

```bash
git add internal/config/write.go internal/config/write_test.go
git commit -m "feat: write an explicit chat list into the config, keeping other settings"
git push origin main
```

---

### Task 4: `tgbup sync --dry-run`

**Files:**
- Create: `cmd/tgbup/plan.go`
- Modify: `cmd/tgbup/sync.go`
- Test: `cmd/tgbup/plan_test.go`, `cmd/tgbup/sync_test.go`

**Interfaces:**
- Consumes: `(*tgclient.API).HistoryCount` (Task 1).
- Produces:
  - `type planItem struct{ ID int64; Type, Title, Source string; Peer tg.InputPeerClass }`
  - `type planRow struct{ ID int64; Type, Title, Source string; Count int }`
  - `type historyCounter interface{ HistoryCount(ctx context.Context, peer tg.InputPeerClass) (int, error) }`
  - `func planRows(ctx context.Context, c historyCounter, items []planItem) ([]planRow, error)`
  - `func printPlan(w io.Writer, rows []planRow)`, `func groupDigits(n int) string`
  - `type chatSelection struct{ self int64; dialogs []tgclient.Dialog; targets []tgclient.Target; warnings []string }`
  - `func selectChats(ctx context.Context, api *tgclient.API, raw *tg.Client, cfg config.Config, log *slog.Logger) (chatSelection, error)`

- [ ] **Step 1: Падающие тесты** — `cmd/tgbup/plan_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

type fakeCounter map[int64]int

func (f fakeCounter) HistoryCount(_ context.Context, peer tg.InputPeerClass) (int, error) {
	p, ok := peer.(*tg.InputPeerUser)
	if !ok {
		return 0, errors.New("unexpected peer")
	}
	return f[p.UserID], nil
}

func TestPlanRowsAndPrint(t *testing.T) {
	items := []planItem{
		{ID: 1, Type: "private", Title: "Мама", Source: "folder Семья", Peer: &tg.InputPeerUser{UserID: 1}},
		{ID: 2, Type: "group", Title: "Дача", Source: "include", Peer: &tg.InputPeerUser{UserID: 2}},
	}
	rows, err := planRows(context.Background(), fakeCounter{1: 15000, 2: 12}, items)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printPlan(&out, rows)
	for _, want := range []string{"would archive 2 chats, ~15 012 messages:", "Мама", "folder Семья", "~15 000", "include", "~12"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plan output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestGroupDigits(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1 000", 1234567: "1 234 567"} {
		if got := groupDigits(n); got != want {
			t.Errorf("groupDigits(%d) = %q, want %q", n, got, want)
		}
	}
}
```

И в `cmd/tgbup/sync_test.go` добавить:

```go
func TestDryRunWithTakeoutIsAFlagError(t *testing.T) {
	env, _, errOut := testEnv()
	if code := run(context.Background(), []string{"sync", "--dry-run", "--takeout"}, env); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "--dry-run") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}
```
(добавить `"context"` в импорты `sync_test.go`, если его нет).

- [ ] **Step 2:** `go test ./cmd/tgbup/` → FAIL: `planItem undefined`, `run` возвращает не 2.

- [ ] **Step 3: Реализация `plan.go`** — `cmd/tgbup/plan.go`:

```go
package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"

	"github.com/gotd/td/tg"
)

type planItem struct {
	ID     int64
	Type   string
	Title  string
	Source string
	Peer   tg.InputPeerClass
}

type planRow struct {
	ID     int64
	Type   string
	Title  string
	Source string
	Count  int
}

type historyCounter interface {
	HistoryCount(ctx context.Context, peer tg.InputPeerClass) (int, error)
}

func planRows(ctx context.Context, c historyCounter, items []planItem) ([]planRow, error) {
	rows := make([]planRow, 0, len(items))
	for _, it := range items {
		n, err := c.HistoryCount(ctx, it.Peer)
		if err != nil {
			return nil, fmt.Errorf("count messages in %d %q: %w", it.ID, it.Title, err)
		}
		rows = append(rows, planRow{ID: it.ID, Type: it.Type, Title: it.Title, Source: it.Source, Count: n})
	}
	return rows, nil
}

func printPlan(w io.Writer, rows []planRow) {
	total := 0
	for _, r := range rows {
		total += r.Count
	}
	fmt.Fprintf(w, "would archive %d chats, ~%s messages:\n", len(rows), groupDigits(total))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t~%s\n", r.ID, r.Type, r.Title, r.Source, groupDigits(r.Count))
	}
	tw.Flush()
}

func groupDigits(n int) string {
	s := strconv.Itoa(n)
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ' ')
		}
		out = append(out, s[i])
	}
	return string(out)
}
```

- [ ] **Step 4: Реализация в `sync.go`** — вынести выбор чатов из `runSync` и добавить dry-run.

Заменить начало `runSync` (от `api := tgclient.NewAPI(raw)` до `log.Info("targets selected", ...)` включительно) и объявить общий выбор:

```go
type chatSelection struct {
	self     int64
	dialogs  []tgclient.Dialog
	targets  []tgclient.Target
	warnings []string
}

func selectChats(ctx context.Context, api *tgclient.API, raw *tg.Client, cfg config.Config, log *slog.Logger) (chatSelection, error) {
	self, err := tgclient.SelfID(ctx, raw)
	if err != nil {
		return chatSelection{}, err
	}
	dialogs, err := api.Dialogs(ctx)
	if err != nil {
		return chatSelection{}, err
	}
	folders, err := api.Folders(ctx, self)
	if err != nil {
		return chatSelection{}, err
	}
	targets, warnings, err := tgclient.SelectTargets(dialogs, folders, cfg.Targets)
	if err != nil {
		return chatSelection{}, err
	}
	targets, migrationWarnings := api.AddMigrated(ctx, targets)
	for _, w := range migrationWarnings {
		log.Warn(w)
	}
	log.Info("targets selected", "count", len(targets))
	return chatSelection{self: self, dialogs: dialogs, targets: targets, warnings: warnings}, nil
}

func runSync(ctx context.Context, raw *tg.Client, db *store.Store, cfg config.Config, log *slog.Logger) (syncSummary, error) {
	api := tgclient.NewAPI(raw)
	sel, err := selectChats(ctx, api, raw, cfg, log)
	if err != nil {
		return syncSummary{}, err
	}
	self, dialogs, targets, warnings := sel.self, sel.dialogs, sel.targets, sel.warnings
```
(остальная часть `runSync` — с `dialogIDs := map[int64]bool{}` — без изменений.)

Добавить dry-run:

```go
func targetItems(targets []tgclient.Target) []planItem {
	items := make([]planItem, 0, len(targets))
	for _, t := range targets {
		source := "include"
		if len(t.Chat.Folders) > 0 {
			source = "folder " + strings.Join(t.Chat.Folders, ", ")
		}
		items = append(items, planItem{ID: t.Chat.ID, Type: t.Chat.Type, Title: t.Chat.Title, Source: source, Peer: t.Peer})
	}
	return items
}

// dryRun resolves targets like sync but touches neither run.lock nor the archive.
func dryRun(ctx context.Context, st setup, env Env, log *slog.Logger) int {
	var warnings []string
	err := tgclient.Run(ctx, st.clientOptions(), func(ctx context.Context, c *telegram.Client) error {
		if err := tgclient.RequireAuth(ctx, c); err != nil {
			return err
		}
		api := tgclient.NewAPI(c.API())
		sel, err := selectChats(ctx, api, c.API(), st.cfg, log)
		if err != nil {
			return err
		}
		rows, err := planRows(ctx, api, targetItems(sel.targets))
		if err != nil {
			return err
		}
		printPlan(env.Stdout, rows)
		warnings = sel.warnings
		return nil
	})
	for _, w := range warnings {
		fmt.Fprintf(env.Stdout, "warning: %s\n", w)
	}
	if err != nil {
		log.Error("dry run failed", "err", err)
		return 1
	}
	if len(warnings) > 0 {
		return 1
	}
	return 0
}
```

В `cmdSync` после объявления `useTakeout`:

```go
	dry := fs.Bool("dry-run", false, "show which chats would be archived and how many messages they hold; download nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dry && *useTakeout {
		fmt.Fprintln(env.Stderr, "--dry-run and --takeout cannot be combined")
		return 2
	}
	log := slog.New(slog.NewTextHandler(env.Stderr, nil))

	st, err := loadSetup(env, true)
	if err != nil {
		log.Error("setup failed", "err", err)
		return 1
	}
	if *dry {
		return dryRun(ctx, st, env, log)
	}
```
(убрать прежний дубль `fs.Parse`/`log`/`loadSetup`; добавить `"strings"` в импорты.)

- [ ] **Step 5:** `go test -race ./...` → PASS; `golangci-lint run ./...` → 0 issues; `make build`.

- [ ] **Step 6: Commit + push**

```bash
git add cmd/tgbup/plan.go cmd/tgbup/plan_test.go cmd/tgbup/sync.go cmd/tgbup/sync_test.go
git commit -m "feat: add sync --dry-run listing chats and message counts"
git push origin main
```

---

### Task 5: `tgbup setup`

**Files:**
- Create: `cmd/tgbup/setupcmd.go`
- Modify: `cmd/tgbup/main.go` (ветка `setup`, usage)
- Test: `cmd/tgbup/setupcmd_test.go`

**Interfaces:**
- Consumes: `selector.*` (Task 2), `config.WriteTargets` (Task 3), `planItem`, `planRow`, `planRows`, `printPlan` (Task 4).
- Produces:
  - `type setupDialog struct{ in *bufio.Scanner; out io.Writer; groups []selector.Group; dialogs []tgclient.Dialog; sel selector.Selection; cfgPath string; plan func([]tgclient.Dialog) ([]planRow, error) }`
  - `func (s *setupDialog) run() bool` — `true` = сохранить `s.sel`
  - `func dialogItems(chosen []tgclient.Dialog, groups []selector.Group) []planItem`
  - `func cmdSetup(ctx context.Context, env Env) int`

- [ ] **Step 1: Падающий тест** — `cmd/tgbup/setupcmd_test.go`:

```go
package main

import (
	"bufio"
	"bytes"
	"slices"
	"strings"
	"testing"

	"gitlab.com/joeunderwood/tgbup/internal/selector"
	"gitlab.com/joeunderwood/tgbup/internal/store"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

var (
	sForum = tgclient.Dialog{ID: -1000000000001, Type: store.ChatForum, Title: "Семейный форум"}
	sMama  = tgclient.Dialog{ID: 10, Type: store.ChatPrivate, Title: "Мама"}
	sDacha = tgclient.Dialog{ID: -20, Type: store.ChatGroup, Title: "Дача"}
	sOld   = tgclient.Dialog{ID: 11, Type: store.ChatPrivate, Title: "Старый", Archived: true}
)

func newDialog(input string, initial selector.Selection) (*setupDialog, *bytes.Buffer) {
	dialogs := []tgclient.Dialog{sForum, sMama, sDacha, sOld}
	folders := []tgclient.Folder{{Title: "Семья", Include: []int64{sForum.ID, sMama.ID}}}
	var out bytes.Buffer
	groups := selector.Groups(dialogs, folders)
	return &setupDialog{
		in: bufio.NewScanner(strings.NewReader(input)), out: &out,
		groups: groups, dialogs: dialogs, sel: initial, cfgPath: "/c.yaml",
		plan: func(chosen []tgclient.Dialog) ([]planRow, error) {
			var rows []planRow
			for _, it := range dialogItems(chosen, groups) {
				rows = append(rows, planRow{ID: it.ID, Type: it.Type, Title: it.Title, Source: it.Source, Count: 5})
			}
			return rows, nil
		},
	}, &out
}

func TestSetupSelectsAcrossGroupsAndSaves(t *testing.T) {
	// Семья → 1 (форум) → назад; Вне папок → 1 (Дача) → назад; готово; да.
	d, out := newDialog("1\n1\n\n2\n1\n\nd\ny\n", selector.Selection{})
	if !d.run() {
		t.Fatalf("not saved:\n%s", out)
	}
	if got := d.sel.IDs(); !slices.Equal(got, []int64{sForum.ID, sDacha.ID}) {
		t.Fatalf("ids = %v", got)
	}
	for _, want := range []string{"Семья (2, выбрано 0)", "[x] Семейный форум", "would archive 2 chats", "Записать в /c.yaml (2 чатов)?"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestSetupStartsFromCurrentSelection(t *testing.T) {
	d, out := newDialog("q\n", selector.Selection{sMama.ID: true})
	if d.run() {
		t.Fatal("q must not save")
	}
	if !strings.Contains(out.String(), "Выбрано: 1 чатов") || !strings.Contains(out.String(), "Семья (2, выбрано 1)") {
		t.Fatalf("initial selection not shown:\n%s", out)
	}
}

func TestSetupDeclineAndSearch(t *testing.T) {
	// поиск «мам» → отметить → назад; готово; нет; выйти.
	d, out := newDialog("s\nмам\n1\n\nd\nN\nq\n", selector.Selection{})
	if d.run() {
		t.Fatal("declined confirmation saved")
	}
	if !slices.Equal(d.sel.IDs(), []int64{sMama.ID}) || !strings.Contains(out.String(), "Поиск: мам") {
		t.Fatalf("search selection = %v\n%s", d.sel.IDs(), out)
	}
}

func TestSetupRejectsBadInput(t *testing.T) {
	d, out := newDialog("1\n9\n0\n\nx\nd\nq\n", selector.Selection{})
	if d.run() {
		t.Fatal("saved")
	}
	if len(d.sel) != 0 {
		t.Fatalf("bad input changed selection: %v", d.sel.IDs())
	}
	for _, want := range []string{"ошибка:", "неизвестная команда \"x\"", "ничего не выбрано"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestSetupEOFExitsWithoutSaving(t *testing.T) {
	for _, input := range []string{"", "1\n1", "d\n", "1\n1\n\nd\n"} {
		d, _ := newDialog(input, selector.Selection{})
		if d.run() {
			t.Errorf("input %q: EOF saved the config", input)
		}
	}
}
```

- [ ] **Step 2:** `go test ./cmd/tgbup/ -run TestSetup` → FAIL: `setupDialog undefined`.

- [ ] **Step 3: Реализация** — `cmd/tgbup/setupcmd.go`:

```go
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gotd/td/telegram"

	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/selector"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

type setupDialog struct {
	in      *bufio.Scanner
	out     io.Writer
	groups  []selector.Group
	dialogs []tgclient.Dialog
	sel     selector.Selection
	cfgPath string
	plan    func([]tgclient.Dialog) ([]planRow, error)
}

// ask returns false on EOF (Ctrl+D), which always means "leave without saving".
func (s *setupDialog) ask(prompt string) (string, bool) {
	fmt.Fprint(s.out, prompt)
	if !s.in.Scan() {
		fmt.Fprintln(s.out)
		return "", false
	}
	return strings.TrimSpace(s.in.Text()), true
}

func (s *setupDialog) run() bool {
	for {
		fmt.Fprintf(s.out, "\nВыбрано: %d чатов\n", len(s.sel))
		for i, g := range s.groups {
			fmt.Fprintf(s.out, "  %d) %s (%d, выбрано %d)\n", i+1, g.Title, len(g.Chats), s.sel.Count(g.Chats))
		}
		fmt.Fprintln(s.out, "  s) поиск по названию    d) готово    q) выйти без сохранения")
		line, ok := s.ask("> ")
		if !ok || line == "q" {
			return false
		}
		switch line {
		case "s":
			q, ok := s.ask("поиск: ")
			if !ok || !s.editGroup("Поиск: "+q, selector.Search(s.dialogs, q)) {
				return false
			}
		case "d":
			save, ok := s.confirm()
			if !ok {
				return false
			}
			if save {
				return true
			}
		default:
			n, err := strconv.Atoi(line)
			if err != nil || n < 1 || n > len(s.groups) {
				fmt.Fprintf(s.out, "неизвестная команда %q\n", line)
				continue
			}
			if !s.editGroup(s.groups[n-1].Title, s.groups[n-1].Chats) {
				return false
			}
		}
	}
}

// editGroup returns false on EOF.
func (s *setupDialog) editGroup(title string, chats []tgclient.Dialog) bool {
	for {
		fmt.Fprintf(s.out, "\n%s:\n", title)
		if len(chats) == 0 {
			fmt.Fprintln(s.out, "  (пусто)")
		}
		for i, c := range chats {
			mark := " "
			if s.sel[c.ID] {
				mark = "x"
			}
			fmt.Fprintf(s.out, "  %d) [%s] %s  %s\n", i+1, mark, c.Title, c.Type)
		}
		fmt.Fprintln(s.out, "номера (1,3,5-7) — переключить, a — все, n — ни одного, Enter — назад")
		line, ok := s.ask("> ")
		if !ok {
			return false
		}
		switch line {
		case "":
			return true
		case "a":
			s.sel.SetAll(chats, true)
		case "n":
			s.sel.SetAll(chats, false)
		default:
			idx, err := selector.ParseIndices(line, len(chats))
			if err != nil {
				fmt.Fprintf(s.out, "ошибка: %v\n", err)
				continue
			}
			s.sel.Toggle(chats, idx)
		}
	}
}

// confirm shows the dry-run table for the selection; ok is false on EOF.
func (s *setupDialog) confirm() (save, ok bool) {
	var chosen []tgclient.Dialog
	for _, d := range s.dialogs {
		if s.sel[d.ID] {
			chosen = append(chosen, d)
		}
	}
	if len(chosen) == 0 {
		fmt.Fprintln(s.out, "ничего не выбрано")
		return false, true
	}
	rows, err := s.plan(chosen)
	if err != nil {
		fmt.Fprintf(s.out, "не удалось посчитать сообщения: %v\n", err)
	} else {
		printPlan(s.out, rows)
	}
	ans, ok := s.ask(fmt.Sprintf("Записать в %s (%d чатов)? [y/N]: ", s.cfgPath, len(chosen)))
	if !ok {
		return false, false
	}
	return strings.EqualFold(ans, "y"), true
}

func dialogItems(chosen []tgclient.Dialog, groups []selector.Group) []planItem {
	items := make([]planItem, 0, len(chosen))
	for _, d := range chosen {
		var in []string
		for _, g := range groups {
			for _, c := range g.Chats {
				if c.ID == d.ID {
					in = append(in, g.Title)
					break
				}
			}
		}
		items = append(items, planItem{ID: d.ID, Type: d.Type, Title: d.Title, Source: strings.Join(in, ", "), Peer: d.Peer})
	}
	return items
}

func cmdSetup(ctx context.Context, env Env) int {
	st, err := loadSetup(env, false)
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup setup:", err)
		return 1
	}
	err = tgclient.Run(ctx, st.clientOptions(), func(ctx context.Context, c *telegram.Client) error {
		if err := tgclient.RequireAuth(ctx, c); err != nil {
			return err
		}
		raw := c.API()
		api := tgclient.NewAPI(raw)
		self, err := tgclient.SelfID(ctx, raw)
		if err != nil {
			return err
		}
		dialogs, err := api.Dialogs(ctx)
		if err != nil {
			return err
		}
		folders, err := api.Folders(ctx, self)
		if err != nil {
			return err
		}
		sel, warnings := selector.Initial(dialogs, folders, st.cfg.Targets)
		for _, w := range warnings {
			fmt.Fprintf(env.Stdout, "текущий конфиг: %s\n", w)
		}
		groups := selector.Groups(dialogs, folders)
		d := &setupDialog{
			in: bufio.NewScanner(env.Stdin), out: env.Stdout,
			groups: groups, dialogs: dialogs, sel: sel, cfgPath: st.paths.Config,
			plan: func(chosen []tgclient.Dialog) ([]planRow, error) {
				return planRows(ctx, api, dialogItems(chosen, groups))
			},
		}
		if !d.run() {
			fmt.Fprintln(env.Stdout, "конфиг не изменён")
			return nil
		}
		titles := map[int64]string{}
		for _, dg := range dialogs {
			titles[dg.ID] = dg.Title
		}
		if err := config.WriteTargets(st.paths.Config, d.sel.IDs(), titles); err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "записано %d чатов в %s\n", len(d.sel), st.paths.Config)
		return nil
	})
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup setup:", err)
		return 1
	}
	return 0
}
```

В `cmd/tgbup/main.go`: в `usage` добавить строку `  setup     choose which chats to archive (writes the config)` после `login`, в `switch` — ветку:

```go
	case "setup":
		return cmdSetup(ctx, env)
```

- [ ] **Step 4:** `go test -race ./...` → PASS; `golangci-lint run ./...` → 0 issues; `make build`.

- [ ] **Step 5: Commit + push**

```bash
git add cmd/tgbup/setupcmd.go cmd/tgbup/setupcmd_test.go cmd/tgbup/main.go
git commit -m "feat: add interactive tgbup setup to choose chats by folder"
git push origin main
```

---

### Task 6: e2e для dry-run, README

**Files:**
- Modify: `e2e/e2e_test.go`, `README.md`

**Interfaces:**
- Consumes: fixture (`fx.ChatID`, `fx.SelfID`, `fx.GroupChatID()`), `newArchive`, `testServerEnv`.

- [ ] **Step 1: Тест** — в `e2e/e2e_test.go` `runSync` начинает возвращать вывод (`return string(out)` в конце, сигнатура `func runSync(t *testing.T, cfgPath string, args ...string) string`), и добавляется:

```go
func TestDryRunListsChatsWithoutArchiving(t *testing.T) {
	cfgPath, archive := newArchive(t)
	out := runSync(t, cfgPath, "--dry-run")
	if !strings.Contains(out, "would archive 3 chats") {
		t.Errorf("dry run output:\n%s", out)
	}
	for _, id := range []int64{fx.ChatID, fx.SelfID, fx.GroupChatID()} {
		if !strings.Contains(out, fmt.Sprint(id)) {
			t.Errorf("chat %d missing from dry run", id)
		}
	}
	if _, err := os.Stat(filepath.Join(archive, "archive.sqlite")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("dry run touched the archive: %v", err)
	}
}
```
(импорты `errors`, `io/fs`.)

- [ ] **Step 2:** `make e2e` (seed → run → clean) → все тесты PASS, в выводе dry-run три чата.

- [ ] **Step 3: README** — в «Настройка» заменить шаг 3 на:

````markdown
3. Выбор чатов: `bin/tgbup setup` — папки Telegram показываются для навигации,
   отмечаете конкретные чаты, в конце видно число сообщений и подтверждение.
   Выбор пишется в `~/.config/tgbup/config.yaml` как список числовых ID.
   Проверить, что будет выгружено, без скачивания: `bin/tgbup sync --dry-run`.
   Конфиг по-прежнему можно править руками (`folders`, `include`, `exclude`).
````
(шаги 4–6 перенумеровать так, чтобы `login` шёл до `setup`.)

- [ ] **Step 4:** `golangci-lint run --build-tags e2e ./e2e/...` → 0 issues.

- [ ] **Step 5: Commit + push**

```bash
git add e2e/e2e_test.go README.md
git commit -m "test: cover sync --dry-run in e2e; document setup"
git push origin main
```

---

## Покрытие spec

| Spec | Задача |
|---|---|
| §3 dry-run: без lock/архива, счётчик одним запросом, таблица, коды выхода, конфликт с `--takeout` | Tasks 1, 4, 6 |
| §4 setup: главный экран, группы «Вне папок»/«Архив», экран папки, `1,3,5-7`/`a`/`n`, поиск, начальное состояние, `d` с таблицей и `[y/N]`, `q`/EOF | Tasks 2, 5 |
| §5 запись: только `targets.include`, удаление `folders`/`exclude`, комментарии-названия, сохранение прочего, новый файл, валидация, атомарность, 0600 | Task 3 |
| §6 компоненты | Tasks 1–5 |
| §7 тесты | Tasks 1–6 |

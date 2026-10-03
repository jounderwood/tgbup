# tgbup: исключение топиков, подробный dry-run, постраничный setup — план

> **Выполнен.** Актуальное устройство — `docs/PROJECT.md`, решения — в спеках (раздел «Расхождения с реализацией»). План не поддерживается.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** реализовать spec `docs/superpowers/specs/2026-10-01-tgbup-topics-stats-design.md`.

**Architecture:** `config` получает `exclude_topics`; `tgclient.SelectTargets` переносит
исключения в `Target.ExcludedTopics`; `syncer` отбрасывает сообщения исключённых топиков
и через `store.ApplyTopicExclusions` сбрасывает backfill при возврате топика. Новый пакет
`internal/stats` собирает статистику чата (всего, по типам, по топикам) поверх новых
методов `tgclient`; `cmd/tgbup/report.go` печатает блоки. `selector.State` хранит выбор чатов
и исключения топиков; диалог `setup` получает страницы, ленивые счётчики и экран топиков.

**Tech Stack:** Go ≥ 1.25, gotd/td v0.162.0 (`messages.getSearchCounters`,
`messages.getForumTopics`, `messages.getReplies`), `go.yaml.in/yaml/v3`, `golang.org/x/term`.

**Spec:** `docs/superpowers/specs/2026-10-01-tgbup-topics-stats-design.md`

**Формат плана.** Пользователь поручил выполнение без подтверждений («дальше давай сам»),
поэтому план компактный: для каждой задачи — файлы, интерфейсы и перечень тестов, которые
пишутся первыми (TDD). Код пишется при выполнении.

## Global Constraints

- Новых зависимостей нет.
- Весь пользовательский вывод `setup`/`dry-run`/`login` — на английском.
- Бинарник и тесты — только против тестовых серверов (`TGBUP_TEST_DC=1`, `make e2e-*`); боевую сессию не трогать.
- Каждый коммит — conventional, с attribution-трейлером, сразу `git push origin main`.
- Комментарии только для неочевидного «почему».

## Review Focus

1. **Возврат топика после исключения** — пропущенные сообщения дописываются, уже сохранённые не дублируются, повторный запуск без изменений ничего не сбрасывает. Тест в Task 4.
2. **Сообщение форума без `topic_id` и исключённый General** — считается топиком 1 и фильтруется. Тест в Task 4.
3. **Снятие всех топиков форума / отметка топика у невыбранного форума** — форум снимается / выбирается с исключением остальных. Тест в Task 7.
4. **Ошибка подсчёта одного чата** (flood, нет доступа) — `?` в его блоке, остальные досчитаны, `dry-run` → код 1. Тест в Task 5 и 6.
5. **Сквозные номера на страницах** — `21-23` на 2-й странице, `45` с 2-й страницы (другая страница), `p 99` вне диапазона — ошибка без изменений. Тест в Task 8.

---

### Task 1: `config` — `exclude_topics`, запись, симлинк, fsync

**Files:** `internal/config/config.go`, `internal/config/write.go`, тесты рядом.

**Interfaces — Produces:**
- `Targets.ExcludeTopics map[int64][]int` (`yaml:"exclude_topics"`); валидация: ID топиков > 0.
- `type Selected struct { Chats []int64; ChatTitles map[int64]string; ExcludeTopics map[int64][]int; TopicTitles map[int64]map[int]string }`
- `func WriteTargets(path string, sel Selected) error` — заменяет прежнюю сигнатуру; `exclude_topics` пишется только для чатов из `Chats` с непустым списком, flow-последовательностью с комментарием `# <chat>: <topic>, <topic>` (комментарий на узле последовательности), ключ удаляется, если исключений нет; путь-симлинк разрешается `filepath.EvalSymlinks` до записи; `tmp.Sync()` перед `Close`.

**Тесты (сначала):** чтение `exclude_topics`; отрицательный/нулевой ID топика — ошибка; запись с исключениями и комментарием, чтение обратно; исключения невыбранного чата не пишутся; пустые исключения удаляют ключ; симлинк остаётся симлинком, файл-цель обновлён. Существующие вызовы `WriteTargets` переводятся на `Selected`.

### Task 2: `store` — миграция 003 и `ApplyTopicExclusions`

**Files:** `internal/store/migrations/003_topic_exclusions.sql`, `internal/store/chats.go`, тест.

**Interfaces — Produces:**
- колонка `chats.excluded_topics_json TEXT NOT NULL DEFAULT '[]'`;
- `func (s *Store) ApplyTopicExclusions(ctx context.Context, chatID int64, excluded []int) (reset bool, err error)` — в одной транзакции: читает прежнее множество; если в новом нет хотя бы одного прежнего топика и курсор существует — `oldest_id = newest_id + 1`, `backfill_done = 0`; записывает новое множество (отсортированное).

**Тесты:** первая запись — без сброса; расширение множества — без сброса; сужение — сброс курсора; повтор того же множества — без сброса; сужение без курсора — без ошибки.

### Task 3: `tgclient` — исключения в `Target`, счётчики, Saved Messages

**Files:** `internal/tgclient/targets.go`, `internal/tgclient/api.go`, тесты.

**Interfaces — Produces:**
- `Target.ExcludedTopics []int` — из `sel.ExcludeTopics[id]` для форумов; `SelectTargets` добавляет предупреждение `exclude_topics <id>: chat is not selected` для ключей вне выбранных чатов.
- `type TypeCounts struct { Photos, Videos, Files, Voice, Round, Music, GIFs, Links int }`
- `func (a *API) SearchCounters(ctx, peer) (TypeCounts, error)` — один `messages.getSearchCounters` с фильтрами Photos, Video, Document, Voice, RoundVideo, Music, Gif, Url.
- `func (a *API) TopicCount(ctx, peer) (int, error)` — `messages.getForumTopics(limit=1)` → `Count`.
- `func (a *API) TopicMessageCount(ctx, peer, topicID int) (int, error)` — `messages.getReplies(msg_id=topicID, limit=1)` → count (как `HistoryCount`).
- `Dialogs`: диалог с `ID == selfID` получает `Title = "Saved Messages"` — через `func (a *API) Dialogs(ctx)` не знает self, поэтому отдельная функция `func NameSelf(ds []Dialog, selfID int64)` вызывается там, где известен self (`selectChats`, `cmdSetup`).

**Тесты:** `SelectTargets` переносит исключения и предупреждает о лишнем ключе; `SearchCounters` раскладывает ответ по полям (порядок фильтров в ответе произвольный); `TopicCount`; `TopicMessageCount`; `NameSelf`.

### Task 4: `syncer` — фильтр топиков и сброс backfill

**Files:** `internal/syncer/syncer.go`, тест.

- `syncChat` для форума после `UpsertChat` и `syncTopics` вызывает `ApplyTopicExclusions(t.ExcludedTopics)`; при `reset` — `Log.Info("topic re-included, re-reading history", …)` и курсор перечитывается. ID из `ExcludedTopics`, которых нет среди топиков форума, — `Log.Warn`.
- `save` пропускает сообщения, у которых `res.Message.TopicID` в исключённых (media тоже не регистрируется).

**Тесты (fake Telegram как в существующих тестах syncer):** сообщения исключённого топика и их медиа не сохраняются, курсор дошёл до конца; сообщение без reply-header в форуме (General) отфильтровано при исключённом 1; после удаления топика из исключений повторный `Run` дописывает его сообщения, остальные не дублируются; третий `Run` без изменений не сбрасывает курсор (HistoryBefore не вызывается).

### Task 5: пакет `internal/stats`

**Files:** `internal/stats/stats.go`, тест.

**Interfaces — Produces:**
```go
type Counter interface {
	HistoryCount(ctx context.Context, peer tg.InputPeerClass) (int, error)
	SearchCounters(ctx context.Context, peer tg.InputPeerClass) (tgclient.TypeCounts, error)
	ForumTopics(ctx context.Context, peer tg.InputPeerClass) ([]tgclient.Topic, error)
	TopicMessageCount(ctx context.Context, peer tg.InputPeerClass, topicID int) (int, error)
}
type Item struct { ID int64; Type, Title string; Peer tg.InputPeerClass; ExcludedTopics []int }
type TopicStats struct { ID int; Title string; Count int; Excluded bool }
type ChatStats struct { Item Item; Total int; Types tgclient.TypeCounts; Topics []TopicStats; Err error }
func (c ChatStats) Saved() int // Total минус сообщения исключённых топиков
func Collect(ctx context.Context, c Counter, items []Item, progress func(done, total int)) []ChatStats
```
- General (ID 1) не запрашивается: `Total − Σ остальных`, не меньше 0.
- Ошибка любого запроса чата → `Err`, переход к следующему чату; `ctx` отменён → оставшиеся получают `ctx.Err()`.

**Тесты:** обычный чат; форум с исключённым топиком (`Saved`, General вычислен); ошибка одного чата не мешает второму; `progress` вызван по разу на чат.

### Task 6: вывод и `sync --dry-run`

**Files:** `cmd/tgbup/report.go` (заменяет `plan.go`), `cmd/tgbup/sync.go`, тесты.

**Interfaces — Produces:**
- `func cleanTitle(s string, max int) string` — управляющие символы/таб/переводы строк → пробел/удаление, схлопывание пробелов, обрезка до `max` рун с `…`.
- `func groupDigits(n int) string` (есть).
- `func printReport(w io.Writer, chats []stats.ChatStats)` — первая строка `would archive N chats, ~X messages:` (X — сумма `Saved()`), затем блоки из spec §6, итог `══ Total: …`; нулевые типы не печатаются; блок с `Err` — `?` и причина.
- `func progressPrinter(w io.Writer, tty bool) func(done, total int)`
- `func statItems(targets []tgclient.Target) []stats.Item`
- `dryRun`: `stats.Collect` → `printReport` → предупреждения; код 1 при предупреждениях или любой `Err`.

**Тесты:** формат блоков (форум с исключённым топиком, личный чат, чат с ошибкой, итог); `cleanTitle`; прогресс для не-tty — строки `counting 1/2`.

### Task 7: `selector` — `State`, «All chats», топики

**Files:** `internal/selector/selector.go`, тест.

**Interfaces — Produces:**
- константы `AllChats = "All chats"`, `OutsideFolders = "No folder"`, `ArchiveGroup = "Archived"`; `Groups` начинается с «All chats» (все диалоги).
- `type State struct { Chats Selection; Excluded map[int64]map[int]bool }`, `func NewState() *State`
- `func (s *State) TopicArchived(chat int64, topic int) bool`
- `func (s *State) ToggleTopics(chat int64, all []int, idx []int)`, `func (s *State) SetAllTopics(chat int64, all []int, on bool)` — пусто → форум снят, исключения сброшены; отметка у невыбранного → форум выбран, остальные исключены.
- `func (s *State) ExcludedFor(chat int64) []int` (отсортировано; только для выбранного чата)
- `Initial(dialogs, folders, sel) (*State, []string)` — плюс исключения из `sel.ExcludeTopics` для выбранных.
- `Search` принимает `@username`.
- `ParseIndices` сообщения ошибок — на английском.

**Тесты:** обновить существующие под `State`/новые имена; «All chats» первая группа со всеми диалогами; снятие всех топиков снимает форум; отметка топика у невыбранного форума; `ExcludedFor`; `Initial` с `exclude_topics`; `@famforum`.

### Task 8: диалог `setup`

**Files:** `cmd/tgbup/setupcmd.go`, тест.

- Главный экран и списки — spec §6, английский; 20 на странице; сквозные номера; команды `a`/`n` (страница), `A`/`N` (список), `>`, `<`, `p N`, `t N`, Enter.
- Счётчики строк лениво: `rowStats func([]tgclient.Dialog) map[int64]rowInfo` (`rowInfo{Msgs, Topics int; Err error}`), кэш в диалоге; в `cmdSetup` — `HistoryCount` и для форумов `TopicCount`.
- Экран топиков: `details func([]stats.Item) []stats.ChatStats` для одного форума; строки `[x]/[ ]`, `Title #id`, `~count`.
- `d`: `details` по выбранным → `printReport` → `Write to <path> (N chats)? [y/N]`; `y/yes/д/да`; иначе `not written`.
- `readLines` сообщает об ошибке чтения (не EOF) в вывод.
- `cmdSetup`: `NameSelf`, запись `config.WriteTargets(path, config.Selected{…})` с названиями топиков из собранной статистики/`ForumTopics`.

**Тесты:** обновить существующие под английский; страницы (`>`, `<`, `p 2`, `p 99` — ошибка); `21-23` на 2-й странице; `a` на странице vs `A`; `t` у не-форума → `not a forum`; экран топиков исключает топик и итог показывает `excluded`; Ctrl+C/EOF без записи; ошибка счётчика строки → `?`.

### Task 9: Ctrl+C в `login`

**Files:** `internal/tgclient/login.go`, `cmd/tgbup/login.go`, тест.

- `terminalAuth` читает строки через канал (как `readLines`) и `select` c `ctx.Done()`; `Phone/Code/Password` возвращают `ctx.Err()` при отмене.
- Пароль: `readPassword` выполняется в горутине; при отмене — `term.Restore` сохранённого до чтения состояния терминала (иначе эхо останется выключенным).

**Тесты:** отменённый `ctx` при ожидании телефона → `Phone` возвращает `context.Canceled` быстро; то же для пароля (фейковый `readPassword`, блокирующийся навсегда).

### Task 10: e2e и README

**Files:** `e2e/e2e_test.go`, `README.md`.

- `TestTopicExclusion`: конфиг с `exclude_topics: {<forum>: [<TopicID>]}` → `dry-run` содержит `excluded:` и название топика; `sync` не сохраняет `fx.TopicMsg`; конфиг без исключения → повторный `sync` сохраняет `fx.TopicMsg`.
- `TestDryRunListsChatsWithoutArchiving` дополняется проверкой строки `Total:`.
- README: `setup` (страницы, топики), `exclude_topics`, пример вывода `dry-run`.
- Прогон: `make e2e-seed`, `make e2e-run`, `make e2e-clean` (только тестовые серверы).

# tgbup: бэкап, run, расписание, уведомления, зависимости — план

> **Выполнен.** Актуальное устройство — `docs/PROJECT.md`, решения — в спеках (раздел «Расхождения с реализацией»). План не поддерживается.

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans.

**Goal:** реализовать `docs/superpowers/specs/2026-10-02-tgbup-backup-design.md`.

**Формат:** компактный (выполнение без подтверждений, поручено пользователем):
файлы, интерфейсы, тесты, которые пишутся первыми.

## Global Constraints

- Новых Go-зависимостей нет (restic/rclone — внешние бинарники).
- Бинарник tgbup против Telegram — только тестовые серверы; боевую сессию не трогать.
- Тесты не ходят в сеть (фейковые HTTP/Bot API/launchctl); интеграция с restic —
  только при доступном бинарнике.
- Каждый коммит — conventional, с трейлером, сразу `git push origin main`.

## Review Focus

1. Неверная контрольная сумма скачанного бинарника — файл не сохраняется, ошибка. Тест в Task 1.
2. Один репозиторий недоступен — второй всё равно получает снимок, отметки prune/check не сдвигаются у упавшего. Тест в Task 3.
3. `integrity_check` не ok — снимка нет, ошибка в отчёте и уведомлении. Тест в Task 3.
4. `run` при ошибке sync всё равно делает бэкап и пишет `runs`. Тест в Task 5.
5. Нет `BOT_TOKEN` — уведомления молча выключены, запуск не падает. Тест в Task 4.

### Task 1: `internal/deps`

`type Tool struct{ Name, Version string; Assets map[string]Asset }` (`"darwin/arm64"` → `{File, SHA256, Format}`),
`var Restic, Rclone Tool`; `type Finder struct{ StateDir, Configured string; BaseURL string; HTTP *http.Client; LookPath func(string)(string,error); Exe func()(string,error) }`;
`func (f Finder) Find(t Tool) (string, bool)`, `func (f Finder) Ensure(ctx, t Tool) (string, error)`.
**Тесты:** порядок поиска; скачивание bz2 и zip с фейкового сервера; неверная сумма → ошибка, файла нет; неизвестная платформа → ошибка.

### Task 2: `store` — integrity, VACUUM INTO, `runs`

Миграция 006 `runs(id, started_at, finished_at, ok, summary_json)`; `IntegrityCheck(ctx) error`,
`VacuumInto(ctx, path) error`, `AddRun(ctx, Run) error`, `RunsSince(ctx, t) ([]Run, error)`.
**Тесты:** копия открывается и содержит данные; integrity ok; runs пишутся/читаются.

### Task 3: `internal/backup`

`type Options struct{ Restic, Rclone string; Repos []string; Retention; PruneEvery, CheckEvery; CheckSubset; Password, RcloneConfigPass string; StateDir, ArchiveDir, ConfigPath string; Now func() time.Time }`;
`type RepoResult struct{ Repo string; Snapshot string; Pruned, Checked bool; Err error }`; `type Report struct{ Integrity error; Repos []RepoResult }`;
`func Run(ctx, db *store.Store, o Options) Report`, `func Init(ctx, o Options) []RepoResult`.
**Тесты (настоящий restic, skip без него):** init + снимок содержит БД и медиа; prune/check по интервалам (meta);
второй репозиторий работает при сломанном первом; integrity fail → без снимка.

### Task 4: `internal/notify`

`type Bot struct{ Token, ChatID, BaseURL string; HTTP *http.Client }`, `func (b Bot) Enabled() bool`, `Send(ctx, text) error`;
`func ErrorText(host string, r RunSummary) string`, `func SummaryText(host string, runs []store.Run, s ArchiveStats) string`.
**Тесты:** фейковый Bot API получает текст; без токена — Enabled false; форматы.

### Task 5: `run`, `backup`, `deps`, `notify test` в `cmd/tgbup`

`runSyncWith(ctx, st, log, out)` вынести из `cmdSync`; `cmdRun`: lock → sync → backup → runs → notify (ошибка сразу; сводка по интервалу).
**Тесты:** с фейковыми sync/backup/bot (через интерфейс) — бэкап при ошибке sync, запись runs, уведомления.

### Task 6: `schedule` и `make install`

launchd plist (`StartCalendarInterval` 00:30/06:30/12:30/18:30, логи в `<state>/logs`), `launchctl bootstrap/bootout gui/<uid>`;
`make install` → `~/.local/bin/tgbup`. **Тесты:** plist; install/uninstall через фейковый launchctl.

### Task 7: README и e2e

README: установка, deps, бэкап, восстановление `restic restore`, расписание, бот. e2e: не меняется (бэкап — интеграционным тестом).

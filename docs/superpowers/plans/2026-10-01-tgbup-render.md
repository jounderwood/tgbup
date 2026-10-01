# tgbup: рендеринг и экспорт — план

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** реализовать `docs/superpowers/specs/2026-10-01-tgbup-render-design.md`.

**Architecture:** `store` отдаёт чаты/топики/месяцы с отпечатками и сообщения месяца
с медиа и именами авторов. Новый пакет `internal/render` превращает их в модель
страницы, рисует HTML (`html/template`) и Markdown (`text/template`), пишет индекс
поиска и пропускает месяцы с неизменным отпечатком. `cmd/tgbup` вызывает его после
`sync`, командой `render` и для `export` (рендер подмножества во временную папку +
копии медиа → zip).

**Tech Stack:** Go stdlib (`html/template`, `text/template`, `embed`, `archive/zip`),
`modernc.org/sqlite`. Новых зависимостей нет.

**Spec:** `docs/superpowers/specs/2026-10-01-tgbup-render-design.md`

**Формат.** Выполнение без подтверждений (поручено пользователем), поэтому план
компактный: файлы, интерфейсы, тесты, которые пишутся первыми.

## Global Constraints

- Новых зависимостей нет. Весь вывод и интерфейс страниц — английский.
- Бинарник и тесты — только против тестовых серверов; боевую сессию не трогать.
- Каждый коммит — conventional, с attribution-трейлером, сразу `git push origin main`.
- Текст из Telegram всегда экранируется; ссылки — только `http(s):`, `mailto:`, `tel:`, `tg:`.

## Review Focus

1. **Разметка с эмодзи и пересекающимися entities** — смещения UTF-16 не рвут суррогатные пары, теги закрываются корректно. Тест в Task 3.
2. **Ответ на сообщение из другого месяца/топика** — ссылка ведёт на правильную страницу. Тест в Task 5.
3. **Инкрементальность** — правка старого сообщения перерисовывает только его месяц; без изменений — ноль перерисовок. Тест в Task 5.
4. **Названия чатов/топиков, совпадающие или с опасными символами** — папки как в `files/`, без выхода за пределы `html/`. Тест в Task 5.
5. **Экспорт с фильтрами** — в zip только выбранный топик/период и только их медиа. Тест в Task 7.

---

### Task 1: `browse.Clean` и имена папок чатов

`browse.clean` → экспорт `Clean`; `chatFolders` → `FolderNames(chats []FolderChat) map[int64]string`
(`FolderChat{ID, Title, UpgradedTo, UpgradedTitle}`), используется и `files/`, и `render`.
Тесты browse остаются зелёными.

### Task 2: `store` — миграция 004 и запросы для рендера

- `render_pages(chat_id, topic_id, month, fingerprint, PRIMARY KEY(chat_id, topic_id, month))`.
- `type MonthKey struct{ ChatID int64; TopicID int; Month, Fingerprint string }`;
  `func (s *Store) Months(ctx, chatIDs []int64) ([]MonthKey, error)` — месяцы по местному времени
  (`strftime('%Y-%m', date, 'unixepoch', 'localtime')`), отпечаток из count/max(id)/max(edit_date)/sum(pinned)/реакций/статусов медиа; пустой `chatIDs` — все чаты.
- `func (s *Store) RenderedFingerprints(ctx) (map[MonthKey]string, error)`, `SetRendered(ctx, MonthKey) error`.
- `type RenderChat struct{ ID int64; Type, Title, Username string; MigratedFromID int64; Topics map[int]string }`;
  `func (s *Store) RenderChats(ctx) ([]RenderChat, error)`.
- `type RenderMessage struct{ store.Message поля + SenderName string; Media *RenderMedia }`,
  `type RenderMedia struct{ Kind, FileName, Mime string; Size int64; Status, LocalPath, Error string }`;
  `func (s *Store) MonthMessages(ctx, chatID int64, topicID int, month string) ([]RenderMessage, error)`.
- `type MessageRef struct{ ChatID int64; ID, TopicID int; Month, SenderName, Text string }`;
  `func (s *Store) MessageRefs(ctx, chatID int64, ids []int) (map[int]MessageRef, error)`.

**Тесты:** месяцы и отпечатки (меняется от правки, медиа done, реакции; не меняется без изменений); имена авторов из users/peers/chats; ссылки на сообщения.

### Task 3: `render` — разметка

`func HTMLText(text, entitiesJSON string) template.HTML`, `func MarkdownText(text, entitiesJSON string) string`, `func safeURL(string) (string, bool)`.

**Тесты:** все типы, вложенные/пересекающиеся, эмодзи (суррогаты) до/внутри, экранирование `<script>`, `javascript:` ссылка → текст, pre/code, spoiler, mention_name, Markdown-экранирование.

### Task 4: `render` — страница месяца (HTML и Markdown)

Модель `Page{Breadcrumbs, Title, Prev, Next, Messages []PageMessage}`, `PageMessage` (автор, время, текст, ответ, пересылка, медиа, служебное, реакции, пометки); шаблоны в `internal/render/templates/` через `embed`; `func (r *Renderer) monthHTML/monthMD`.

**Тесты:** фрагменты страницы для каждого вида: текст, ответ (та же страница / другая / вне архива), пересылка, фото/видео/кружок/голос/аудио/документ, заглушки skipped/failed/pending, служебные (pin, topic create, прочие), реакции, edited/pinned; Markdown для тех же.

### Task 5: `render` — сайт и инкрементальность

`type Options struct{ ArchiveDir string; Full bool; ChatIDs []int64; Topic int; From, To string; Loc *time.Location }`;
`type Result struct{ Pages, Skipped int; Errors []error }`; `func Render(ctx, db *store.Store, opts Options) (Result, error)` — страницы месяцев (по отпечаткам), `index.html`, оглавления чатов, `search/<id>.js`, assets; атомарная запись.

**Тесты:** первый прогон рисует всё, второй — ноль страниц; правка сообщения → одна страница; ответ на сообщение из другого месяца/топика — правильная относительная ссылка; одинаковые/опасные названия → безопасные папки; ошибка одной страницы не мешает другим.

### Task 6: `cmd` — `render`, вызов после `sync`, сводка

`tgbup render [--full]` (lock, без Telegram); `runSync` после `files/` вызывает `render.Render`; сводка `rendered=N`, `render: <ошибки>` → код 1. Usage.

**Тесты:** команда строит `html/index.html` на подготовленном архиве; ошибки рендера валят `ok()`.

### Task 7: `export`

`tgbup export <чат> [--topic] [--from] [--to] [--no-media] -o file.zip`: поиск чата (id/@username/название), топика (название/#id); рендер подмножества во временную папку; копии медиа; `index.html`-редирект; сводка `N messages, M files, ~X MB`; zip атомарно.

**Тесты:** состав zip (страницы, медиа, index.html), фильтр топика и периода, `--no-media`, неоднозначное/ненайденное название → код 2.

### Task 8: e2e и README

e2e: после `sync` есть `html/index.html` и страница месяца форума с текстом сообщения и ссылкой на документ; `export` форума → zip с документом. README: чтение архива, Obsidian, экспорт.

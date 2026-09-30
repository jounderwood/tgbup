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
3. `bin/tgbup login`
4. Выбор чатов: `bin/tgbup setup`. Группы: All chats, папки Telegram, No folder, Archived;
   списки по 20 на странице (`>`/`<`/`p N`), номера сквозные (`21-23,45`), `a`/`n` — страница,
   `A`/`N` — весь список, `s` — поиск по названию или `@username`. У форумов `t N` открывает
   топики: снятая отметка исключает топик из архива, новые топики архивируются автоматически.
   На `d` показывается подробный отчёт и вопрос о записи в `~/.config/tgbup/config.yaml`.
   Проверить, что будет выгружено, без скачивания: `bin/tgbup sync --dry-run`:
   ```
   would archive 2 chats, ~17 062 messages:
   ── 1. Family · forum · -1002263466511 ─────────────────────────────
      ~4 812 msgs: photos 310 · videos 42 · files 18 · voice 97 · round 12 · links 55
      topics 5 of 7:  General #1 ~1 200 · Recipes #3 ~300 · …
      excluded:       Flood #5 ~2 900 · Memes #12 ~150   (~3 050 msgs not saved)
   ── 2. Mom · private · 123456789 ───────────────────────────────────
      ~15 300 msgs: photos 1 204 · videos 80 · files 140 · voice 630 · links 210
   ══ Total: 2 chats, ~17 062 msgs saved · …
   ```
   Конфиг по-прежнему можно править руками:
   ```yaml
   archive_dir: ~/TelegramArchive
   targets:
     folders: ["Семья", "Бизнес"]   # папки Telegram
     include: ["@some_group"]        # плюс отдельные чаты
     exclude: []
     exclude_topics:                 # форум → ID топиков (видны в dry-run как «#5»)
       -1002263466511: [5, 12]
   ```
   В `include` надёжнее числовые ID (`-1001234567890`): username могут сменить. Если чат из `include`
   не найден среди диалогов, `sync` завершается с ошибкой, чтобы архивирование не прекратилось молча.
   Исключённый топик выкачивается как текст форума, но не сохраняется; если вернуть топик, следующий
   `sync` один раз перечитывает историю форума и дописывает пропущенное.
5. Первая полная выгрузка: `bin/tgbup sync --takeout` (Telegram попросит подтвердить экспорт в клиенте — подтвердите и запустите снова).
6. Дальше: `bin/tgbup sync` (по расписанию — см. план 3).

Тестовые серверы Telegram: `TGBUP_TEST_DC=1 bin/tgbup login` / `sync` — отдельная сессия `session-test`,
боевая не затрагивается. Аккаунт для тестовой среды заводится в официальном клиенте в тестовом режиме.
`make e2e` создаёт на тестовом аккаунте форум со всеми видами сообщений, гоняет настоящий `tgbup sync`
(полная выгрузка, инкремент, dry-run, исключение и возврат топика), проверяет архив и удаляет за собой
форум и папку.

Пути переопределяются через `TGBUP_CONFIG`, `TGBUP_SECRETS`, `TGBUP_STATE_DIR`.

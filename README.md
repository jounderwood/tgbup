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
4. Выбор чатов: `bin/tgbup setup` — папки Telegram показываются для навигации,
   отмечаете конкретные чаты, в конце видно число сообщений и подтверждение.
   Выбор пишется в `~/.config/tgbup/config.yaml` как список числовых ID.
   Проверить, что будет выгружено, без скачивания: `bin/tgbup sync --dry-run`.
   Конфиг по-прежнему можно править руками:
   ```yaml
   archive_dir: ~/TelegramArchive
   targets:
     folders: ["Семья", "Бизнес"]   # папки Telegram
     include: ["@some_group"]        # плюс отдельные чаты
     exclude: []
   ```
   В `include` надёжнее числовые ID (`-1001234567890`): username могут сменить. Если чат из `include`
   не найден среди диалогов, `sync` завершается с ошибкой, чтобы архивирование не прекратилось молча.
5. Первая полная выгрузка: `bin/tgbup sync --takeout` (Telegram попросит подтвердить экспорт в клиенте — подтвердите и запустите снова).
6. Дальше: `bin/tgbup sync` (по расписанию — см. план 3).

Тестовые серверы Telegram: `TGBUP_TEST_DC=1 bin/tgbup login` / `sync` — отдельная сессия `session-test`,
боевая не затрагивается. Аккаунт для тестовой среды заводится в официальном клиенте в тестовом режиме.
`make e2e` создаёт на тестовом аккаунте форум со всеми видами сообщений, гоняет настоящий `tgbup sync`
(полная выгрузка + инкремент), проверяет архив и удаляет за собой форум и папку.

Пути переопределяются через `TGBUP_CONFIG`, `TGBUP_SECRETS`, `TGBUP_STATE_DIR`.

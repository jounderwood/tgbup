# tgbp

Автономный бэкап Telegram-чатов, групп и форумов в SQLite + файлы. Дизайн: `docs/superpowers/specs/2026-09-28-tgbp-design.md`.

## Установка

```bash
brew install go   # нужен Go 1.25+
make build        # → bin/tgbp
```

## Настройка

1. Создайте приложение на https://my.telegram.org → API development tools.
2. Секреты:
   ```bash
   mkdir -p ~/.config/tgbp
   printf 'TG_API_ID=12345\nTG_API_HASH=abcdef\n' > ~/.config/tgbp/secrets.env
   chmod 600 ~/.config/tgbp/secrets.env
   ```
3. Конфиг `~/.config/tgbp/config.yaml`:
   ```yaml
   archive_dir: ~/TelegramArchive
   targets:
     folders: ["Семья", "Бизнес"]   # папки Telegram
     include: ["@some_group"]        # плюс отдельные чаты
     exclude: []
   ```
   В `include` надёжнее числовые ID (`-1001234567890`): username могут сменить. Если чат из `include`
   не найден среди диалогов, `sync` завершается с ошибкой, чтобы архивирование не прекратилось молча.
4. `bin/tgbp login`
5. Первая полная выгрузка: `bin/tgbp sync --takeout` (Telegram попросит подтвердить экспорт в клиенте — подтвердите и запустите снова).
6. Дальше: `bin/tgbp sync` (по расписанию — см. план 3).

Пути переопределяются через `TGBP_CONFIG`, `TGBP_SECRETS`, `TGBP_STATE_DIR`.

# tgbup

Автономный бэкап Telegram-чатов, групп и форумов в SQLite + файлы. Дизайн: `docs/superpowers/specs/2026-09-28-tgbup-design.md`.

## Установка

```bash
brew install go   # нужен Go 1.25+
make install      # → ~/.local/bin/tgbup (добавьте ~/.local/bin в PATH)
tgbup deps        # скачает restic и rclone закреплённых версий с проверкой SHA-256
```

restic и rclone ставить отдельно не нужно: `tgbup deps` (и первый бэкап) скачивают официальные выпуски
в `~/.local/state/tgbup/bin`. Свои бинарники можно указать в конфиге (`deps.restic`, `deps.rclone`) или
положить рядом с `tgbup`.

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
   На `d` показывается подробный отчёт, затем целиком конфиг, который будет записан в
   `~/.config/tgbup/config.yaml`, и вопрос о записи. При первой записи в конец конфига добавляется
   закомментированная справка по всем настройкам со значениями по умолчанию.
   Команды понимаются и в русской раскладке (`б`/`ю` — страницы, `ф`/`т` — все/ни одного и т. д.).
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
6. Бэкап и расписание — раздел ниже.

## Бэкап и расписание

`tgbup run` — то, что запускает расписание: `sync` → снимок restic во все репозитории (даже если `sync`
упал) → запись в историю → бот: ошибки сразу, сводка раз в `notify.summary_every` (7 дней).

1. Пароль репозиториев в `secrets.env`: `RESTIC_PASSWORD=…`. **Сохраните его ещё и в менеджере паролей** —
   без него бэкапы не прочитать.
2. Репозитории в конфиге:
   ```yaml
   backup:
     repositories:
       - "~/Library/Mobile Documents/com~apple~CloudDocs/tgbup-restic"   # iCloud Drive
       - "rclone:gdrive:tgbup-restic"                                     # Google Drive
   ```
   Папку iCloud в Finder пометьте «Всегда хранить на этом Mac». Для Google Drive: `tgbup deps` печатает
   путь к rclone → `<путь> config` → New remote → имя `gdrive`, тип Google Drive, вход через браузер.
3. `tgbup backup --init` — создать репозитории; `tgbup backup` — снимок вручную.
4. Бот: создать у @BotFather, написать ему любое сообщение, chat_id — из
   `https://api.telegram.org/bot<TOKEN>/getUpdates`. В `secrets.env`: `BOT_TOKEN=…`, `BOT_CHAT_ID=…`.
   Проверка: `tgbup notify test`.
5. `tgbup schedule install` — запуск `tgbup run` в 00:30, 06:30, 12:30, 18:30 (пропущенный во сне —
   после пробуждения). `tgbup schedule status`, `tgbup schedule uninstall`. Лог: `~/.local/state/tgbup/logs/run.log`.

В снимке: копия `archive.sqlite` (через `VACUUM INTO`, после `integrity_check`), `config.yaml`, `media/`.
Не входят: `html/`, `md/`, `files/` (пересобираются), сессия Telegram, `secrets.env`. Хранение: 4 последних,
7 дневных, 4 недельных, 12 месячных; `prune` — раз в сутки, `check` (5% данных) — раз в 30 дней.

Восстановление, в том числе на новом компьютере (tgbup не нужен):
1. Поставить restic (`brew install restic` или выпуск с github.com/restic/restic); для репозитория в Google Drive —
   ещё rclone и заново `rclone config` с тем же именем удалённого хранилища (`gdrive`): настройки rclone в снимок не входят.
2. Восстановить последний снимок:
   ```bash
   RESTIC_PASSWORD=… restic -r "<репозиторий>" restore latest --target ~/restore
   ```
   Пути в снимке абсолютные: в `~/restore` будут `…/.local/state/tgbup/backup/archive.sqlite`, `…/config.yaml`
   и `…/TelegramArchive/media/`.
3. Положить `archive.sqlite` и `media/` в `archive_dir`, `config.yaml` — в `~/.config/tgbup/`; `secrets.env`
   создать заново. Сессия Telegram в снимок не входит — `tgbup login`. Затем `tgbup render` и `tgbup files`
   пересоберут просмотр.

## Чтение архива

После каждого `sync` архив отрисовывается в `~/TelegramArchive/html/` и `~/TelegramArchive/md/`:
- `html/index.html` — открыть в браузере: список чатов и поиск по всему архиву. Чат (у форума — каждый
  топик) открывается на последней странице; страницы по 500 сообщений, `Jump to date` — календарь с
  переходом к нужному дню. Фото, видео, голосовые, файлы, превью ссылок, места, опросы и чеклисты —
  прямо в ленте;
- `html/media.html` — раздел «Медиа»: фото, видео, файлы, голосовые, ссылки, места, контакты, опросы и
  чеклисты с фильтрами по типу, чату, периоду и названию (фильтры сохраняются в адресе — можно в закладки);
- `md/` — те же страницы в Markdown; хранилище Obsidian — папка `~/TelegramArchive`.

Перерисовываются только изменившиеся страницы. Вручную, без Telegram: `bin/tgbup render` (`--full` — всё заново).

Отправить чат кому-то: `bin/tgbup export Family -o family.zip` — zip со страницами и медиа этого чата,
открывается двойным кликом по `index.html`. Чат — название, `@username` или ID; можно сузить:
`--topic Expenses`, `--from 2026-01 --to 2026-03`, `--no-media`.

Медиа хранятся в `media/<тип>/<id>` (один файл на все пересылки, имя от отправителя в путь не попадает).
Для просмотра каждый `sync` пересобирает `files/` — те же файлы жёсткими ссылками, без лишнего места:
`files/<чат>/[<топик>/]<дата время> <имя>.<расширение>`; старая группа, из которой вырос форум, — в
`files/<форум>/before upgrade/`. Пересобрать вручную без Telegram: `bin/tgbup files`. Папки `files/`, `html/`, `md/`
производные: их можно удалить, и в бэкап они не нужны.

Тестовые серверы Telegram: `TGBUP_TEST_DC=1 bin/tgbup login` / `sync` — отдельная сессия `session-test`,
боевая не затрагивается. Аккаунт для тестовой среды заводится в официальном клиенте в тестовом режиме.
`make e2e` создаёт на тестовом аккаунте форум со всеми видами сообщений, гоняет настоящий `tgbup sync`
(полная выгрузка, инкремент, dry-run, исключение и возврат топика), проверяет архив и удаляет за собой
форум и папку.

Пути переопределяются через `TGBUP_CONFIG`, `TGBUP_SECRETS`, `TGBUP_STATE_DIR`.

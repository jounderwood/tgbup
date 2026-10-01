# tgbup: бэкап, запуск по расписанию, уведомления, зависимости — дизайн

Дата: 2026-10-02
Статус: согласован в чате
Основа: `2026-09-28-tgbup-design.md` §5, §8, §9, §11, §13 (решения оттуда в силе,
ниже — уточнения и новое)

## 1. Цель

Архив обновляется сам 4 раза в сутки, после каждого обновления уходит в два
облака снимками restic, о любой ошибке сразу пишет бот, раз в неделю — сводка.
Установка — без ручной подготовки зависимостей; задел под NAS, Windows и
будущий интерфейс (веб/десктоп).

## 2. Решения

- **restic и rclone — внешние бинарники закреплённых версий** (как Backrest,
  autorestic): restic не импортируется как Go-модуль (`internal/`), а
  стандартный `restic restore` должен восстанавливать архив без tgbup.
- **Зависимости ставит сам tgbup**: при отсутствии скачивает официальный выпуск
  под свою ОС/архитектуру в `<state>/bin` и проверяет SHA-256 по суммам,
  зашитым в tgbup. Порядок поиска: путь из конфига (`deps.restic`,
  `deps.rclone`) → рядом с бинарником tgbup → `<state>/bin` → `PATH` → скачать.
- **Расписание ставит сам tgbup**: `tgbup schedule install|uninstall|status`;
  сейчас — launchd (Mac), позже — systemd/cron (NAS), Планировщик (Windows).
- **Логика — в пакетах, CLI тонкий**: бэкап и запуск возвращают
  структурированные отчёты; история запусков — в таблице `runs`. Будущий
  `tgbup serve`/десктоп вызывает те же пакеты и может сам запускать `run` по
  таймеру; пересечение исключает `run.lock`.

## 3. Версии зависимостей

| | Версия | Платформы (sha256 из официального `SHA256SUMS`, взяты 2026-10-02 по HTTPS; подпись `.asc` не проверялась) |
|---|---|---|
| restic | 0.19.1 | darwin amd64/arm64, linux amd64/arm64 (`.bz2`), windows amd64 (`.zip`) |
| rclone | 1.75.1 | osx amd64/arm64, linux amd64/arm64, windows amd64 (`.zip`) |

Обновление версий — правка таблицы в коде (версия + суммы), новый выпуск tgbup.

## 4. Команды

| Команда | Что делает |
|---|---|
| `tgbup deps` | Находит или скачивает restic и rclone, печатает пути и версии |
| `tgbup backup [--init]` | Снимок во все репозитории + forget/prune/check по расписанию; `--init` создаёт недостающие репозитории |
| `tgbup run` | `sync` → `backup` → уведомление; пишет запись в `runs`; это вызывает расписание |
| `tgbup schedule install\|uninstall\|status` | Задача launchd `tgbup run` в 00:30, 06:30, 12:30, 18:30 |
| `tgbup notify test` | Отправляет тестовое сообщение ботом |

## 5. Бэкап

- Перед снимком: `PRAGMA integrity_check`; не `ok` → бэкап не делается, ошибка.
- `VACUUM INTO <state>/backup/archive.sqlite` — консистентная копия; рядом
  копия `config.yaml`. Снимок — два пути: `<state>/backup/` и
  `<archive>/media`. Теги снимка: `tgbup`; хост — имя машины.
- Для каждого репозитория из `backup.repositories` по очереди:
  `restic backup` → `restic forget --keep-last/daily/weekly/monthly` (каждый
  раз) → `restic prune`, если с прошлого прошло ≥ `prune_every` → `restic
  check --read-data-subset=<check_read_subset>`, если ≥ `check_every`.
  Отметки времени — в `meta` по репозиторию.
- Сбой одного репозитория не останавливает остальные; запуск в целом — ошибка.
- Окружение restic: `RESTIC_PASSWORD` из секретов; для `rclone:`-репозиториев
  `-o rclone.program=<путь к rclone>`, `RCLONE_CONFIG_PASS` из секретов если есть.
- `--init`: для каждого репозитория `restic cat config`; нет → `restic init`.
- Не входят в снимок: `html/`, `md/`, `files/` (пересобираются), сессия,
  `secrets.env`.

## 6. Запуск (`run`)

1. `run.lock` (занят — выход 0).
2. `sync` (как сейчас, включая `files/` и рендер).
3. `backup` — всегда, даже если `sync` завершился с ошибкой: всё, что уже на
   диске, должно попасть в облако.
4. Запись в `runs`: `started_at, finished_at, ok, summary_json` (новые
   сообщения/медиа, ошибки sync, результат по каждому репозиторию).
5. Уведомление: при ошибке — сразу; сводка — если с последней прошло
   ≥ `notify.summary_every`.

Код выхода: 0 — всё хорошо; 1 — была ошибка.

## 7. Уведомления

- Bot API `sendMessage` (`BOT_TOKEN`, `BOT_CHAT_ID` из секретов); без
  них уведомления выключены (в выводе `notify: not configured`).
- Ошибка: `⚠️ tgbup: <host>` + перечень ошибок (sync, бэкап по репозиториям),
  кратко, без стеков.
- Сводка за период по `runs`: новых сообщений и медиа, размер архива
  (`archive.sqlite` + `media/`), последний успешный бэкап по каждому
  репозиторию, число окончательно не скачанных файлов, число запусков с
  ошибками. Отметка `last_summary_at` в `meta`.
- Сбой отправки — в лог и код выхода, запуск не валит.

## 8. Конфиг и секреты

```yaml
backup:
  repositories:
    - "~/Library/Mobile Documents/com~apple~CloudDocs/tgbup-restic"
    - "rclone:gdrive:tgbup-restic"
  retention: {last: 4, daily: 7, weekly: 4, monthly: 12}
  prune_every: 1d
  check_every: 30d
  check_read_subset: 5%
deps:                # необязательно: свои бинарники вместо скачанных
  restic: ""
  rclone: ""
notify:
  summary_every: 7d
```

Секреты: `RESTIC_PASSWORD` (обязателен для бэкапа), `RCLONE_CONFIG_PASS`
(если конфиг rclone зашифрован), `BOT_TOKEN`, `BOT_CHAT_ID`.

## 9. Установка (Mac, сейчас)

`make install`: собирает tgbup в `~/.local/bin/tgbup`. Дальше:
`tgbup deps` → `tgbup backup --init` → `tgbup schedule install`.
Подключение Google Drive — `rclone config` (скачанным rclone: путь печатает
`tgbup deps`). Позже: Docker-образ (NAS), Scoop/zip (Windows), Homebrew.

## 10. Тесты

- `deps`: выбор файла по ОС/архитектуре; проверка суммы (неверная — ошибка,
  файл не сохраняется); распаковка bz2/zip; порядок поиска (конфиг → рядом →
  кэш → PATH) — без сети, через фейковый HTTP-сервер.
- `backup`: интеграционный тест с настоящим restic на локальном временном
  репозитории (skip, если restic недоступен): init, снимок содержит копию БД
  и медиа, forget по retention, prune/check по расписанию; сбой одного
  репозитория не мешает второму; `integrity_check` не ok — без снимка.
- `run`: запись в `runs`, уведомление при ошибке, сводка по интервалу —
  с фейковыми sync/backup/bot.
- `notify`: формат сообщений; фейковый Bot API.
- `schedule`: генерация plist; install/uninstall через фейковый `launchctl`.

## 11. Вне области (сейчас)

Docker, Scoop/zip, Homebrew; systemd/cron и Планировщик Windows; доработки под
Windows (блокировка, пути, права); веб/десктоп-интерфейс; восстановление
командой tgbup (пока — `restic restore`, описано в README).

# tgbup

> **Work in progress.** tgbup is under active development and has no stable release yet: settings, the archive layout
> and commands may still change between versions, and some parts (a password-protected export) are not done. Use it at
> your own risk and keep the backups on.

An archive of your Telegram chats, groups and forums, on your own computer. Messages are kept in an SQLite database,
files sit next to it under readable names. The archive reads in a browser and in Obsidian, updates itself on a
schedule, and encrypted backups go to every place you choose.

- **Telegram is only read.** Every call is checked against an allowlist: tgbup cannot send, edit or delete anything in
  your account (see [Security and privacy](#security-and-privacy)).
- **Nothing is deleted.** Messages deleted in Telegram stay in the archive. Restoring from a backup never overwrites
  what is already on disk.
- **Everything stays with you.** The control page opens only on this computer, and only with the key in the address
  tgbup prints. Keys and passwords are in a hidden file with mode 600, backups are encrypted.

Made for macOS. On Linux (a NAS, say) downloading, backups, restore and the control page work; the schedule is set up
there with cron or systemd (`tgbup run`), and there is no folder picker or clipboard.

How it works inside (subsystems, layout on disk) and the decisions log, in Russian: [`docs/PROJECT.md`](docs/PROJECT.md).

## Quick start

You need a phone with Telegram (for the login code) and 10–15 minutes. The first download may take longer if the
chats hold a lot of media. Install tgbup in one of three ways.

**A release build.** [Releases](https://github.com/jounderwood/tgbup/releases) has archives for macOS (Apple Silicon
and Intel) and Linux. Put `tgbup` into `~/.local/bin` and open the setup:

```bash
xattr -d com.apple.quarantine ~/.local/bin/tgbup
```

```bash
tgbup serve --open
```

The first command is for macOS only: the build is not signed, and without it macOS refuses to run it.

**With Go** (1.26 or newer):

```bash
go install github.com/jounderwood/tgbup/cmd/tgbup@latest
```

This puts `tgbup` into `~/go/bin`; then `tgbup serve --open`.

**From source:**

```bash
make setup
```

`make setup` builds tgbup, installs it as `~/.local/bin/tgbup` and opens the setup page in the browser. If you prefer
the terminal, `make setup-cli` (or `tgbup init`) runs the same steps.

## Setup, step by step

The Settings page and `tgbup init` go through the same steps. Done steps are ticked and not asked again. You can stop
at any point: `make setup` or `tgbup init` again picks up where you left off. The same way later changes anything:
chats, backup places, schedule, keys, password.

| # | Step | What you do |
|---|---|---|
| 1 | Telegram keys | at https://my.telegram.org → API development tools, create an app and paste `api_id` and `api_hash` |
| 2 | Login | phone → the code from the Telegram app → the two-step verification password, if you have one |
| 3 | Archive folder | `~/Tgbup/archive` by default («Choose…» opens a folder picker). **Or** «Restore from a backup», see [Restore](#restore). The archive can later be moved to another folder as a whole: instantly on the same disk, with a copy and a check to another one |
| 4 | Chats | tick chats, Telegram folders and forum topics right in this step (see [Choosing chats](#choosing-chats)) |
| 5 | Backups | a folder on this Mac (`~/Tgbup/backup` by default), more places, and the password |
| 6 | Notification bot (optional) | create a bot with @BotFather, paste its token and write to the bot; tgbup finds the chat_id itself and sends a test message |
| 7 | First download | Telegram asks you to allow a data export — allow it in the app. It also works without the export, only slower. This is an ordinary run (`tgbup run --takeout`): it goes into the history and makes the first backup right away |
| 8 | Schedule | run times (00:30, 06:30, 12:30, 18:30 by default), installing and a test run. The times are changed and the schedule turned off here later too |
| 9 | `PATH` (terminal only) | adds `~/.local/bin` to the shell profile so the `tgbup` command works everywhere |

**The backup password** is never set silently. tgbup generates one and shows it («Use this password?»), or you type
your own twice. Where to keep a copy is up to you. Without this password the backups cannot be opened and it cannot be
recovered; on the computer it is in `secrets.env`, which is not in the backup. On a new computer choose «type your own»
and enter the old one.

**Backup places.** Every snapshot goes to every place chosen:
- a folder on this Mac — `~/Tgbup/backup` by default, another one, or none;
- places tgbup finds: iCloud Drive, Google Drive, Dropbox and OneDrive from `~/Library/CloudStorage`, external disks
  from `/Volumes`;
- Google Drive without its app — through rclone, signing in in the browser (access to the files tgbup created only);
- your own path or restic repository: a full path (`/…` or `~/…`) or a restic address (`sftp:`, `s3:`, `rclone:`…).
  Nothing else is taken — so a password pasted by mistake, say, never becomes a backup place.

In Finder, mark the iCloud folder «Keep Downloaded». Backups can be turned off: the Backups step → «Keep backing up?
n». The snapshots already made stay.

### Where things are

Everything worth looking at is in one folder:
```
~/Tgbup/
  config.yaml       settings: chats, backup places, run times
  archive/          the archive: database, media/, html/, md/
  backup/           the backup on this Mac (if chosen)
  logs/             tgbup.log — every command's log, run.log — runs' output (10 MB each, 5 kept)
```
The Telegram keys, the backup password, the bot token (`secrets.env`) and the Telegram login are apart, in the hidden
`~/.local/state/tgbup/`. `~/Tgbup` gets copied, synced and shared, and with the session anyone can read your messages
as you. An install made before `~/Tgbup` (settings in `~/.config/tgbup`) keeps working where it is.

## What happens next

The schedule starts `tgbup run` at the chosen times. If the computer was asleep, a missed run starts after it wakes up.
A run:
1. downloads new messages and media;
2. redraws the archive pages that changed;
3. snapshots the archive into every backup place — even if the download failed;
4. tells you through the bot: errors right away and with a sound, a summary every 7 days (`notify.summary_every`).

The bot also writes, silently, when a run starts (scheduled, from the page, or the first download) and when it
finishes (what is new, which snapshots were made), and when setup is done (the schedule installed): what is archived,
where backups go, when runs happen. To turn these messages off: `notify: {progress: false}`; errors always come.

To see how things are — `tgbup serve --open` (or `make setup`). The page at http://localhost:8640 opens only on this
computer, and only through the address `tgbup serve` prints (or opens itself with `--open`): it carries a key that
changes on every start, so other programs and other accounts on the computer cannot use the page. It has three tabs:
- **Overview** — the last run, the archive size, backups, schedule and warnings. Until setup is finished, a «Setup is
  not finished» banner at the top says what is missing, with a «Continue setup» button. A run with errors opens all
  its errors and warnings on a click. Each backup place shows when it was last up to date, with a link to its
  snapshots. «Update now» starts what the schedule starts and shows its log (which can be hidden). «Open the archive»
  opens the archive in the browser;
- **Settings** — on first use the steps in order, then all the settings: chats first (with the names of what is
  archived), backups, schedule, notifications; the archive folder and the login below. Restore is here as well. Every step that runs
  another program or keeps secrets (keys, login, archive, backups, schedule, bot) has «How this works»: what exactly
  it set up on this computer, with its real paths, and the commands to check it and change it without tgbup;
- **Snapshots** — every snapshot of every place, newest first, with what changed in it. «Restore a copy» restores a
  snapshot into a folder of its own next to the archive (`<archive>-snapshots/<id>`, with all the files of then; the
  archive itself is not touched), then «Open» opens it in the browser. «Copy command» copies the same command for a
  terminal.

A run started from the page is a process of its own: closing the page does not stop it. Two runs never overlap: if one
is running, the second is skipped.

**Logs.** Every command writes its log to the terminal and to `~/Tgbup/logs/tgbup.log` (the file is turned off with
`log: {file: false}`). The output of scheduled runs and of runs from the page is in `~/Tgbup/logs/run.log`. What
happens on the page (every action, errors, runs, slow requests) is logged too; passwords, codes and tokens typed in are
not.

## Reading the archive

The archive's pages open straight from the disk, no server needed. In the archive folder (`~/Tgbup/archive`):

| What | How to read it |
|---|---|
| `html/index.html` | the chat list and a search over the whole archive. At the top of every page, «archive as of … · snapshot …» — when the archive was last checked and which snapshot holds it; the link leads to the history of changes and snapshots |
| a chat's page | opens on the newest messages at the bottom, as in a messenger; older ones are on the previous pages, 500 messages each. The bar at the top stays in view: search (this chat, or all chats with a tick), the list of pages with their dates, and a calendar — pick a day and you are there. Photos, videos, voice messages, files, link previews, places and polls are right in the feed: a video shows its first frame, GIFs and video stickers play silently while in view, a round video plays on a click |
| `html/media.html` | every photo, video, file, voice message, link, place, contact and poll — with the same previews as in the feed, videos with their length, voice messages playable right in the list and named by who said them. The filters by kind, chat, topic, period and name live in the address and can be bookmarked |
| `md/` | the same pages in Markdown; the archive folder opens as an Obsidian vault |
| `media/` | the files themselves, by year and kind: `media/2025/file/2025-06-14_180233_Lease.pdf`, `media/2025/photo/…` (kinds: photo, video, file, voice, round, audio, sticker, gif) |

Files not downloaded show ⏳ (queued), ⛔ (skipped by the settings) or ⚠️ (could not be downloaded) and why.

**File names.** After the message's date and time comes the file's original name. Photos, voice messages and round
videos have none: then the chat's name, and for voice messages also who said it:
`2025-06-14_180233_Family_Kate.ogg`. Sorting by name is chronological, and a file's modification date is the message's
date. Each file is there once, even if it was forwarded to several chats; which chat and message it came from is known
to the database and the pages. A name is chosen at download and never changes afterwards, so do not rename files in
`media/`: the pages link to these names.

`html/` and `md/` are redrawn from the database and can be deleted. By hand, without Telegram: `tgbup render`
(`--full` redraws everything). After a chat is renamed, its old folder stays in `html/` and `md/`: it can be deleted.

**Sending a chat to someone:** `tgbup export Family -o family.zip` — a zip with the pages and media that opens with a
double click on `index.html`. Name the chat by its title or an unambiguous part of it, `@username` or ID. To narrow it:
`--topic Expenses`, `--from 2026-01 --to 2026-03`, `--no-media`. **The zip is not password-protected yet** — do not
send it over open channels.

## Choosing chats

The Chats step in Settings, or `tgbup setup` in a terminal. Chats are grouped: All chats, Telegram folders, No folder,
Archived. In a forum you can open its topics and untick the ones you do not need; new topics are archived
automatically. Before saving you see what will be written and the resulting config.

Messages are counted only when asked: each chat costs Telegram several requests, and on a long list that is slow. On
the page — «count» on a chat, «count messages of the chats shown», «count messages per topic» in topics and «Count
messages» before saving. In a terminal — the `c` command on a list page, in topics and at the question before saving.
In a terminal:
- lists show 20 a page, page with `>`/`<`/`p N`;
- numbers run through the whole list, `21-23,45` works;
- `a`/`n` tick or untick the page, `A`/`N` the whole list;
- `s` searches by title or `@username`;
- `t N` opens a forum's topics;
- `c` counts the messages on the current page (in topics, per topic);
- `d` shows what will be written and asks whether to write it (`c` counts the messages first);
- commands work in the Russian keyboard layout too.

To check what would be downloaded without downloading anything: `tgbup sync --dry-run`.
```
would archive 2 chats, ~17 062 messages:
── 1. Family · forum · -1005550001234 ─────────────────────────────
   ~4 812 msgs: photos 310 · videos 42 · files 18 · voice 97 · round 12 · links 55
   topics 5 of 7:  General #1 ~1 200 · Recipes #3 ~300 · …
   excluded:       Flood #5 ~2 900 · Memes #12 ~150   (~3 050 msgs not saved)
── 2. Mom · private · 123456789 ───────────────────────────────────
   ~15 300 msgs: photos 1 204 · videos 80 · files 140 · voice 630 · links 210
══ Total: 2 chats, ~17 062 msgs saved · …
```

The config can be edited by hand too; its end has a commented reference of every setting.
```yaml
archive_dir: ~/Tgbup/archive
targets:
  folders: ["Family", "Work"]   # Telegram folders
  include: ["@some_group"]      # plus single chats; numeric IDs are safer: usernames change
  exclude: []
  exclude_topics:               # forum → topic IDs (shown in dry-run as «#5»)
    -1005550001234: [5, 12]
schedule:
  times: ["00:30", "06:30", "12:30", "18:30"]
```
If a chat in `include` is not found, the download ends with an error, so that archiving does not stop silently. If an
excluded topic is brought back, the next download reads the forum's history once and adds what was missed.

## Backups

A snapshot holds a copy of the database (checked with `integrity_check`), `config.yaml`, `media/` and `html/` — the
pages, so that the archive in a snapshot can be read without tgbup. It does not hold `md/` (the same as `html/`), the
Telegram session and `secrets.env`: on a new computer you log in again.

**A snapshot only when the archive changed.** The pages are redrawn on every run, but a snapshot is made only if
something changed since the last snapshot in that place: messages, files, edits, reactions, checklist ticks or
`config.yaml`. A place that already holds the archive as it is, is only checked (is the repository there, and the same
one) and counts as «up to date». A new place, a repository made again, or a place whose last snapshot failed or could
not read some files gets a snapshot on the next run.

**The last snapshot of every day is kept — always**, plus the 4 newest. Every snapshot is complete: the whole archive
restores from any of them, but restic stores only what is new, so a day costs what came in that day. It can be limited
in `config.yaml`, e.g. `backup: {retention: {daily: 90}}` — then beyond 90 days one a week (4 weeks) and one a month
(12 months) stay; `all` keeps every one. The rest is forgotten after a snapshot, the space is freed once a day, and 5%
of the data is read back every 30 days. A snapshot by hand, even without changes: `tgbup backup`.

**History.** At the top of every archive page: «snapshot 3333bbbb of Oct 7, 12:31» — the snapshot that holds what you
see, or «not in a snapshot yet» if the latest changes are not backed up yet. The link leads to `html/history.html`:
every change — when, the snapshot's id, what changed («+120 messages · +5 files · 3 edited» and per chat), and in which
places it is. A snapshot is stored encrypted, not as files, so it cannot be opened in Finder — only restored. For that
each snapshot has its command; the copy goes into `<archive>-snapshots/<id>`, the archive and the settings are not
touched, and the history then shows an «open the restored copy» link instead of the command:

```bash
tgbup restore --copy --from ~/Tgbup/backup --snapshot 3333bbbb
```

On the overview a run shows the snapshot it made or that the archive did not change; the Snapshots tab shows what
changed in each snapshot.

Backups are made by [restic](https://restic.net); Google Drive without its app is reached through
[rclone](https://rclone.org). There is no need to install them: tgbup downloads the versions it needs and checks their
checksums (`tgbup deps` shows where they are). A restic or rclone set in `deps:` in the config, lying next to the
`tgbup` binary or found on `PATH` is used as it is, without that check.

## Restore

On a new computer or after losing the archive:
1. `make setup` (or `tgbup serve --open`), the Telegram keys and the login — as on the first setup.
2. The Archive folder step → «Restore from a backup». In a terminal — answer `y` to «Restore an archive from an
   existing backup», or the separate `tgbup restore` command.
3. Give the backup place and the password, choose a snapshot (the latest by default).
4. The setup then goes on with the usual steps. The chats are already chosen if the config came from the snapshot.

Google Drive through rclone is connected again on a new computer, by signing in in the browser.

What a restore does:
- **the database** — if the archive folder already has `archive.sqlite`, it is renamed to
  `archive.sqlite.before-restore-<time>`, and the restored one takes its place;
- **media** — missing files are added, identical ones stay. If another file lies under the same name, it moves to
  `media.before-restore-<time>`, and the snapshot's file takes its place: the restored database links to it;
- **the config** — taken from the snapshot if the current one has no chats chosen yet; the backup places of both are
  merged. A config with chats chosen is not changed;
- **the password** — saved only if there is none yet. If you already have another password, it stays, and the place
  restored from is not added to the backups: one password must open every place;
- **the pages** are redrawn.

You need free space for the whole snapshot: it is first laid out in a temporary folder next to the archive and then
moved into place by renaming.

`tgbup restore` in a terminal: `--from <place>` (otherwise the first place in the config, or a question), `--snapshot
<id|latest>`, `--to <folder>`, `--list` to only list the snapshots, `--copy` for a copy to look at next to the
archive, which touches neither the archive nor the settings.

<details>
<summary>Without tgbup (on another OS, say)</summary>

1. Install restic. For Google Drive also rclone, and `rclone config` again with the same remote name (`gdrive`).
2. Restore the latest snapshot:
   ```bash
   RESTIC_PASSWORD=… restic -r "<repository>" restore latest --target ~/restore
   ```
   Paths in a snapshot are absolute: `~/restore` gets `…/archive/.snapshot/archive.sqlite`,
   `…/archive/.snapshot/config.yaml`, `…/archive/media/` and `…/archive/html/`.
3. To only read it, open `…/archive/html/index.html`.
4. To go on with the archive: put `archive.sqlite` and `media/` into the archive folder and `config.yaml` into
   `~/Tgbup/`, create `secrets.env` again in `~/.local/state/tgbup/`, then `tgbup login` and `tgbup render`.
</details>

## Security and privacy

**Telegram is read only.** Every Telegram API call goes through an allowlist of methods
(`internal/tgclient/readonly.go`). Anything not on it is refused before it reaches the network: sending, editing,
deleting, forwarding, marking as read, reactions, joining and leaving chats, changing the profile and settings.

| Methods | What for |
|---|---|
| `messages.getDialogs`, `messages.getDialogFilters` | the chat and folder list |
| `messages.getHistory`, `messages.getReplies`, `messages.search`, `messages.getSearchCounters` | messages, their count, pins, counts by kind |
| `messages.getForumTopics` | forum topics |
| `messages.getMessages`, `channels.getMessages` | reread a message for a fresh file reference |
| `channels.getFullChannel` | which group a supergroup grew from |
| `users.getUsers` | your own id, checking the login |
| `upload.getFile` | downloading media |
| `auth.sendCode`, `auth.signIn`, `account.getPassword`, `auth.checkPassword` | login |
| `account.initTakeoutSession`, `account.finishTakeoutSession` | the data export for the first download |
| `help.getConfig`, `help.getNearestDc`, `help.getCdnConfig`, `auth.exportAuthorization`, `auth.importAuthorization` | connecting to Telegram's data centers |

All that shows in the account:
- the login creates a «tgbup» session in the list of active sessions, like any Telegram app. If you end it, tgbup stops
  downloading and tells you;
- Telegram confirms a data export with a request in the app.

History is read without marking it as read; nobody you talk to sees anything.

**The archive on disk.** Backups are encrypted (restic), while the archive itself is plain files: what protects it is
disk encryption — FileVault on a Mac (or an encrypted APFS volume for an external disk), an encrypted volume (LUKS),
an encrypted shared folder or ZFS encryption on a NAS. tgbup checks the disk the archive is on and, if it is not
encrypted, warns on the overview, in Settings and at the end of `tgbup init`. When it cannot tell (another OS, a network folder), it says nothing.

**Files sent to you.** Extensions that run on a double click (`.command`, `.sh`, `.pkg`, `.dmg`…) or in a browser
(`.html`, `.svg`, `.xml`…; a page someone sent could otherwise read the archive's search index) get `.txt` appended,
also on files downloaded before.
Files from other people get macOS's «downloaded from the internet» mark: macOS asks before running such a file. Your
own files (sent by you, not forwarded) come without the mark.

**Secrets.** The keys, the backup password and the bot token are only in `~/.local/state/tgbup/secrets.env` (mode
600), neither in the backups nor in the logs. A password inside a backup address (`rest:https://user:pass@…`) is shown
as `***` in pages, logs and messages. The control page is reachable only from this computer and opens only with the key
from the address `tgbup serve` prints, which it turns into a cookie; its actions also need a token that only its own
pages know. A restored archive is readable by its owner only, and a snapshot's config never chooses the restic or
rclone tgbup runs.

## Commands

| Command | What it does |
|---|---|
| `tgbup init` | step-by-step setup in a terminal; run again to continue or change the settings |
| `tgbup serve --open` | the page on this computer: overview, «Update now», settings with the chat choice, snapshots |
| `tgbup setup` | choosing chats in a terminal |
| `tgbup sync` | download what is new (`--dry-run` only shows it, `--takeout` goes through a data export) |
| `tgbup run` | what the schedule starts: download, backup, notifications (`--takeout` goes through a data export) |
| `tgbup backup` | a snapshot by hand (`--init` creates the repositories) |
| `tgbup restore` | restore the archive from a backup (`--from`, `--snapshot`, `--to`, `--list`; `--copy` for a copy to look at, next to the archive, touching neither the archive nor the settings) |
| `tgbup render` | redraw the pages without Telegram |
| `tgbup export <chat>` | a zip of a chat, a topic or a period |
| `tgbup schedule install\|status\|uninstall` | the launchd schedule (times in `schedule.times` or Settings) |
| `tgbup notify test` | a test message from the bot |
| `tgbup login` | log in to Telegram again |
| `tgbup deps` | where restic and rclone are (downloads them if missing) |

## When something is wrong

| What you see | What to do |
|---|---|
| «the Telegram session is no longer valid» | the session was ended in Telegram: `tgbup init` (or `tgbup login`) |
| «another tgbup run is in progress» | another run is going (scheduled or «Update now»), wait for it to end |
| The first download waits for permission | allow the data export in the Telegram app and press Enter, or `s` to download without the export |
| A chat in `include` is not found | it was renamed or you left it: fix the list in Settings → Chats or `tgbup setup` |
| «a backup place is a full path…» | the backup places hold a line that is neither a path nor an address (a pasted password, say): Settings → Backups → change → Save drops it. The other places are backed up as usual |
| «The disk with the archive is not encrypted» | turn on FileVault (Mac) or move the archive to an encrypted volume |
| No backups to iCloud | macOS asked for access to iCloud during the test run. If it was refused, allow it in System Settings → Privacy & Security → Files and Folders |
| Google Drive through rclone: no access | the Backups step → «Sign in to Google Drive again» |
| The backup password is forgotten | the old snapshots cannot be opened. Set a new password and a new backup place |
| A run marked failed because of one file | a file with attempts left (5 in all) is downloaded again on the next run and does not fail the run; only a file out of attempts counts as an error |
| Details of any error | the overview on the `tgbup serve` page (a run opens on a click), `~/Tgbup/logs/tgbup.log` and `run.log` |

## Development

```bash
make test         # unit tests
make test-backup  # backup tests against a real restic
make lint
```

Telegram's test servers: `TGBUP_TEST_DC=1 bin/tgbup login` creates a separate `session-test` session; the real one is
not touched. Everything else in this mode needs its own `TGBUP_CONFIG`, `TGBUP_SECRETS` and `TGBUP_STATE_DIR` (copy
`session-test` and the API keys there), or tgbup refuses to start. That way test chats never reach the real archive,
and a test backup password never replaces the real one. In this mode the archive and backup folders default to next to
the test config, the log goes there too, and the schedule is not touched at all.

`make e2e` creates a forum with every kind of message on a test account and runs a real `tgbup sync`: a full download,
an incremental one, a dry run, excluding a topic and bringing it back. It then checks the archive and deletes the forum
and the folder it made. Only this fixture can write to Telegram: its code compiles only with the `e2e` tag, works only
on the test servers, and is never in the `tgbup` binary.

## License

[MIT](LICENSE).

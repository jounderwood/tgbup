package main

import (
	"cmp"
	"context"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jounderwood/tgbup/internal/deps"
	"github.com/jounderwood/tgbup/internal/diskcrypt"
	"github.com/jounderwood/tgbup/internal/render"
)

// howTo is what a settings step set up outside tgbup — a program, a file
// with secrets, a session — with this computer's own paths, how to check it
// and how to change it without tgbup.
type howTo struct {
	What   []howLine `json:"what"`
	Check  []howLine `json:"check"`
	Change []howLine `json:"change"`
}

// howLine is a sentence and, when there is one, the command it means.
type howLine struct {
	Text string `json:"text"`
	Cmd  string `json:"cmd,omitempty"`
}

// howItWorks explains each step that uses another program or keeps secrets.
func howItWorks(ctx context.Context, st setup, env Env) map[string]*howTo {
	home := st.paths.Home
	q := func(p string) string { return render.ShellQuote(tildePath(p, home)) }
	secrets := q(st.paths.Secrets)
	secretFile := howLine{Text: "Only you can read the file (-rw-------):", Cmd: "ls -l " + secrets}
	out := map[string]*howTo{}

	out["keys"] = &howTo{
		What: []howLine{{Text: "Your app's api_id and api_hash from my.telegram.org tell Telegram which app connects. They are " +
			"API_ID and API_HASH in " + tildePath(st.paths.Secrets, home) + ", and nowhere else: not in the config, the backups or the logs."}},
		Check: []howLine{secretFile, {Text: "Your apps are at my.telegram.org → API development tools."}},
		Change: []howLine{{Text: "Edit API_ID and API_HASH in the file, or here with «change». Deleting the app at my.telegram.org " +
			"stops new logins with these keys."}},
	}

	session := st.paths.Session(st.testDC)
	out["login"] = &howTo{
		What: []howLine{
			{Text: "tgbup logged in as a Telegram app with your phone number. The session, the key Telegram gave this login, is " +
				tildePath(session, home) + ": with it the archive is read without asking you again."},
			{Text: "Every request goes through a list of read-only methods (internal/tgclient/readonly.go): tgbup cannot send, " +
				"edit, delete or mark anything as read."},
		},
		Check: []howLine{
			{Text: "Telegram → Settings → Devices shows the session as «tgbup»."},
			{Text: "Only you can read the session:", Cmd: "ls -l " + q(session)},
		},
		Change: []howLine{
			{Text: "End the «tgbup» session in Telegram → Settings → Devices: tgbup stops downloading and tells you. Or delete the file:", Cmd: "rm " + q(session)},
			{Text: "Log in again:", Cmd: "tgbup login"},
		},
	}

	archive := st.cfg.ArchiveDir
	disk := diskcrypt.Check(ctx, archive)
	encrypted := map[diskcrypt.State]string{diskcrypt.Encrypted: "encrypted (" + disk.How + ")",
		diskcrypt.Plain: "not encrypted", diskcrypt.Unknown: "of unknown encryption"}[disk.State]
	var checkDisk howLine
	if runtime.GOOS == "darwin" {
		checkDisk = howLine{Text: "The encryption of the disk the archive is on (FileVault: Yes):",
			Cmd: "diskutil info \"$(df " + q(archive) + " | tail -1 | cut -d' ' -f1)\" | grep FileVault"}
	} else {
		checkDisk = howLine{Text: "The volume the archive is on:", Cmd: "findmnt --target " + q(archive)}
	}
	out["archive"] = &howTo{
		What: []howLine{{Text: "Your messages are in " + tildePath(filepath.Join(archive, "archive.sqlite"), home) +
			" (SQLite), the files in media/, the pages in html/ and md/: plain files, readable by any program you run. What protects " +
			"them is the disk's encryption; this disk is " + encrypted + "."}},
		Check: []howLine{{Text: "Only you can open the folder (drwx------):", Cmd: "ls -ld " + q(archive)}, checkDisk},
		Change: []howLine{{Text: "Move the archive with «change»: tgbup moves the files and points the config at them. " +
			"Encrypt the disk: " + cmp.Or(disk.Hint, "FileVault on a Mac, LUKS on Linux") + "."}},
	}

	places, _ := splitPlaces(st)
	finder := deps.NewFinder(st.paths.StateDir, st.cfg.Deps.Restic)
	restic, haveRestic := finder.Find(deps.Restic)
	resticWhat := "restic, downloaded by tgbup when first needed (version " + deps.Restic.Version + ", its SHA-256 checked)"
	if st.cfg.Deps.Restic != "" {
		resticWhat = "your own restic set in deps: in the config, run as it is"
	}
	if haveRestic {
		resticWhat += ", at " + tildePath(restic, home)
	} else {
		restic = "restic"
	}
	bk := &howTo{
		What: []howLine{
			{Text: "Snapshots are made by " + resticWhat + ", encrypted with your backup password, into: " +
				strings.Join(placeNames(places, home), ", ") + "."},
			{Text: "The password is RESTIC_PASSWORD in " + tildePath(st.paths.Secrets, home) + ". Nothing else opens the " +
				"snapshots, and the file is not in them: keep a copy of the password yourself."},
		},
		Check:  []howLine{secretFile},
		Change: nil,
	}
	pw := "RESTIC_PASSWORD=\"$(sed -n 's/^RESTIC_PASSWORD=//p' " + secrets + ")\" "
	for _, p := range places {
		prefix := pw
		if strings.HasPrefix(p, "rclone:") {
			rclone, ok := deps.NewFinder(st.paths.StateDir, st.cfg.Deps.Rclone).Find(deps.Rclone)
			if ok {
				prefix += "PATH=" + q(filepath.Dir(rclone)) + ":\"$PATH\" "
			}
		}
		bk.Check = append(bk.Check, howLine{Text: "The snapshots in " + placeName(p, home) + ":",
			Cmd: prefix + render.ShellQuote(restic) + " -r " + render.ShellQuote(placeName(p, home)) + " snapshots --tag tgbup"})
	}
	if len(places) > 0 {
		bk.Check = append(bk.Check, howLine{Text: "That the data reads back (a part of it):",
			Cmd: pw + render.ShellQuote(restic) + " -r " + render.ShellQuote(placeName(places[0], home)) + " check --read-data-subset=5%"})
	}
	if slicesHasPrefix(places, "rclone:") {
		rclone, _ := deps.NewFinder(st.paths.StateDir, st.cfg.Deps.Rclone).Find(deps.Rclone)
		bk.What = append(bk.What, howLine{Text: "Google Drive is reached through rclone (version " + deps.Rclone.Version +
			", downloaded the same way) with the access you gave it in the browser; rclone keeps that in its own config file."})
		bk.Check = append(bk.Check, howLine{Text: "Where rclone keeps the Google access:", Cmd: render.ShellQuote(cmp.Or(rclone, "rclone")) + " config file"})
	}
	bk.Check = append(bk.Check, howLine{Text: "Which restic and rclone tgbup uses:", Cmd: "tgbup deps"})
	bk.Change = []howLine{
		{Text: "Your own restic or rclone: deps: {restic: /path/to/restic} in " + tildePath(st.paths.Config, home) + "."},
		{Text: "How many snapshots are kept: backup: {retention: …} there; places and the password: «change» here."},
		{Text: "Stop backups with «change» → off; the snapshots already made stay where they are."},
	}
	out["backup"] = bk

	j, err := newLaunchdJob(st, env)
	if err == nil && runtime.GOOS == "darwin" && !st.testDC {
		target := "gui/$(id -u)/" + j.Label
		out["schedule"] = &howTo{
			What: []howLine{{Text: "A launchd agent, " + j.Label + ", in " + tildePath(j.path(), home) + " starts " +
				tildePath(j.Exe, home) + " run at " + clockList(j.Times) + "; a run missed while the Mac slept starts when it wakes. " +
				"Its output goes to " + tildePath(j.LogPath, home) + "."}},
			Check: []howLine{
				{Text: "The agent as macOS sees it (state, last exit code):", Cmd: "launchctl print " + target},
				{Text: "Its file:", Cmd: "cat " + q(j.path())},
				{Text: "The last runs' output:", Cmd: "tail -n 50 " + q(j.LogPath)},
			},
			Change: []howLine{
				{Text: "Turn it off (here: «change» → off):", Cmd: "launchctl bootout " + target},
				{Text: "Or: System Settings → General → Login Items & Extensions → Allow in the Background → tgbup."},
				{Text: "The times are schedule: {times: […]} in " + tildePath(st.paths.Config, home) + "; «change» here installs them."},
			},
		}
	}

	out["bot"] = &howTo{
		What: []howLine{
			{Text: "Alerts go through Telegram's Bot API (api.telegram.org) from your own bot. Its token and your chat id are " +
				"BOT_TOKEN and BOT_CHAT_ID in " + tildePath(st.paths.Secrets, home) + "."},
			{Text: "The bot can only write to you: it has no access to your account or chats. It sends errors at once and, " +
				"quietly, when a run starts and ends (notify: {progress: …}), and a summary every week."},
		},
		Check: []howLine{secretFile, {Text: "A test message:", Cmd: "tgbup notify test"}},
		Change: []howLine{
			{Text: "Revoke the token in Telegram: @BotFather → /mybots → your bot → API Token → Revoke; alerts stop until a new one is set here."},
			{Text: "Fewer messages: notify: {progress: false} in " + tildePath(st.paths.Config, home) + "; errors always come."},
		},
	}

	out["download"] = &howTo{
		What: []howLine{{Text: "The first download used Telegram's data export (takeout), which Telegram allows for whole histories " +
			"after you confirm it in the app; later runs read only what is new, as an ordinary app does."}},
		Check: []howLine{{Text: "Telegram sent a service message «Data export request» when it was asked for."}},
	}
	return out
}

func placeNames(repos []string, home string) []string {
	out := make([]string, len(repos))
	for i, r := range repos {
		out[i] = placeName(r, home)
	}
	if len(out) == 0 {
		return []string{"no place yet"}
	}
	return out
}

func slicesHasPrefix(list []string, prefix string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

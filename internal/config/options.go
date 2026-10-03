package config

import (
	"fmt"
	"strings"
	"time"
)

// OptionsMarker starts the commented reference of all options that setup
// appends to the config once.
const OptionsMarker = "── All options"

// optionsReference documents every option with its default, as YAML
// comments, so the config file shows what can be changed.
func optionsReference() string {
	d := Default()
	r := d.Backup.Retention
	rows := [][2]string{
		{"archive_dir: " + d.ArchiveDir, "where the archive is kept"},
		{"targets:", ""},
		{`  folders: ["Family"]`, "every chat in these Telegram folders"},
		{`  include: [-1001234567890, "@username"]`, "plus these chats (ids are safer: usernames change)"},
		{"  exclude: []", "never these chats"},
		{"  exclude_topics:", "forum id -> topics not to archive (#N in sync --dry-run)"},
		{"    -1001234567890: [5, 12]", ""},
		{"media:", ""},
		{"  max_file_size: 0", "skip larger files, e.g. 500MB; 0 = no limit"},
		{"  skip_kinds: []", strings.Join(MediaKinds, ", ")},
		{fmt.Sprintf("  workers: %d", d.Media.Workers), "parallel downloads, 1-16"},
		{"sync:", ""},
		{"  run_time_budget: " + shortDuration(d.Sync.RunTimeBudget.D()), "stop reading old history after this per run"},
		{"  max_flood_wait: " + shortDuration(d.Sync.MaxFloodWait.D()), "wait out Telegram flood limits up to this, else defer the chat"},
		{"backup:", "restic snapshots after every run; tgbup init sets this up"},
		{`  repositories: ["~/Tgbup/backup", "rclone:gdrive:TelegramBackup"]`, "every run snapshots into each"},
		{fmt.Sprintf("  retention: {last: %d, daily: %d, weekly: %d, monthly: %d}", r.Last, r.Daily, r.Weekly, r.Monthly), ""},
		{"  prune_every: " + shortDuration(d.Backup.PruneEvery.D()), ""},
		{"  check_every: " + shortDuration(d.Backup.CheckEvery.D()), ""},
		{"  check_read_subset: " + d.Backup.CheckReadSubset, ""},
		{"deps:", "own restic/rclone instead of the ones tgbup downloads"},
		{`  restic: ""`, ""},
		{`  rclone: ""`, ""},
		{"notify:", "bot: errors right away, a summary every summary_every"},
		{"  summary_every: " + shortDuration(d.Notify.SummaryEvery.D()), ""},
	}
	width := 0
	for _, row := range rows {
		if row[1] != "" {
			width = max(width, len(row[0]))
		}
	}
	var b strings.Builder
	b.WriteString("# " + OptionsMarker + " (defaults shown; set them in the keys above) ──\n")
	for _, row := range rows {
		line := "# " + row[0]
		if row[1] != "" {
			line = fmt.Sprintf("# %-*s  # %s", width, row[0], row[1])
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// shortDuration writes 2h instead of time's 2h0m0s.
func shortDuration(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}

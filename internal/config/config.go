package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	ArchiveDir string  `yaml:"archive_dir"`
	Targets    Targets `yaml:"targets"`
	Media      Media   `yaml:"media"`
	Sync       Sync    `yaml:"sync"`
	Backup     Backup  `yaml:"backup"`
	Deps       Deps    `yaml:"deps"`
	Notify     Notify  `yaml:"notify"`
}

// Deps points at restic and rclone binaries to use instead of the ones tgbup downloads.
type Deps struct {
	Restic string `yaml:"restic"`
	Rclone string `yaml:"rclone"`
}

type Targets struct {
	Folders []string  `yaml:"folders"`
	Include []PeerRef `yaml:"include"`
	Exclude []PeerRef `yaml:"exclude"`
	// ExcludeTopics maps a forum's chat id to topics whose messages are not archived.
	ExcludeTopics map[int64][]int `yaml:"exclude_topics"`
}

type Media struct {
	MaxFileSize ByteSize `yaml:"max_file_size"`
	SkipKinds   []string `yaml:"skip_kinds"`
	Workers     int      `yaml:"workers"`
}

type Sync struct {
	RunTimeBudget Duration `yaml:"run_time_budget"`
	MaxFloodWait  Duration `yaml:"max_flood_wait"`
}

type Backup struct {
	Repositories    []string  `yaml:"repositories"`
	Retention       Retention `yaml:"retention"`
	PruneEvery      Duration  `yaml:"prune_every"`
	CheckEvery      Duration  `yaml:"check_every"`
	CheckReadSubset string    `yaml:"check_read_subset"`
}

type Retention struct {
	Last    int `yaml:"last"`
	Daily   int `yaml:"daily"`
	Weekly  int `yaml:"weekly"`
	Monthly int `yaml:"monthly"`
}

type Notify struct {
	SummaryEvery Duration `yaml:"summary_every"`
}

var MediaKinds = []string{"photo", "video", "video_note", "voice", "audio", "document", "sticker", "animation"}

func Default() Config {
	return Config{
		ArchiveDir: "~/TelegramArchive",
		Media:      Media{Workers: 4},
		Sync:       Sync{RunTimeBudget: Duration(2 * time.Hour), MaxFloodWait: Duration(5 * time.Minute)},
		Backup: Backup{
			Retention:       Retention{Last: 4, Daily: 7, Weekly: 4, Monthly: 12},
			PruneEvery:      Duration(24 * time.Hour),
			CheckEvery:      Duration(30 * 24 * time.Hour),
			CheckReadSubset: "5%",
		},
		Notify: Notify{SummaryEvery: Duration(7 * 24 * time.Hour)},
	}
}

func Load(path, home string) (Config, error) { return load(path, home, true) }

// LoadWithoutTargets accepts a config whose targets are not chosen yet, for
// the commands that come before choosing them (login, setup).
func LoadWithoutTargets(path, home string) (Config, error) { return load(path, home, false) }

func load(path, home string, requireTargets bool) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	return parse(path, data, home, requireTargets)
}

func parse(path string, data []byte, home string, requireTargets bool) (Config, error) {
	cfg := Default()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.ArchiveDir = expandHome(cfg.ArchiveDir, home)
	for i, r := range cfg.Backup.Repositories {
		cfg.Backup.Repositories[i] = expandHome(r, home)
	}
	cfg.Deps.Restic, cfg.Deps.Rclone = expandHome(cfg.Deps.Restic, home), expandHome(cfg.Deps.Rclone, home)
	if err := cfg.validate(requireTargets); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

func (c Config) validate(requireTargets bool) error {
	var errs []error
	if c.ArchiveDir == "" {
		errs = append(errs, errors.New("archive_dir is empty"))
	}
	if requireTargets && len(c.Targets.Folders) == 0 && len(c.Targets.Include) == 0 {
		errs = append(errs, errors.New("no chats chosen yet: run tgbup init (or tgbup setup)"))
	}
	for _, r := range append(slices.Clone(c.Targets.Include), c.Targets.Exclude...) {
		_, isUser := r.Username()
		_, isID := r.ID()
		if !isUser && !isID {
			errs = append(errs, fmt.Errorf("targets: invalid peer %q (want @username or numeric id)", string(r)))
		}
	}
	for chat, topics := range c.Targets.ExcludeTopics {
		for _, id := range topics {
			if id < 1 {
				errs = append(errs, fmt.Errorf("targets: exclude_topics %d: invalid topic id %d", chat, id))
			}
		}
	}
	if c.Media.Workers < 1 || c.Media.Workers > 16 {
		errs = append(errs, fmt.Errorf("media.workers must be 1..16, got %d", c.Media.Workers))
	}
	for _, k := range c.Media.SkipKinds {
		if !slices.Contains(MediaKinds, k) {
			errs = append(errs, fmt.Errorf("media.skip_kinds: unknown kind %q (known: %s)", k, strings.Join(MediaKinds, ", ")))
		}
	}
	return errors.Join(errs...)
}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

type ByteSize int64

func (b *ByteSize) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseByteSize(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*b = ByteSize(v)
	return nil
}

func ParseByteSize(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	units := []struct {
		suffix string
		mult   float64
	}{{"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}}
	for _, u := range units {
		if num, ok := strings.CutSuffix(s, u.suffix); ok {
			n, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
			if err != nil || n < 0 {
				return 0, fmt.Errorf("invalid size %q", s)
			}
			return int64(n * u.mult), nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n, nil
}

type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = Duration(v)
	return nil
}

func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return v, nil
}

type PeerRef string

func (p *PeerRef) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: peer must be @username or numeric id", n.Line)
	}
	*p = PeerRef(n.Value)
	return nil
}

func (p PeerRef) Username() (string, bool) {
	s := string(p)
	if len(s) > 1 && s[0] == '@' {
		return strings.ToLower(s[1:]), true
	}
	return "", false
}

func (p PeerRef) ID() (int64, bool) {
	n, err := strconv.ParseInt(string(p), 10, 64)
	return n, err == nil
}

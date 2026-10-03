package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// ErrBadAPIID is returned for a TG_API_ID that is not a number.
var ErrBadAPIID = errors.New("TG_API_ID must be a number")

type Secrets struct {
	APIID            int
	APIHash          string
	BotToken         string
	BotChatID        string
	ResticPassword   string
	RcloneConfigPass string
}

// LoadSecrets resolves each key as: env KEY, then file named by env KEY_FILE, then the secrets file.
func LoadSecrets(path string, getenv func(string) string) (Secrets, error) {
	file, err := readEnvFile(path)
	if err != nil {
		return Secrets{}, err
	}
	var errs []error
	get := func(key string) string {
		if v := getenv(key); v != "" {
			return v
		}
		if f := getenv(key + "_FILE"); f != "" {
			b, err := os.ReadFile(f)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s_FILE: %w", key, err))
				return ""
			}
			return strings.TrimSpace(string(b))
		}
		return file[key]
	}
	s := Secrets{
		APIHash:          get("TG_API_HASH"),
		BotToken:         get("BOT_TOKEN"),
		BotChatID:        get("BOT_CHAT_ID"),
		ResticPassword:   get("RESTIC_PASSWORD"),
		RcloneConfigPass: get("RCLONE_CONFIG_PASS"),
	}
	if v := get("TG_API_ID"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, ErrBadAPIID)
		}
		s.APIID = n
	}
	return s, errors.Join(errs...)
}

// SecretRoundTrips reports whether v reads back unchanged from the secrets file.
func SecretRoundTrips(v string) bool { return strings.Trim(strings.TrimSpace(v), `"'`) == v }

func (s Secrets) RequireTelegram() error {
	if s.APIID == 0 || s.APIHash == "" {
		return errors.New("no Telegram API keys yet: run tgbup init (or put TG_API_ID and TG_API_HASH from https://my.telegram.org into secrets.env)")
	}
	return nil
}

func readEnvFile(path string) (map[string]string, error) {
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is readable by group/others: chmod 600 it", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, i+1)
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out, nil
}

// SetSecrets writes the given keys into the secrets file, replacing their
// lines or appending them, and keeps every other line and comment. The file
// stays private (0600).
func SetSecrets(path string, values map[string]string) error {
	for k, v := range values {
		if strings.ContainsAny(k+v, "\r\n") || strings.Contains(k, "=") || k == "" {
			return fmt.Errorf("secret %s: invalid key or value", k)
		}
		// The file format trims these, so the value read back would differ.
		if !SecretRoundTrips(v) {
			return fmt.Errorf("secret %s: must not start or end with a space or a quote", k)
		}
	}
	if _, err := readEnvFile(path); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(data) == 0 {
		lines = nil
	}
	done := map[string]bool{}
	for i, line := range lines {
		k, _, ok := strings.Cut(strings.TrimSpace(line), "=")
		k = strings.TrimSpace(k)
		if v, set := values[k]; ok && set {
			lines[i] = k + "=" + v
			done[k] = true
		}
	}
	for _, k := range slices.Sorted(maps.Keys(values)) {
		if !done[k] {
			lines = append(lines, k+"="+values[k])
		}
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	return writePrivate(path, []byte(strings.Join(lines, "\n")+"\n"))
}

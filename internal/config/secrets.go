package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

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
			errs = append(errs, errors.New("TG_API_ID must be a number"))
		}
		s.APIID = n
	}
	return s, errors.Join(errs...)
}

func (s Secrets) RequireTelegram() error {
	if s.APIID == 0 || s.APIHash == "" {
		return errors.New("TG_API_ID and TG_API_HASH are required (create an app at https://my.telegram.org)")
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

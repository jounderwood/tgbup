package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// IntegrityCheck runs SQLite's integrity check: a damaged archive must not be backed up over good snapshots.
func (s *Store) IntegrityCheck(ctx context.Context) error {
	var res string
	if err := s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&res); err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("archive.sqlite integrity check: %s", res)
	}
	return nil
}

// VacuumInto writes a consistent copy of the archive to path, replacing an older copy.
func (s *Store) VacuumInto(ctx context.Context, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}

type Run struct {
	Started, Finished time.Time
	OK                bool
	Summary           string // JSON
}

func (s *Store) AddRun(ctx context.Context, r Run) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO runs (started_at, finished_at, ok, summary_json) VALUES (?, ?, ?, ?)`,
		r.Started.Unix(), r.Finished.Unix(), r.OK, r.Summary)
	return err
}

// RunsSince returns runs started at or after t, oldest first.
func (s *Store) RunsSince(ctx context.Context, t time.Time) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT started_at, finished_at, ok, summary_json FROM runs WHERE started_at >= ? ORDER BY started_at`, t.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var (
			r      Run
			st, fi int64
		)
		if err := rows.Scan(&st, &fi, &r.OK, &r.Summary); err != nil {
			return nil, err
		}
		r.Started, r.Finished = time.Unix(st, 0), time.Unix(fi, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestIntegrityAndVacuumInto(t *testing.T) {
	s := renderFixture(t)
	ctx := context.Background()
	if err := s.IntegrityCheck(ctx); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copy", "archive.sqlite")
	if err := s.VacuumInto(ctx, dst); err != nil {
		t.Fatal(err)
	}
	// A second copy over the first must work too: VACUUM INTO refuses existing files.
	if err := s.VacuumInto(ctx, dst); err != nil {
		t.Fatalf("second copy: %v", err)
	}
	c, err := Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if n, _ := c.CountMessages(ctx, -1001); n != 3 {
		t.Fatalf("copy has %d messages, want 3", n)
	}
}

func TestRuns(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	t0 := time.Unix(1790000000, 0)
	for i, ok := range []bool{true, false, true} {
		r := Run{Started: t0.Add(time.Duration(i) * time.Hour), Finished: t0.Add(time.Duration(i)*time.Hour + time.Minute), OK: ok, Summary: `{"n":` + string(rune('0'+i)) + `}`}
		if err := s.AddRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.RunsSince(ctx, t0.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].OK || !got[1].OK || got[1].Summary != `{"n":2}` || !got[0].Started.Equal(t0.Add(time.Hour)) {
		t.Fatalf("runs = %+v", got)
	}
}

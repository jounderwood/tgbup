package main

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLockIsExclusiveAndReleasable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "run.lock")
	release, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLock(path); !errors.Is(err, errLocked) {
		t.Fatalf("second lock err = %v, want errLocked", err)
	}
	release()
	release2, err := acquireLock(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	release2()
}

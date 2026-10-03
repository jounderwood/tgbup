package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var errLocked = errors.New("another tgbup run is in progress")

// lockPatience is how long acquireLock waits for a lock held only for an
// instant, such as tgbup serve checking whether a run is in progress.
var lockPatience = 2 * time.Second

func acquireLock(path string) (func(), error) {
	deadline := time.Now().Add(lockPatience)
	for {
		release, err := tryLock(path)
		if !errors.Is(err, errLocked) || time.Now().After(deadline) {
			return release, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func tryLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

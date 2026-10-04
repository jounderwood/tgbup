// Package diskcrypt tells whether the disk that holds a folder is encrypted
// at rest: FileVault on macOS; LUKS/dm-crypt, eCryptfs (Synology encrypted
// shared folders) or ZFS encryption on Linux. Anything it cannot tell is
// Unknown, never an error: the check only decides whether to warn.
package diskcrypt

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
)

type State int

const (
	Unknown State = iota
	Encrypted
	Plain
)

func (s State) String() string {
	return [...]string{"unknown", "encrypted", "not encrypted"}[s]
}

type Result struct {
	State State
	// How names what was checked, e.g. "FileVault".
	How string
	// Hint says how to encrypt it, for Plain.
	Hint string
}

// run is the command runner; tests replace it.
var run = func(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// Check looks at the disk holding dir.
func Check(ctx context.Context, dir string) Result {
	return check(ctx, runtime.GOOS, dir)
}

func check(ctx context.Context, goos, dir string) Result {
	switch goos {
	case "darwin":
		return checkMac(ctx)
	case "linux":
		return checkLinux(ctx, dir)
	}
	return Result{}
}

func checkMac(ctx context.Context) Result {
	r := Result{How: "FileVault", Hint: "turn on FileVault: System Settings → Privacy & Security → FileVault"}
	out, err := run(ctx, "fdesetup", "status")
	switch {
	case err != nil:
	case strings.Contains(out, "FileVault is On"):
		r.State = Encrypted
	case strings.Contains(out, "FileVault is Off"):
		r.State = Plain
	}
	return r
}

func checkLinux(ctx context.Context, dir string) Result {
	r := Result{How: "the volume", Hint: "keep the archive on an encrypted volume (LUKS) or an encrypted shared folder of the NAS"}
	out, err := run(ctx, "findmnt", "-n", "-o", "SOURCE,FSTYPE", "--target", dir)
	f := strings.Fields(out)
	if err != nil || len(f) < 2 {
		return r
	}
	src, fstype := f[0], f[1]
	if i := strings.IndexByte(src, '['); i > 0 {
		src = src[:i] // btrfs subvolume: /dev/sda1[/@home]
	}
	switch {
	case fstype == "ecryptfs":
		r.State, r.How = Encrypted, "eCryptfs"
	case fstype == "zfs":
		r.How = "ZFS encryption"
		if v, err := run(ctx, "zfs", "get", "-H", "-o", "value", "encryption", src); err == nil {
			if strings.TrimSpace(v) == "off" {
				r.State = Plain
			} else {
				r.State = Encrypted
			}
		}
	case strings.HasPrefix(src, "/dev/"):
		r.How = "LUKS / dm-crypt"
		// -s lists the device and what it is built on: a crypt layer anywhere means encrypted.
		types, err := run(ctx, "lsblk", "-s", "-n", "-o", "TYPE", src)
		if err != nil {
			return r
		}
		r.State = Plain
		for _, t := range strings.Fields(types) {
			if t == "crypt" {
				r.State = Encrypted
			}
		}
	}
	return r
}

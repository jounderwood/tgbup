package diskcrypt

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func fake(t *testing.T, outputs map[string]string) {
	t.Helper()
	old := run
	t.Cleanup(func() { run = old })
	run = func(_ context.Context, name string, args ...string) (string, error) {
		out, ok := outputs[name]
		if !ok {
			return "", errors.New(name + ": not found")
		}
		return out, nil
	}
}

func TestCheck(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name    string
		goos    string
		outputs map[string]string
		want    State
	}{
		{"FileVault on", "darwin", map[string]string{"df": dfData, "diskutil": "   Volume Name: Data\n   Device Location: Internal\n   FileVault:                 Yes\n"}, Encrypted},
		{"FileVault off", "darwin", map[string]string{"df": dfData, "diskutil": "   Device Location: Internal\n   FileVault:                 No\n"}, Plain},
		{"external disk, plain, FileVault on", "darwin", map[string]string{"df": dfExternal, "diskutil": "   Device Location: External\n   FileVault:                 No\n"}, Plain},
		{"external disk, encrypted", "darwin", map[string]string{"df": dfExternal, "diskutil": "   Device Location: External\n   FileVault:                 Yes\n"}, Encrypted},
		{"network share", "darwin", map[string]string{"df": "Filesystem 512-blocks Used Available Capacity Mounted on\n//me@nas/share 1 1 1 1% /Volumes/share\n"}, Unknown},
		{"no df", "darwin", nil, Unknown},
		{"LUKS", "linux", map[string]string{"findmnt": "/dev/mapper/data ext4\n", "lsblk": "crypt\npart\ndisk\n"}, Encrypted},
		{"plain partition", "linux", map[string]string{"findmnt": "/dev/sda1[/@home] btrfs\n", "lsblk": "part\ndisk\n"}, Plain},
		{"Synology encrypted folder", "linux", map[string]string{"findmnt": "/volume1/@Archive@ ecryptfs\n"}, Encrypted},
		{"ZFS encrypted", "linux", map[string]string{"findmnt": "tank/archive zfs\n", "zfs": "aes-256-gcm\n"}, Encrypted},
		{"ZFS plain", "linux", map[string]string{"findmnt": "tank/archive zfs\n", "zfs": "off\n"}, Plain},
		{"network share", "linux", map[string]string{"findmnt": "nas:/export nfs4\n"}, Unknown},
		{"no findmnt", "linux", nil, Unknown},
		{"other OS", "windows", map[string]string{"df": dfData, "diskutil": "FileVault: No"}, Unknown},
	} {
		fake(t, c.outputs)
		r := check(ctx, c.goos, "/archive")
		if r.State != c.want {
			t.Errorf("%s: %v, want %v", c.name, r.State, c.want)
		}
		if r.State == Plain && !strings.Contains(r.Hint, "encrypt") && !strings.Contains(r.Hint, "FileVault") {
			t.Errorf("%s: no hint: %+v", c.name, r)
		}
	}
}

const (
	dfData     = "Filesystem 512-blocks Used Available Capacity Mounted on\n/dev/disk3s5 965595304 668414464 216144544 76% /System/Volumes/Data\n"
	dfExternal = "Filesystem 512-blocks Used Available Capacity Mounted on\n/dev/disk6s1 1000 10 990 1% /Volumes/My Disk\n"
)

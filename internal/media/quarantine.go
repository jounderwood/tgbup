package media

import (
	"fmt"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

// quarantine marks a file someone else sent the way a browser marks a
// download: macOS asks before running it from Finder.
func quarantine(path string, now time.Time) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	v := fmt.Sprintf("0081;%08x;tgbup;", now.Unix())
	return unix.Setxattr(path, "com.apple.quarantine", []byte(v), 0)
}

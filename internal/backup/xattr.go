package backup

import "golang.org/x/sys/unix"

// copyQuarantine carries macOS's downloaded-file mark over a copy between
// volumes; a file without it, or a volume that cannot hold it, is left as is.
func copyQuarantine(from, to string) {
	const name = "com.apple.quarantine"
	buf := make([]byte, 256)
	n, err := unix.Getxattr(from, name, buf)
	if err != nil {
		return
	}
	_ = unix.Setxattr(to, name, buf[:n], 0)
}

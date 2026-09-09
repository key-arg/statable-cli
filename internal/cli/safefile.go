package cli

import (
	"fmt"
	"io"
	"os"
)

// writeNewFile writes a file that must not already exist.
//
// os.WriteFile opens with O_CREATE|O_TRUNC, which follows a symlink at the
// target path. The scratch path this program writes to is predictable — a
// fixed name plus the process id — so on a shared directory someone can plant
// symlinks across a range of ids and have the next run overwrite a file of
// their choosing, with this user's permissions. O_EXCL refuses to follow a
// link and refuses an existing file, which turns that into a failed write.
func writeNewFile(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

// readSmallFile reads at most limit bytes from a regular file.
//
// Two things it refuses. Anything that is not a regular file: a symlink to
// /dev/zero reports a size of zero and then yields bytes forever, so
// os.ReadFile on it grows its buffer until the process is killed. And
// anything longer than the limit, because the point of the file is to be
// small and the cost of being wrong about that is unbounded memory.
//
// The check is on the open descriptor, not on the path, so nothing can be
// swapped underneath between the test and the read. O_NOFOLLOW is not used:
// it is not portable, and following a symlink to a regular file is harmless
// here — the content still has to pass the fingerprint check.
func readSmallFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return io.ReadAll(io.LimitReader(f, limit))
}

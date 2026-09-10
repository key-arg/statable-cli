package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/key-arg/statable-cli/internal/auth"
)

// TestWriteNewFileRefusesASymlink: the scratch path this program writes to is
// predictable — a fixed name plus the process id — so on a directory anyone
// can write, someone can plant symlinks across a range of ids and have the
// next run overwrite a file of their choosing with this user's permissions.
// os.WriteFile opens with O_CREATE|O_TRUNC and follows the link; O_EXCL does
// not.
func TestWriteNewFileRefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "IMPORTANT")
	const keep = "data someone else owns"
	if err := os.WriteFile(victim, []byte(keep), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "scratch.tmp")
	if err := os.Symlink(victim, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := writeNewFile(link, []byte("attacker-controlled")); err == nil {
		t.Fatal("writing through a symlink must fail")
	}
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != keep {
		t.Fatalf("the victim file was overwritten: %q", got)
	}
}

// TestWriteNewFileRefusesAnExistingFile is the same property stated directly:
// the scratch path is meant to be new every time.
func TestWriteNewFileRefusesAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scratch.tmp")
	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeNewFile(path, []byte("second")); err == nil {
		t.Fatal("an existing file must not be truncated")
	}
}

// TestWriteNewFileIsPrivate: the file may hold a listing of every property the
// key can read.
func TestWriteNewFileIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.tmp")
	if err := writeNewFile(path, []byte("x")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// See auth.FileModeIsEnforced: Windows does not honour the bits.
	if perm := fi.Mode().Perm(); auth.FileModeIsEnforced() && perm != 0o600 {
		t.Fatalf("mode = %o, want 600", perm)
	}
}

// TestReadSmallFileRefusesWhatIsNotAFile is the fix for a hang, not for a
// wrong answer. A symlink to /dev/zero reports a size of zero and then yields
// bytes forever, so os.ReadFile on it grows its buffer until the process is
// killed. A cache must never be able to hang the command it exists to speed
// up, so anything that is not a regular file is refused before a byte is read.
func TestReadSmallFileRefusesWhatIsNotAFile(t *testing.T) {
	if _, err := os.Stat("/dev/zero"); err != nil {
		t.Skip("no /dev/zero on this platform")
	}
	link := filepath.Join(t.TempDir(), "sites-cache.json")
	if err := os.Symlink("/dev/zero", link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := readSmallFile(link, 4<<20); err == nil {
			t.Error("a character device must not be read as a cache file")
		}
	}()
	select {
	case <-done:
	case <-timeoutAfter():
		t.Fatal("reading an endless file did not return; this is the hang the check exists to prevent")
	}
}

// TestReadSmallFileStopsAtTheLimit: even a regular file may have been replaced
// with a large one.
func TestReadSmallFileStopsAtTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 5000)), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := readSmallFile(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 100 {
		t.Fatalf("read %d bytes, want at most 100", len(b))
	}
}

// TestUntrustedConfigDirIsRecognised: the temp-dir fallback is world-writable
// territory, and a site cache read from there is a mapping from a site name to
// an id someone else chose.
func TestUntrustedConfigDirIsRecognised(t *testing.T) {
	fallback := filepath.Join(os.TempDir(), noHomeDirName)
	if !isUntrustedConfigDir(fallback) {
		t.Fatalf("%s must be treated as untrusted", fallback)
	}
	if !isUntrustedConfigDir(fallback + "/") {
		t.Fatal("a trailing separator must not disguise the fallback")
	}
	if isUntrustedConfigDir(t.TempDir()) {
		t.Fatal("a private directory must not be treated as the fallback")
	}
}

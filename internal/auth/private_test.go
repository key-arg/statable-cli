package auth

import (
	"strings"
	"testing"
)

// TestPlaintextStorageIsNeverSilentlyUnprotected.
//
// This package's rule is that credentials never degrade silently. Windows maps
// a file mode onto the read-only attribute and nothing else, so a credentials
// file created with 0600 is readable by every account on the machine. Writing
// it and saying nothing is exactly the degradation the rule forbids, and it
// was invisible until CI ran the tests on Windows.
func TestPlaintextStorageIsNeverSilentlyUnprotected(t *testing.T) {
	const path = "C:/Users/x/.config/statable/credentials.json"

	if !fileModeIsEnforced("linux") || !fileModeIsEnforced("darwin") {
		t.Fatal("Unix platforms do enforce the mode")
	}
	if fileModeIsEnforced("windows") {
		t.Fatal("Windows does not enforce the mode; claiming it does is the bug")
	}

	// Where the mode holds, there is nothing to say.
	for _, goos := range []string{"linux", "darwin", "freebsd"} {
		if w := plaintextWarning(goos, path); w != "" {
			t.Errorf("%s enforces the mode but warns anyway: %q", goos, w)
		}
	}

	w := plaintextWarning("windows", path)
	if w == "" {
		t.Fatal("Windows must be told the file is not protected")
	}
	// The warning has to name the file and the way out, or it is noise.
	if !strings.Contains(w, path) {
		t.Errorf("the warning does not name the file: %q", w)
	}
	if !strings.Contains(w, "Credential Manager") {
		t.Errorf("the warning does not name what does protect the key: %q", w)
	}
}

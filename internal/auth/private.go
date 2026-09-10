package auth

import "runtime"

// FileModeIsEnforced reports whether this platform honours the Unix permission
// bits the credential file is written with.
//
// Windows does not. Go's os package maps a file mode onto the read-only
// attribute and nothing else, so a file created with 0600 reports 0666 and is
// readable by every account on the machine. Restricting it properly means
// writing an ACL through the Windows API, which this package does not do.
//
// The consequence is not hidden from the user: Save says so before writing.
// This function exists so that promise and the tests that check it read from
// one definition rather than two.
func FileModeIsEnforced() bool { return fileModeIsEnforced(runtime.GOOS) }

// fileModeIsEnforced is the rule as a pure function of the platform, so both
// branches can be tested from either one. The same shape execctx uses: detect
// the environment once, decide with functions that take it as an argument.
func fileModeIsEnforced(goos string) bool { return goos != "windows" }

// PlaintextWarning is what a person is told before a key is written somewhere
// the operating system will not protect. Empty when the platform does enforce
// file modes.
func PlaintextWarning(path string) string { return plaintextWarning(runtime.GOOS, path) }

func plaintextWarning(goos, path string) string {
	if fileModeIsEnforced(goos) {
		return ""
	}
	return "Windows does not apply Unix file permissions, so " + path +
		" is readable by any account on this machine. The Windows Credential" +
		" Manager does protect it: run `statable auth login` without" +
		" --insecure-storage to use it."
}

package cli

import (
	"strings"
	"testing"
)

// TestBuildInfoAlwaysNamesSomething: "dev" forever was the bug. Whatever the
// build, `statable version` has to give a bug report something to quote.
func TestBuildInfoAlwaysNamesSomething(t *testing.T) {
	b := BuildInfo()
	if b.Version == "" {
		t.Fatal("version is empty")
	}
	if b.Go == "" || b.OS == "" || b.Arch == "" {
		t.Fatalf("the platform is not described: %+v", b)
	}
	if !strings.HasPrefix(b.String(), "statable ") {
		t.Fatalf("the human line does not name the program: %q", b.String())
	}
}

// TestPseudoVersionIsNotReportedAsARelease.
//
// Go synthesises a version like v0.0.0-20260909171644-36bf72eab1db for a build
// from an untagged working tree, and appends "+dirty" when the tree is
// modified. Reporting either as a version invites a bug report against
// something that was never published. An earlier fix matched the commit as a
// suffix, which the "+dirty" case slipped straight past — exactly the build
// most likely to be mistaken for a release.
func TestPseudoVersionIsNotReportedAsARelease(t *testing.T) {
	pseudo := []string{
		"v0.0.0-20260909171644-36bf72eab1db",
		"v0.0.0-20260909171644-36bf72eab1db+dirty",
		"v1.2.3-0.20260101000000-abcdef123456",
		"v1.2.3-rc.1.0.20260101000000-abcdef123456",
	}
	for _, v := range pseudo {
		if !isPseudoVersion(v) {
			t.Errorf("%q is a pseudo-version and must not be reported as a release", v)
		}
	}

	real := []string{"v0.1.0", "v1.2.3", "v1.2.3-rc.1", "v2.0.0+incompatible", ""}
	for _, v := range real {
		if isPseudoVersion(v) {
			t.Errorf("%q is a tag, not a pseudo-version", v)
		}
	}
}

// TestUserAgentIsOneDefinition: two spellings had drifted, one of them without
// the platform, so a server-side count of clients saw two products.
func TestUserAgentIsOneDefinition(t *testing.T) {
	ua := UserAgent()
	if !strings.HasPrefix(ua, "statable-cli/") {
		t.Fatalf("user agent does not name the client: %q", ua)
	}
	b := BuildInfo()
	for _, want := range []string{b.Version, b.OS, b.Arch} {
		if !strings.Contains(ua, want) {
			t.Fatalf("user agent %q omits %q", ua, want)
		}
	}
	// A header value with a newline in it is a request-splitting primitive.
	if strings.ContainsAny(ua, "\r\n") {
		t.Fatalf("user agent contains a line break: %q", ua)
	}
}

// TestShortCommitDoesNotPanicOnAnythingShort: the value comes from the build,
// not from a promise about its length.
func TestShortCommitDoesNotPanicOnAnythingShort(t *testing.T) {
	for _, c := range []string{"", "a", "abcdef", "abcdefg", "abcdefgh"} {
		if got := shortCommit(c); len(got) > 7 {
			t.Fatalf("shortCommit(%q) = %q", c, got)
		}
	}
}

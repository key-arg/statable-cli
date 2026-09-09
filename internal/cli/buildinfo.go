package cli

import (
	"fmt"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
)

// Version, Commit and Date are stamped by the release build with -ldflags.
// They are deliberately the only three: anything else a person needs is
// already in the binary and can be read back at run time.
var (
	Version = ""
	Commit  = ""
	Date    = ""
)

// Build is what `statable version` reports.
type Build struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Date    string `json:"date,omitempty"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	// Modified is true when the working tree had uncommitted changes at build
	// time. A bug report against a modified build is a bug report against
	// source nobody else has.
	Modified bool `json:"modified,omitempty"`
}

// BuildInfo is resolved once. debug.ReadBuildInfo walks the whole build graph,
// and the answer cannot change while the process runs.
var BuildInfo = sync.OnceValue(buildInfo)

// UserAgent identifies this client to the API.
//
// One definition, because two of them drifted: the login check sent a
// different string from every other call, so a server-side count of clients
// saw two products.
func UserAgent() string {
	b := BuildInfo()
	return fmt.Sprintf("statable-cli/%s (%s; %s)", b.Version, b.OS, b.Arch)
}

// buildInfo assembles the version from the strongest source available.
//
// A release stamps the three variables above. Everything else — `go install
// github.com/key-arg/statable-cli/cmd/statable@latest`, `go build` in a clone,
// `go run` — stamps nothing, and used to report "dev" forever: a user could
// not tell which build they had and neither could a bug report. Go already
// records the module version and the VCS revision in every binary it links, so
// the fallback reads them back rather than inventing a placeholder.
func buildInfo() Build {
	b := Build{
		Version: Version,
		Commit:  Commit,
		Date:    Date,
		Go:      runtime.Version(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}

	bi, ok := debug.ReadBuildInfo()
	moduleVersion := ""
	if ok {
		// "(devel)" is what Go writes for a build from a working tree with no
		// VCS information. It is not a version, so it is discarded here and
		// the revision below describes the build instead.
		if v := bi.Main.Version; v != "(devel)" {
			moduleVersion = v
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if b.Commit == "" {
					b.Commit = s.Value
				}
			case "vcs.time":
				if b.Date == "" {
					b.Date = s.Value
				}
			case "vcs.modified":
				b.Modified = s.Value == "true"
			}
		}
	}

	if b.Version == "" && !isPseudoVersion(moduleVersion) {
		b.Version = moduleVersion
	}
	if b.Version == "" {
		// Nothing tagged. The commit is the only honest identifier left, and
		// "devel" beats a number that looks like a release and is not one.
		if b.Commit != "" {
			b.Version = "devel"
		} else {
			b.Version = "unknown"
		}
	}
	return b
}

// isPseudoVersion reports whether a module version is one Go synthesised from
// this very commit rather than one someone tagged.
//
// Go records a pseudo-version like v0.0.0-20260909171644-36bf72eab1db for a
// build from an untagged working tree. It is accurate but it is not a release,
// and printing it as one invites a bug report against a version that was never
// published. The commit it ends with is shown separately anyway.
//
// Matching on the commit alone was not enough: Go appends "+dirty" when the
// tree is modified, and the suffix check then missed the very build most
// likely to be mistaken for a release.
func isPseudoVersion(version string) bool {
	return pseudoVersion.MatchString(version)
}

// pseudoVersion is Go's own shape: a fourteen-digit timestamp and a
// twelve-character revision.
//
// The separator before the timestamp is a dash when there is no known base
// version and a dot when the base is a tag, so both forms are matched. A
// pattern that accepted only the dash missed every pseudo-version built on a
// repository that already had tags — which is every repository after its first
// release.
var pseudoVersion = regexp.MustCompile(`[-.]\d{14}-[0-9a-f]{12}`)

// String is the single line a person reads.
func (b Build) String() string {
	var sb strings.Builder
	sb.WriteString("statable ")
	sb.WriteString(b.Version)
	if b.Commit != "" {
		sb.WriteString(" (")
		sb.WriteString(shortCommit(b.Commit))
		if b.Modified {
			sb.WriteString(", modified")
		}
		sb.WriteString(")")
	}
	sb.WriteString(" ")
	sb.WriteString(b.OS)
	sb.WriteString("/")
	sb.WriteString(b.Arch)
	sb.WriteString(" ")
	sb.WriteString(b.Go)
	return sb.String()
}

// shortCommit is the seven characters every git UI shows.
func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

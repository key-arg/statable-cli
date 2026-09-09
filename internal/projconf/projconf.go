// Package projconf reads the optional per-project configuration file that may
// be committed to a repository.
//
// The allowlist in this file is a security control, not a convenience.
// A file that lives in a repository is written by whoever can open a pull
// request. If it could set the API host, the CLI would send credentials to an
// address chosen by that person; if it could set a token, a checkout would
// silently swap the caller's identity. So the file may set only which site to
// look at and over what period, and every other key is ignored loudly.
package projconf

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Filenames searched for, in order, in each directory walked upward.
var Filenames = []string{".statable.yml", ".statable.yaml"}

// Allowed is the complete set of keys a project file may set. Adding to this
// list is a security decision: ask what a hostile pull request could do with
// the new key before adding it.
var Allowed = map[string]bool{
	"site":   true,
	"period": true,
}

// Config is the accepted subset.
type Config struct {
	// Site is the site id or domain to use when --site is not given.
	Site string
	// Period is the default date range, for example "7d".
	Period string

	// Path is the file this came from, empty if none was found.
	Path string
	// Ignored lists keys that were present and refused, so the user learns
	// their setting is not taking effect instead of wondering why.
	Ignored []string
}

// Find walks upward from dir looking for a project file, stopping at the
// filesystem root. The nearest file wins; they do not merge.
func Find(dir string) (string, bool) {
	cur, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		for _, name := range Filenames {
			p := filepath.Join(cur, name)
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				return p, true
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		cur = parent
	}
}

// maxSize bounds the file. A project config is a handful of lines; anything
// larger is a mistake or an attack, and either way is not worth parsing.
const maxSize = 64 << 10

// Load reads and filters a project file. A missing file is not an error: the
// zero Config is returned.
func Load(path string) (Config, error) {
	var c Config
	c.Path = path

	fi, err := os.Stat(path)
	if err != nil {
		return Config{}, nil
	}
	if fi.Size() > maxSize {
		c.Ignored = append(c.Ignored, "(the file is too large to read)")
		return c, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, nil
	}

	raw := map[string]any{}
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return c, err
	}

	// Keys are compared case-insensitively, so two spellings of the same key
	// in one file are ambiguous. Go map iteration is randomised, which would
	// make the CLI query a different site on different runs from the same
	// committed file. Refuse the collision instead of picking one at random.
	byNormal := map[string][]string{}
	for k := range raw {
		n := strings.ToLower(strings.TrimSpace(k))
		byNormal[n] = append(byNormal[n], k)
	}
	for n, spellings := range byNormal {
		if len(spellings) > 1 {
			sort.Strings(spellings)
			c.Ignored = append(c.Ignored,
				fmt.Sprintf("%s (set %d times as %s)", n, len(spellings), strings.Join(spellings, ", ")))
			delete(byNormal, n)
		}
	}

	for n, spellings := range byNormal {
		k := spellings[0]
		if !Allowed[n] {
			c.Ignored = append(c.Ignored, k)
			continue
		}
		str, ok := raw[k].(string)
		if !ok {
			// A non-string value for an allowed key is still refused, so a
			// nested map cannot smuggle anything in.
			c.Ignored = append(c.Ignored, k)
			continue
		}
		switch n {
		case "site":
			c.Site = strings.TrimSpace(str)
		case "period":
			c.Period = strings.TrimSpace(str)
		}
	}
	sort.Strings(c.Ignored)
	return c, nil
}

// LoadFrom finds and loads the nearest project file starting at dir.
func LoadFrom(dir string) (Config, error) {
	p, ok := Find(dir)
	if !ok {
		return Config{}, nil
	}
	return Load(p)
}

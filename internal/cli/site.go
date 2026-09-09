package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/clierr"
)

// settings is the small amount of state the CLI keeps for itself, separate
// from credentials so a logout never loses a preference and a preference file
// never holds a secret.
type settings struct {
	DefaultSite string `json:"default_site,omitempty"`
	// extra keeps every key this version does not know about, so an upgrade
	// that adds a setting does not lose one written by a newer build, and a
	// `sites use` does not quietly rewrite the file down to what it recognises.
	extra map[string]json.RawMessage
}

func (r *Runtime) settingsPath() string {
	return filepath.Join(r.Store.ConfigDir, "settings.json")
}

// loadSettings reads the preference file. A file that does not parse is
// reported rather than silently ignored: the same rule the project file
// follows, and for the same reason — a setting that quietly does nothing
// costs an hour to work out.
func (r *Runtime) loadSettings() settings {
	var s settings
	b, err := os.ReadFile(r.settingsPath())
	if err != nil {
		return s
	}
	if err := json.Unmarshal(b, &s); err != nil {
		r.Out.Warn("%s: could not be parsed, ignoring it: %v", r.settingsPath(), err)
		return settings{}
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &raw); err == nil {
		delete(raw, "default_site")
		if len(raw) > 0 {
			s.extra = raw
		}
	}
	return s
}

func (r *Runtime) saveSettings(s settings) error {
	if err := os.MkdirAll(r.Store.ConfigDir, 0o700); err != nil {
		return clierr.Wrap(err, "CONFIG_DIR", "could not create the config directory")
	}
	merged := map[string]any{}
	for k, v := range s.extra {
		merged[k] = v
	}
	if s.DefaultSite != "" {
		merged["default_site"] = s.DefaultSite
	} else {
		delete(merged, "default_site")
	}
	b, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return clierr.Wrap(err, clierr.CodeUnexpected, "could not encode the settings")
	}
	// The temp file carries the process id so two simultaneous writes do not
	// share one scratch path and interleave.
	tmp := fmt.Sprintf("%s.%d.tmp", r.settingsPath(), os.Getpid())
	if err := writeNewFile(tmp, b); err != nil {
		return clierr.Wrap(err, "CONFIG_WRITE", "could not write the settings file")
	}
	if err := os.Rename(tmp, r.settingsPath()); err != nil {
		os.Remove(tmp)
		return clierr.Wrap(err, "CONFIG_WRITE", "could not write the settings file")
	}
	return nil
}

// fetchSites lists what the key can read, at most once per process and, when
// a recent listing is on disk, without a request at all.
//
// Every reading command resolves a site, and every resolution listed the sites
// again, so a single `statable now` spent two calls against a documented
// hourly budget where one would do.
func (r *Runtime) fetchSites(ctx context.Context) ([]api.Site, error) {
	r.sitesMu.Lock()
	defer r.sitesMu.Unlock()
	if r.sitesLoaded {
		return r.sites, nil
	}

	if !r.sitesNoCache {
		if cached, ok := r.loadSiteCache(ctx); ok {
			r.sites = cached
			r.sitesFromCache = true
			r.sitesLoaded = true
			return r.sites, nil
		}
	}

	// A failure is deliberately not remembered. Marking the listing as loaded
	// on the way in meant the first failure answered every later call in the
	// process, so a retry after a transient 500 returned the old error from
	// memory and never reached the server.
	client, err := r.Client(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := client.Get(ctx, "/sites", nil)
	r.traceResponse(resp)
	if err != nil {
		return nil, err
	}
	var out api.SitesResponse
	if err := api.DecodeInto(resp, &out); err != nil {
		return nil, err
	}
	r.sites = out.Sites
	r.sitesFromCache = false
	r.sitesLoaded = true
	r.saveSiteCache(ctx, out.Sites)
	return r.sites, nil
}

// matchSite finds a site by numeric id, exact name, or hostname. A site's
// name is often a full URL rather than a bare domain, so "example.com" has to
// match "https://example.com/" too.
func matchSite(sites []api.Site, want string) (api.Site, bool) {
	want = strings.TrimSpace(want)
	if want == "" {
		return api.Site{}, false
	}
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
		s = strings.TrimPrefix(s, "www.")
		return strings.TrimSuffix(s, "/")
	}
	// A name match is tried before the numeric one. With the id branch first,
	// a site literally named "12345" was unreachable by name and the user
	// silently got the analytics of whichever site happened to have that id.
	target := norm(want)
	for _, s := range sites {
		if norm(s.Name) == target {
			return s, true
		}
	}
	if id, err := strconv.ParseInt(want, 10, 64); err == nil {
		for _, s := range sites {
			if s.SiteID == id {
				return s, true
			}
		}
	}
	return api.Site{}, false
}

// resolveSite decides which site a command should read.
//
// Order: the --site flag, then a project file, then the saved default, then
// the only site the key can see. If none of those settle it, a person at a
// terminal is asked and anyone else gets an action_required error naming the
// choices, because guessing which site to report on is worse than stopping.
func (r *Runtime) resolveSite(ctx context.Context) (api.Site, error) {
	want := strings.TrimSpace(r.Proj.Site)
	if want == "" {
		want = strings.TrimSpace(r.loadSettings().DefaultSite)
	}

	sites, err := r.fetchSites(ctx)
	if err != nil {
		return api.Site{}, err
	}
	if len(sites) == 0 {
		return api.Site{}, clierr.ActionRequired("NO_SITES",
			"this key can read no sites",
			"statable auth status",
			"create a site at https://statable.com")
	}

	if want != "" {
		if s, ok := matchSite(sites, want); ok {
			return s, nil
		}
		// Naming a different site fixes this, so it is action_required with
		// the choices listed, not a plain failure. A script that retries on 1
		// and pages a human on 2 should not wake anyone for a typo.
		steps := make([]string, 0, len(sites)+1)
		for _, s := range sites {
			steps = append(steps, "statable --site "+s.Name+" ...")
		}
		return api.Site{}, &clierr.Err{Envelope: clierr.Envelope{
			Status: clierr.StatusActionRequired,
			Error: fmt.Sprintf("no site called %q; this key can read %s",
				want, strings.Join(siteNames(sites), ", ")),
			Issues:    []clierr.Issue{{Code: clierr.CodeSiteNotFound}},
			NextSteps: steps,
		}}
	}

	if len(sites) == 1 {
		return sites[0], nil
	}

	if !r.Ctx.CanPrompt() {
		return api.Site{}, clierr.ActionRequired(clierr.CodeNoSiteSelected,
			fmt.Sprintf("this key can read %d sites, so one has to be named", len(sites)),
			"statable --site "+sites[0].Name+" ...",
			"statable sites use "+sites[0].Name)
	}
	return r.promptForSite(sites)
}

func siteNames(sites []api.Site) []string {
	out := make([]string, 0, len(sites))
	for _, s := range sites {
		out = append(out, s.Name)
	}
	return out
}

// promptForSite asks which site to use.
//
// It is a numbered list read from stdin, not a full-screen selector. The rule
// this CLI follows is print-and-exit: a redraw loop is what breaks screen
// readers and CI, and a list of names a person types a number into works in
// every terminal, over every ssh connection, and reads aloud correctly.
func (r *Runtime) promptForSite(sites []api.Site) (api.Site, error) {
	w := r.Out.Human()
	fmt.Fprintf(w, "This key can read %d sites:\n\n", len(sites))
	for i, s := range sites {
		fmt.Fprintf(w, "  %d) %s\n", i+1, s.Name)
	}
	fmt.Fprintf(w, "\nWhich one? [1-%d] ", len(sites))

	// The CanPrompt gate above should already have prevented this, but a
	// blocking read on a pipe hangs forever rather than failing, so the
	// terminal check is repeated here where the block would actually happen.
	// Defence in depth is cheap; a wedged CI job is not.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return api.Site{}, clierr.ActionRequired(clierr.CodeNoSiteSelected,
			"several sites are readable and there is no terminal to ask on",
			"statable --site <domain> ...",
			"statable sites use <domain>")
	}

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return api.Site{}, clierr.ActionRequired(clierr.CodeNoSiteSelected,
			"no site was chosen",
			"statable sites use <domain>")
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(sites) {
		return api.Site{}, clierr.Failf(clierr.CodeNoSiteSelected,
			"%q is not one of the choices", strings.TrimSpace(line))
	}
	chosen := sites[n-1]
	r.Out.Note("using %s; run `statable sites use %s` to make it the default",
		chosen.Name, chosen.Name)
	return chosen, nil
}

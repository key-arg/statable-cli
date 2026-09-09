package cli

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/auth"
	"github.com/key-arg/statable-cli/internal/clierr"
)

// siteCacheTTL is how long a listing is believed without asking again.
//
// The Stats API needs a numeric site_id, but people name sites by domain, so
// every reading command had to list the sites first and every `statable now`
// in a shell prompt cost two calls against an hourly budget. Ids never change
// and sites are created rarely, so a day-old listing is almost always right,
// and withSite below repairs the rare case where it is not.
const siteCacheTTL = 24 * time.Hour

// siteCacheFile is the on-disk shape.
//
// MAC authenticates the payload under the key the listing was fetched with. An
// identifier derived from the key would only bind the entry to a key — and
// since it is stored in the same file, anyone who can write the file can copy
// it across and repoint a site name at any id they like. `statable now` would
// then report on a property the user does not own, exit 0, and say nothing.
// A MAC cannot be forged without the key, so a rewritten file is a miss.
type siteCacheFile struct {
	MAC       string     `json:"mac"`
	FetchedAt time.Time  `json:"fetched_at"`
	Sites     []api.Site `json:"sites"`
}

// macPayload is exactly what is authenticated: the listing and its age, in the
// order they are written. Anything outside it is untrusted.
func macPayload(fetchedAt time.Time, sites []api.Site) ([]byte, error) {
	return json.Marshal(struct {
		FetchedAt time.Time  `json:"fetched_at"`
		Sites     []api.Site `json:"sites"`
	}{fetchedAt, sites})
}

func (r *Runtime) siteCachePath() string {
	return filepath.Join(r.Store.ConfigDir, "sites-cache.json")
}

// siteCacheMAC authenticates a payload under the active key and server.
//
// The server address is part of the MAC key because the same numeric id means
// a different property on a different deployment, so a listing must not carry
// across one.
func (r *Runtime) siteCacheMAC(ctx context.Context, payload []byte) string {
	return r.macWith(r.Key(ctx), payload)
}

// macWithCheapKey is the completion path: it never touches the keyring, so a
// TAB press cannot raise a Keychain dialog or block on a locked keyring.
func (r *Runtime) macWithCheapKey(payload []byte) string {
	return r.macWith(r.Store.ResolveCheap(r.flagKey, auth.Lookup(r.env)), payload)
}

func (r *Runtime) macWith(k auth.Resolved, payload []byte) string {
	if !k.Found() {
		return ""
	}
	secret := sha256.Sum256([]byte("statable-cli/site-cache\x00" + r.baseURL + "\x00" + k.Key))
	m := hmac.New(sha256.New, secret[:])
	m.Write(payload)
	return hex.EncodeToString(m.Sum(nil))
}

// maxSiteCacheBytes bounds what will be read back.
//
// A listing of a thousand sites is well under this. The bound exists because
// the path is a file anyone with write access to the directory can replace,
// and a symlink to /dev/zero has a stat size of zero and an endless body:
// os.ReadFile on it grows its buffer until the process dies. A cache must
// never be able to hang the command it exists to speed up.
const maxSiteCacheBytes = 4 << 20

// loadSiteCache returns a listing only when it belongs to this key and server
// and is younger than the TTL. Every failure is a miss: a cache is an
// optimisation, and no corruption in it may ever stop a command.
func (r *Runtime) loadSiteCache(ctx context.Context) ([]api.Site, bool) {
	return r.loadSiteCacheWith(func(payload []byte) string {
		return r.siteCacheMAC(ctx, payload)
	})
}

// loadSiteCacheCheap is loadSiteCache for a shell completion.
func (r *Runtime) loadSiteCacheCheap() ([]api.Site, bool) {
	return r.loadSiteCacheWith(r.macWithCheapKey)
}

func (r *Runtime) loadSiteCacheWith(mac func([]byte) string) ([]api.Site, bool) {
	// Defence in depth. The write-side guard is what keeps a cache out of
	// this directory in the first place, and a file planted there by someone
	// else fails the MAC anyway; this makes the intent explicit at the point
	// where the trust decision is made.
	if isUntrustedConfigDir(r.Store.ConfigDir) {
		return nil, false
	}
	b, err := readSmallFile(r.siteCachePath(), maxSiteCacheBytes)
	if err != nil {
		return nil, false
	}
	var f siteCacheFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, false
	}
	if len(f.Sites) == 0 {
		return nil, false
	}
	payload, err := macPayload(f.FetchedAt, f.Sites)
	if err != nil {
		return nil, false
	}
	want := mac(payload)
	// Constant time, because the comparison is against a value an attacker
	// controls and can retry.
	if want == "" || !hmac.Equal([]byte(want), []byte(f.MAC)) {
		return nil, false
	}
	age := time.Since(f.FetchedAt)
	// A negative age means the clock moved backwards or the file was written
	// by a machine ahead of this one. Either way the age is unusable, so the
	// entry is treated as a miss rather than as valid forever.
	if age < 0 || age > siteCacheTTL {
		return nil, false
	}
	return f.Sites, true
}

// saveSiteCache writes the listing. A failure is silent by design: not being
// able to cache is not a reason to fail a command that already succeeded.
func (r *Runtime) saveSiteCache(ctx context.Context, sites []api.Site) {
	// Nothing is cached into the temp-dir fallback. A listing read back from
	// a directory anyone can write is a mapping from a site name to a site
	// id that someone else chose, and the command would report on the wrong
	// property without a word.
	if isUntrustedConfigDir(r.Store.ConfigDir) {
		return
	}
	if len(sites) == 0 {
		return
	}
	fetchedAt := time.Now()
	payload, err := macPayload(fetchedAt, sites)
	if err != nil {
		return
	}
	mac := r.siteCacheMAC(ctx, payload)
	if mac == "" {
		return
	}
	b, err := json.Marshal(siteCacheFile{MAC: mac, FetchedAt: fetchedAt, Sites: sites})
	if err != nil {
		return
	}
	if err := os.MkdirAll(r.Store.ConfigDir, 0o700); err != nil {
		return
	}
	// 0600 because the listing names every property the key can read, which
	// is not a secret but is nobody else's business on a shared machine.
	tmp := filepath.Join(r.Store.ConfigDir, "sites-cache.json."+strconv.Itoa(os.Getpid())+".tmp")
	if err := writeNewFile(tmp, b); err != nil {
		return
	}
	if err := os.Rename(tmp, r.siteCachePath()); err != nil {
		os.Remove(tmp)
	}
}

func (r *Runtime) dropSiteCache() { os.Remove(r.siteCachePath()) }

// refreshSites forces one live listing, replacing whatever was cached.
func (r *Runtime) refreshSites(ctx context.Context) ([]api.Site, error) {
	r.sitesMu.Lock()
	r.sitesLoaded = false
	r.sitesNoCache = true
	r.sitesMu.Unlock()
	return r.fetchSites(ctx)
}

// staleSiteCodes are the codes that mean "the server does not agree that this
// key reaches this site". Documented in errors.md: unknown_site covers both a
// site that does not exist and one this key cannot read, and key_not_scoped is
// a single-site key asking about another.
var staleSiteCodes = map[string]bool{
	"unknown_site":   true,
	"key_not_scoped": true,
}

func isStaleSiteError(err error) bool {
	var e *clierr.Err
	if !errors.As(err, &e) {
		return false
	}
	for _, is := range e.Envelope.Issues {
		if staleSiteCodes[is.Code] {
			return true
		}
	}
	return false
}

// withSite runs a site-scoped call and repairs one specific mistake: a cached
// listing that has gone out of date. Without this, renaming or recreating a
// site left the CLI reporting a stale id for a whole day with an error that
// told the user nothing about why.
func (r *Runtime) withSite(ctx context.Context, fn func(site api.Site) error) error {
	site, err := r.resolveSite(ctx)
	if err != nil {
		return err
	}
	err = fn(site)
	if err == nil || !r.sitesFromCache || !isStaleSiteError(err) {
		return err
	}

	// The repair matches against a fresh listing directly rather than calling
	// resolveSite again. resolveSite may warn about a broken settings file and
	// may ask a person which site to use, and doing either a second time
	// inside one command means a duplicated warning or a second prompt whose
	// first answer has already been read off stdin and discarded.
	r.dropSiteCache()
	fresh, rerr := r.refreshSites(ctx)
	if rerr != nil {
		return err
	}
	repaired, ok := matchSite(fresh, site.Name)
	if !ok || repaired.SiteID == site.SiteID {
		// Either the site is gone, or the cache was right and the server
		// still refuses. Nothing was stale, so the original error stands.
		return err
	}
	return fn(repaired)
}

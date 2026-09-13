// Package auth resolves the API key and decides where a newly supplied key is
// stored.
//
// Two rules here are deliberate reactions to how other CLIs get this wrong:
//
//   - Nothing degrades silently. Several widely used CLIs fall back from the
//     system keyring to a plaintext file on any keyring error, without saying
//     so and sometimes without any way to opt out. Here the fallback exists,
//     but it has to be asked for with --insecure-storage, and the active
//     source is always reported by `statable auth status`.
//   - The keyring call is bounded. A locked or wedged keyring daemon must not
//     hang the process; after the timeout it is treated as unavailable.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/key-arg/statable-cli/internal/clierr"
)

// Service and account identify the entry in the system keyring.
const (
	Service = "statable-cli"
	Account = "api-key"
)

// KeyringTimeout bounds every keyring call. A keyring that has not answered
// in this long is treated as unavailable rather than waited on.
const KeyringTimeout = 60 * time.Second

// Source names where a key came from. It is reported to the user verbatim,
// because "which key am I actually using" is the question behind most auth
// confusion.
type Source string

const (
	SourceFlag    Source = "flag"
	SourceEnv     Source = "environment"
	SourceKeyring Source = "keyring"
	SourceFile    Source = "file"
	SourceNone    Source = "none"
)

// EnvVar is the environment variable checked before any stored credential.
const EnvVar = "STATABLE_API_KEY"

// Resolved is the answer to "which key, and from where".
type Resolved struct {
	Key    string
	Source Source
	// Path is set only for SourceFile.
	Path string
}

// Found reports whether a key was located at all.
func (r Resolved) Found() bool { return r.Key != "" }

// Keyring is the subset of a system keyring this package needs. It is an
// interface so tests never touch the real one.
type Keyring interface {
	Get(service, account string) (string, error)
	Set(service, account, secret string) error
	Delete(service, account string) error
}

// ErrKeyringUnavailable is returned by a Keyring that cannot be reached.
var ErrKeyringUnavailable = errors.New("keyring unavailable")

// Lookup mirrors os.LookupEnv.
type Lookup func(string) (string, bool)

// Store resolves and persists credentials.
type Store struct {
	Keyring   Keyring
	ConfigDir string
	// Insecure forces the plaintext file even when a keyring is available.
	Insecure bool
	// Timeout bounds keyring calls. Zero means KeyringTimeout.
	Timeout time.Duration
}

type fileCreds struct {
	APIKey string `json:"api_key"`
}

func (s *Store) credentialsPath() string { return filepath.Join(s.ConfigDir, "credentials.json") }

func (s *Store) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return KeyringTimeout
}

// bounded runs fn with a deadline and honours cancellation. The channel is
// buffered so the worker always completes its send and exits even when nobody
// is waiting any more: that is what keeps a slow keyring from leaking a
// goroutine per call.
//
// timedOut distinguishes "the keyring said no" from "the keyring said
// nothing", which the caller has to report differently.
func bounded[T any](ctx context.Context, d time.Duration, fn func() (T, error)) (val T, err error, timedOut bool) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, e := fn()
		ch <- result{v, e}
	}()

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case r := <-ch:
		return r.v, r.err, false
	case <-timer.C:
		var zero T
		return zero, nil, true
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err(), true
	}
}

// getKeyring reads the key with a bounded wait. A keyring that is missing,
// locked or wedged yields ("", false) rather than blocking.
func (s *Store) getKeyring(ctx context.Context) (string, bool) {
	if s.Keyring == nil {
		return "", false
	}
	v, err, _ := bounded(ctx, s.timeout(), func() (string, error) {
		return s.Keyring.Get(Service, Account)
	})
	if err != nil || v == "" {
		return "", false
	}
	return v, true
}

func (s *Store) readFile() (string, bool) {
	b, err := os.ReadFile(s.credentialsPath())
	if err != nil {
		return "", false
	}
	var c fileCreds
	if err := json.Unmarshal(b, &c); err != nil {
		return "", false
	}
	if c.APIKey == "" {
		return "", false
	}
	return c.APIKey, true
}

// Resolve walks the precedence chain: an explicit flag, then the environment,
// then the keyring, then the plaintext file. The first hit wins and the
// source is recorded.
func (s *Store) Resolve(ctx context.Context, flagKey string, env Lookup) Resolved {
	if k := strings.TrimSpace(flagKey); k != "" {
		return Resolved{Key: k, Source: SourceFlag}
	}
	if env != nil {
		if v, ok := env(EnvVar); ok {
			if k := strings.TrimSpace(v); k != "" {
				return Resolved{Key: k, Source: SourceEnv}
			}
		}
	}
	if !keyringDisabled(env) {
		if k, ok := s.getKeyring(ctx); ok {
			return Resolved{Key: k, Source: SourceKeyring}
		}
	}
	if k, ok := s.readFile(); ok {
		return Resolved{Key: k, Source: SourceFile, Path: s.credentialsPath()}
	}
	return Resolved{Source: SourceNone}
}

// NoKeyringVar switches the system keyring off for one invocation.
//
// It exists for two situations that are really the same one. A machine with no
// keyring daemon -- a container, a CI runner, a server -- otherwise waits out
// the full timeout on every command before falling back. And this program's own
// tests resolved through the real keyring on the developer's machine, which
// made two runs out of a few hundred fail for reasons that had nothing to do
// with the code under test.
const NoKeyringVar = "STATABLE_NO_KEYRING"

func keyringDisabled(env Lookup) bool {
	if env == nil {
		return false
	}
	v, ok := env(NoKeyringVar)
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no":
		return false
	}
	return true
}

// ResolveCheap is Resolve without the keyring.
//
// It exists for work that happens on a keypress rather than on a command. On
// macOS a keyring read can raise a Keychain dialog; on Linux a locked
// gnome-keyring can block for the full timeout. Either is unacceptable for a
// shell completion, which must answer immediately or not at all. Callers get
// no key when the only copy is in the keyring, and must treat that as "no
// suggestions" rather than as "not authenticated".
func (s *Store) ResolveCheap(flagKey string, env Lookup) Resolved {
	if k := strings.TrimSpace(flagKey); k != "" {
		return Resolved{Key: k, Source: SourceFlag}
	}
	if env != nil {
		if v, ok := env(EnvVar); ok {
			if k := strings.TrimSpace(v); k != "" {
				return Resolved{Key: k, Source: SourceEnv}
			}
		}
	}
	if k, ok := s.readFile(); ok {
		return Resolved{Key: k, Source: SourceFile, Path: s.credentialsPath()}
	}
	return Resolved{Source: SourceNone}
}

// Save persists a key and reports where it went.
//
// When no keyring is available and --insecure-storage was not given, this
// fails rather than quietly writing the key in the clear. That refusal is the
// point: a user who believes their key is in the keyring should never be
// wrong about it.
func (s *Store) Save(ctx context.Context, key string) (Source, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return SourceNone, clierr.Fail("EMPTY_KEY", "no key was given")
	}

	if !s.Insecure && s.Keyring != nil {
		_, err, timedOut := bounded(ctx, s.timeout(), func() (struct{}, error) {
			return struct{}{}, s.Keyring.Set(Service, Account, key)
		})
		switch {
		case timedOut:
			// The write may still land after we return. Compensating by
			// deleting would be worse than saying so: if the late write
			// failed, the delete would destroy a key that was already there
			// and working. Report the uncertainty and name the command that
			// resolves it.
			return SourceNone, clierr.ActionRequired(clierr.CodeKeyringLocked,
				"the system keyring did not respond, so the key may or may not have been stored",
				"statable auth status",
				"statable auth login --insecure-storage")
		case err == nil:
			return SourceKeyring, nil
		}
	}

	if !s.Insecure {
		return SourceNone, clierr.ActionRequired(clierr.CodeKeyringLocked,
			"the system keyring is not available, and the key will not be written in the clear without being asked",
			"statable auth login --insecure-storage",
			"unlock your keyring and try again")
	}

	if err := s.writeFile(key); err != nil {
		return SourceNone, err
	}
	return SourceFile, nil
}

// InsecureWarning is the sentence to show after a key was written in the
// clear, or empty when there is nothing platform-specific to say.
//
// The rule this package follows is that credentials never degrade silently.
// On Windows the file mode is not enforced at all, so writing 0600 and saying
// nothing would be exactly that degradation: the user believes the key is
// protected by permissions that the operating system ignores.
func (s *Store) InsecureWarning() string { return PlaintextWarning(s.credentialsPath()) }

func (s *Store) writeFile(key string) error {
	if err := os.MkdirAll(s.ConfigDir, 0o700); err != nil {
		return clierr.Wrap(err, "CONFIG_DIR", "could not create the config directory")
	}
	// MkdirAll leaves an existing directory's mode alone, so a config
	// directory created earlier by something more permissive would still be
	// group or world traversable. Tighten it before the key goes in.
	if fi, err := os.Stat(s.ConfigDir); err == nil && fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(s.ConfigDir, 0o700); err != nil {
			return clierr.Wrap(err, "CONFIG_DIR",
				"the config directory is readable by other users and could not be tightened")
		}
	}
	b, err := json.MarshalIndent(fileCreds{APIKey: key}, "", "  ")
	if err != nil {
		return clierr.Wrap(err, clierr.CodeUnexpected, "could not encode the credentials")
	}
	path := s.credentialsPath()
	// Write through a temp file in the same directory so a crash cannot leave
	// a half-written credential behind, and create it 0600 from the start so
	// the key is never briefly world-readable.
	// The scratch path carries the process id so two concurrent logins do not
	// write the same file before renaming.
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return clierr.Wrap(err, "CONFIG_WRITE", "could not write the credentials file")
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return clierr.Wrap(err, "CONFIG_WRITE", "could not write the credentials file")
	}
	return nil
}

// Delete removes the key from both stores. It reports what it actually
// removed so `auth logout` can be honest about it.
func (s *Store) Delete(ctx context.Context) []Source {
	var removed []Source
	if s.Keyring != nil {
		_, err, timedOut := bounded(ctx, s.timeout(), func() (struct{}, error) {
			return struct{}{}, s.Keyring.Delete(Service, Account)
		})
		if err == nil && !timedOut {
			removed = append(removed, SourceKeyring)
		}
	}
	if _, err := os.Stat(s.credentialsPath()); err == nil {
		if err := os.Remove(s.credentialsPath()); err == nil {
			removed = append(removed, SourceFile)
		}
	}
	return removed
}

// Describe renders a one-line explanation of where the active key came from.
func (r Resolved) Describe() string {
	switch r.Source {
	case SourceFlag:
		return "--key on the command line"
	case SourceEnv:
		return fmt.Sprintf("the %s environment variable", EnvVar)
	case SourceKeyring:
		return "the system keyring"
	case SourceFile:
		return fmt.Sprintf("%s (plaintext)", r.Path)
	default:
		return "nowhere: no key is configured"
	}
}

// keyPrefix is the marker every Statable key carries. It is not secret, so it
// is shown in full; everything after it is.
const keyPrefix = "stbl_"

// revealedBody is how many characters of the secret itself Redact shows at
// each end. Nine were shown from the front before, which for a key without
// the usual prefix meant nine characters of the secret rather than four.
const revealedBody = 4

// minRedactable is the shortest key for which showing anything still hides a
// meaningful amount. Below it a fixed placeholder is returned, identical for
// every key, so it carries no information at all. Without this margin a
// 13-character key was reproduced in full with an ellipsis inserted between
// two adjacent slices.
const minRedactable = len(keyPrefix) + revealedBody*2 + 7

// Redact shows enough of a key to recognise it and not enough to use it.
// Redacted keys are printed by auth status and auth login and routinely end
// up in CI logs, so the margin matters.
func Redact(key string) string {
	if len(key) < minRedactable {
		return keyPrefix + "…"
	}
	head, body := "", key
	if strings.HasPrefix(key, keyPrefix) {
		head, body = keyPrefix, key[len(keyPrefix):]
	}
	if len(body) < revealedBody*2+1 {
		return keyPrefix + "…"
	}
	return head + body[:revealedBody] + "…" + body[len(body)-revealedBody:]
}

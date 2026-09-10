package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/key-arg/statable-cli/internal/clierr"
)

// fakeKeyring is an in-memory keyring. Tests never touch the real one.
// It is mutex-guarded because bounded() leaves its worker goroutine running
// after a timeout, so the backend is still reached concurrently with the test.
type fakeKeyring struct {
	mu      sync.Mutex
	data    map[string]string
	failGet bool
	failSet bool
	hang    time.Duration
}

func newFake() *fakeKeyring { return &fakeKeyring{data: map[string]string{}} }

func (f *fakeKeyring) setHang(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hang = d
}

func (f *fakeKeyring) delay() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hang
}

func (f *fakeKeyring) Get(service, account string) (string, error) {
	if d := f.delay(); d > 0 {
		time.Sleep(d)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failGet {
		return "", ErrKeyringUnavailable
	}
	v, ok := f.data[service+"/"+account]
	if !ok {
		return "", errors.New("not found")
	}
	return v, nil
}

func (f *fakeKeyring) Set(service, account, secret string) error {
	if d := f.delay(); d > 0 {
		time.Sleep(d)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failSet {
		return ErrKeyringUnavailable
	}
	f.data[service+"/"+account] = secret
	return nil
}

func (f *fakeKeyring) Delete(service, account string) error {
	// Delete honours the same delay as Get and Set. Without this,
	// TestDeleteIsBounded could not fail: the hang it sets up was ignored and
	// the elapsed-time assertion was satisfied trivially.
	if d := f.delay(); d > 0 {
		time.Sleep(d)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	k := service + "/" + account
	if _, ok := f.data[k]; !ok {
		return errors.New("not found")
	}
	delete(f.data, k)
	return nil
}

func envOf(m map[string]string) Lookup {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func newStore(t *testing.T, kr Keyring) *Store {
	t.Helper()
	return &Store{Keyring: kr, ConfigDir: t.TempDir(), Timeout: time.Second}
}

// TestPrecedence pins the resolution chain. Every source is populated at once,
// then removed one at a time, so the order is asserted rather than assumed.
func TestPrecedence(t *testing.T) {
	kr := newFake()
	s := newStore(t, kr)
	if err := os.WriteFile(s.credentialsPath(), []byte(`{"api_key":"stbl_from_file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := kr.Set(Service, Account, "stbl_from_keyring"); err != nil {
		t.Fatal(err)
	}
	env := envOf(map[string]string{EnvVar: "stbl_from_env"})

	if got := s.Resolve(context.Background(), "stbl_from_flag", env); got.Key != "stbl_from_flag" || got.Source != SourceFlag {
		t.Fatalf("flag must win: %+v", got)
	}
	if got := s.Resolve(context.Background(), "", env); got.Key != "stbl_from_env" || got.Source != SourceEnv {
		t.Fatalf("env must beat stored credentials: %+v", got)
	}
	if got := s.Resolve(context.Background(), "", envOf(nil)); got.Key != "stbl_from_keyring" || got.Source != SourceKeyring {
		t.Fatalf("keyring must beat the file: %+v", got)
	}
	_ = kr.Delete(Service, Account)
	got := s.Resolve(context.Background(), "", envOf(nil))
	if got.Key != "stbl_from_file" || got.Source != SourceFile {
		t.Fatalf("file is the last resort: %+v", got)
	}
	if got.Path == "" {
		t.Fatal("a file-sourced key must report its path so the user can find it")
	}
}

func TestBlankValuesAreNotCredentials(t *testing.T) {
	s := newStore(t, newFake())
	if got := s.Resolve(context.Background(), "   ", envOf(map[string]string{EnvVar: "  "})); got.Found() {
		t.Fatalf("whitespace must not count as a key: %+v", got)
	}
	if got := s.Resolve(context.Background(), "", envOf(nil)); got.Source != SourceNone {
		t.Fatalf("source = %q, want none", got.Source)
	}
}

// TestNoSilentDegradation is the rule this package exists to enforce: when the
// keyring cannot take the key, we refuse rather than writing it in the clear.
func TestNoSilentDegradation(t *testing.T) {
	kr := newFake()
	kr.mu.Lock()
	kr.failSet = true
	kr.mu.Unlock()
	s := newStore(t, kr)

	src, err := s.Save(context.Background(), "stbl_secret")
	if err == nil {
		t.Fatal("saving must fail when the keyring is unavailable and --insecure-storage was not given")
	}
	if src != SourceNone {
		t.Fatalf("source = %q, want none", src)
	}
	e := clierr.From(err)
	if e.Code() != clierr.CodeKeyringLocked {
		t.Fatalf("code = %q", e.Code())
	}
	if e.ExitCode() != clierr.ExitActionRequired {
		t.Fatal("an unavailable keyring is something the user can act on")
	}
	joined := strings.Join(e.Envelope.NextSteps, " ")
	if !strings.Contains(joined, "--insecure-storage") {
		t.Fatalf("the refusal must name the opt-out, got %q", joined)
	}
	if _, err := os.Stat(s.credentialsPath()); err == nil {
		t.Fatal("nothing may be written to disk when the save was refused")
	}
}

func TestInsecureStorageIsExplicitAndWritesOnly600(t *testing.T) {
	kr := newFake()
	kr.mu.Lock()
	kr.failSet = true
	kr.mu.Unlock()
	s := newStore(t, kr)
	s.Insecure = true

	src, err := s.Save(context.Background(), "stbl_secret")
	if err != nil {
		t.Fatal(err)
	}
	if src != SourceFile {
		t.Fatalf("source = %q, want file", src)
	}

	fi, err := os.Stat(s.credentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	// Windows maps a file mode onto the read-only attribute and nothing
	// else, so a file created with 0600 reports 0666 there. Asserting 0600
	// unconditionally does not make the file private on Windows; it only
	// makes the test fail. What must hold on every platform is that the mode
	// requested is 0600 and that the user is told when it is not enforced.
	if perm := fi.Mode().Perm(); FileModeIsEnforced() && perm != 0o600 {
		t.Fatalf("credentials file mode = %o, want 600", perm)
	}
	if !FileModeIsEnforced() && s.InsecureWarning() == "" {
		t.Fatal("this platform does not enforce the file mode and says nothing about it")
	}
	di, err := os.Stat(s.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	// Same platform rule as the file above: a directory mode means nothing on
	// Windows, where every directory reports 0777.
	if perm := di.Mode().Perm(); FileModeIsEnforced() && perm&0o077 != 0 {
		t.Fatalf("config dir mode = %o, must not be group or world accessible", perm)
	}
	if _, err := os.Stat(s.credentialsPath() + ".tmp"); err == nil {
		t.Fatal("the temp file must not survive a successful write")
	}
}

// TestKeyringPreferredWhenAvailable: the file is a fallback, not a default.
func TestKeyringPreferredWhenAvailable(t *testing.T) {
	s := newStore(t, newFake())
	src, err := s.Save(context.Background(), "stbl_secret")
	if err != nil {
		t.Fatal(err)
	}
	if src != SourceKeyring {
		t.Fatalf("source = %q, want keyring", src)
	}
	if _, err := os.Stat(s.credentialsPath()); err == nil {
		t.Fatal("a successful keyring save must not also write a plaintext copy")
	}
}

// TestKeyringHangIsBounded: a wedged keyring daemon must not hang the CLI.
func TestKeyringHangIsBounded(t *testing.T) {
	kr := newFake()
	if err := kr.Set(Service, Account, "stbl_slow"); err != nil {
		t.Fatal(err)
	}
	kr.setHang(2 * time.Second)

	s := &Store{Keyring: kr, ConfigDir: t.TempDir(), Timeout: 50 * time.Millisecond}
	start := time.Now()
	got := s.Resolve(context.Background(), "", envOf(nil))
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("resolution took %v; the keyring call must be bounded", elapsed)
	}
	if got.Found() {
		t.Fatal("a keyring that timed out must be treated as unavailable, not waited on")
	}
}

func TestDeleteReportsWhatItRemoved(t *testing.T) {
	kr := newFake()
	s := newStore(t, kr)
	if _, err := s.Save(context.Background(), "stbl_secret"); err != nil {
		t.Fatal(err)
	}
	s.Insecure = true
	if err := s.writeFile("stbl_secret"); err != nil {
		t.Fatal(err)
	}

	removed := s.Delete(context.Background())
	if len(removed) != 2 {
		t.Fatalf("removed = %v, want both stores", removed)
	}
	if got := s.Delete(context.Background()); len(got) != 0 {
		t.Fatalf("a second logout should remove nothing, got %v", got)
	}
}

func TestDescribeNamesTheSource(t *testing.T) {
	cases := []struct {
		r    Resolved
		want string
	}{
		{Resolved{Source: SourceEnv}, EnvVar},
		{Resolved{Source: SourceKeyring}, "keyring"},
		{Resolved{Source: SourceFile, Path: filepath.Join("x", "credentials.json")}, "plaintext"},
		{Resolved{Source: SourceNone}, "no key"},
	}
	for _, tc := range cases {
		if got := tc.r.Describe(); !strings.Contains(got, tc.want) {
			t.Fatalf("Describe() = %q, want it to mention %q", got, tc.want)
		}
	}
}

func TestRedactKeepsEnoughToRecognise(t *testing.T) {
	got := Redact("stbl_abcdefghijklmnop")
	if strings.Contains(got, "efghijkl") {
		t.Fatalf("redaction leaks the secret: %q", got)
	}
	if !strings.HasPrefix(got, "stbl_") {
		t.Fatalf("redaction should keep the prefix: %q", got)
	}
	if Redact("short") == "" {
		t.Fatal("redaction must handle a short value")
	}
}

// TestSaveTimeoutReportsUncertaintyWithoutDestroying: when the keyring write
// does not answer we cannot know whether it landed. Compensating with a
// delete would destroy a key that was already there and working if the late
// write turns out to have failed, so we say so instead and name the command
// that resolves it.
func TestSaveTimeoutReportsUncertaintyWithoutDestroying(t *testing.T) {
	kr := newFake()
	if err := kr.Set(Service, Account, "stbl_existing_and_working"); err != nil {
		t.Fatal(err)
	}
	kr.setHang(500 * time.Millisecond)

	s := &Store{Keyring: kr, ConfigDir: t.TempDir(), Timeout: 20 * time.Millisecond}
	src, err := s.Save(context.Background(), "stbl_new")
	if err == nil {
		t.Fatal("a timed-out save must not be reported as success")
	}
	if src != SourceNone {
		t.Fatalf("source = %q", src)
	}

	e := clierr.From(err)
	if !strings.Contains(e.Envelope.Error, "may or may not") {
		t.Fatalf("the message must admit the uncertainty, got %q", e.Envelope.Error)
	}
	joined := strings.Join(e.Envelope.NextSteps, " ")
	if !strings.Contains(joined, "auth status") {
		t.Fatalf("next steps must name how to find out, got %q", joined)
	}

	// The pre-existing key must survive: destroying it would sign the user
	// out of a credential that was working.
	kr.setHang(0)
	time.Sleep(700 * time.Millisecond)
	if _, err := kr.Get(Service, Account); err != nil {
		t.Fatal("a working key was destroyed by the failed save")
	}
	if _, err := os.Stat(s.credentialsPath()); err == nil {
		t.Fatal("nothing may be written to disk when the save was refused")
	}
}

// TestRedactNeverRevealsTheWholeKey walks the boundary. A 13-character key
// used to come back complete with an ellipsis inserted between two adjacent
// slices, because the guard was shorter than what the reveal exposed.
func TestRedactNeverRevealsTheWholeKey(t *testing.T) {
	// The body uses a character that does not occur in the "stbl_" prefix, so
	// a match can only come from the secret itself.
	for n := 1; n <= 40; n++ {
		key := "stbl_" + strings.Repeat("X", n)
		got := Redact(key)
		if strings.Contains(got, strings.Repeat("X", n)) {
			t.Fatalf("length %d: redaction contains the whole secret body: %q", len(key), got)
		}
		visible := len(strings.ReplaceAll(got, "…", ""))
		if visible >= len(key) {
			t.Fatalf("length %d: redaction shows %d of %d characters: %q",
				len(key), visible, len(key), got)
		}
	}
}

// TestDeleteIsBounded: auth logout must not hang on a wedged keyring.
func TestDeleteIsBounded(t *testing.T) {
	kr := newFake()
	if err := kr.Set(Service, Account, "stbl_x"); err != nil {
		t.Fatal(err)
	}
	kr.setHang(2 * time.Second)
	s := &Store{Keyring: kr, ConfigDir: t.TempDir(), Timeout: 30 * time.Millisecond}

	start := time.Now()
	s.Delete(context.Background())
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Delete took %v; it must be bounded like every other keyring call", elapsed)
	}
}

// TestResolveHonoursCancellation: Ctrl-C must cut a wedged keyring short
// rather than holding the process for the full timeout.
func TestResolveHonoursCancellation(t *testing.T) {
	kr := newFake()
	if err := kr.Set(Service, Account, "stbl_x"); err != nil {
		t.Fatal(err)
	}
	kr.setHang(3 * time.Second)
	s := &Store{Keyring: kr, ConfigDir: t.TempDir(), Timeout: time.Minute}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()

	start := time.Now()
	got := s.Resolve(ctx, "", envOf(nil))
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("resolution took %v despite cancellation", elapsed)
	}
	if got.Found() {
		t.Fatal("a cancelled keyring read must not yield a key")
	}
}

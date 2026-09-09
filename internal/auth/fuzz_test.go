package auth

import (
	"strings"
	"testing"
)

// FuzzRedact: a redacted key is printed by auth status and auth login in all
// three formats and routinely lands in CI logs. Whatever the key looks like,
// what comes back must be shorter than the secret and must not contain it.
func FuzzRedact(f *testing.F) {
	for _, seed := range []string{
		"", "s", "stbl_", "stbl_abcdefgh", "stbl_abcdefghijklmno",
		"stbl_" + strings.Repeat("x", 200), strings.Repeat("\U0001F642", 10),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, key string) {
		got := Redact(key)
		if key == "" {
			return
		}
		// The ellipsis is stripped before any comparison. Leaving it in makes
		// the check wrong rather than strict: U+2026 encodes as e2 80 a6, so
		// a key whose last byte is 0xe2 appears to be "contained" in the
		// redaction purely by byte overlap with the separator.
		visible := strings.ReplaceAll(got, "\u2026", "")

		if len(key) >= minRedactable && len(visible) >= len(key) {
			t.Fatalf("redaction of a %d-byte key shows %d bytes: %q", len(key), len(visible), got)
		}

		// Below the threshold the answer is a fixed placeholder, identical
		// for every key, so it carries no information and containment tells
		// us nothing. The real guarantee there is that it is constant.
		if len(key) < minRedactable {
			if got != Redact("stbl_"+strings.Repeat("z", 3)) {
				t.Fatalf("a short key must yield the fixed placeholder, got %q", got)
			}
			return
		}

		// Above it, no more than the allowance of the secret may show, and
		// never the whole of it.
		body := strings.TrimPrefix(key, keyPrefix)
		if body != "" && strings.Contains(visible, body) {
			t.Fatalf("redaction of %q shows the whole secret body: %q", key, got)
		}
		shown := strings.TrimPrefix(visible, keyPrefix)
		if len(shown) > revealedBody*2 {
			t.Fatalf("redaction shows %d characters of the secret, the allowance is %d: %q",
				len(shown), revealedBody*2, got)
		}
	})
}

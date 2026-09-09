package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/key-arg/statable-cli/internal/clierr"
)

func serve(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL, "stbl_test", "statable-cli/test")
}

func TestSendsBearerAndAccept(t *testing.T) {
	var gotAuth, gotAccept, gotUA string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotUA = r.Header.Get("User-Agent")
		w.Write([]byte(`{"sites":[]}`))
	})
	if _, err := c.Get(context.Background(), "/sites", nil); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer stbl_test" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Fatalf("Accept = %q", gotAccept)
	}
	if gotUA != "statable-cli/test" {
		t.Fatalf("User-Agent = %q", gotUA)
	}
}

// TestRateLimitIsReadNotAssumed: the ceiling is tuned server-side, so it must
// come from the headers on every call.
func TestRateLimitIsReadNotAssumed(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "1200")
		w.Header().Set("X-RateLimit-Remaining", "1197")
		w.Write([]byte(`{}`))
	})
	resp, err := c.Get(context.Background(), "/sites", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Rate.Limit != 1200 || resp.Rate.Remaining != 1197 {
		t.Fatalf("rate = %+v", resp.Rate)
	}
	if !c.LastRate.Known() {
		t.Fatal("LastRate must be populated so callers can pace themselves")
	}
}

func TestRequestIDIsKeptOnSuccess(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "ch-node01-abc")
		w.Write([]byte(`{}`))
	})
	resp, err := c.Get(context.Background(), "/sites", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.RequestID != "ch-node01-abc" {
		t.Fatalf("RequestID = %q, want it kept on success too", resp.RequestID)
	}
}

// TestServerCodePassesThrough: the API's stable code must survive into the
// envelope untouched, because that is what callers branch on.
func TestServerCodePassesThrough(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "ch-node01-xyz")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"code":"unknown_metric","error":"unknown metric: foo","request_id":"ch-node01-xyz"}`))
	})
	_, err := c.Get(context.Background(), "/query", nil)
	e := clierr.From(err)
	if e == nil {
		t.Fatal("expected an error")
	}
	if e.Code() != "unknown_metric" {
		t.Fatalf("code = %q, want the server's own code", e.Code())
	}
	if e.Envelope.RequestID != "ch-node01-xyz" {
		t.Fatalf("request id = %q", e.Envelope.RequestID)
	}
	if e.ExitCode() != clierr.ExitError {
		t.Fatalf("a bad request must exit %d, got %d", clierr.ExitError, e.ExitCode())
	}
}

// TestUnauthorizedIsActionRequired: a missing or wrong key is something the
// caller can fix, so it exits 1 and names the fix.
func TestUnauthorizedIsActionRequired(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"code":"unauthorized","error":"bad key"}`))
	})
	_, err := c.Get(context.Background(), "/sites", nil)
	e := clierr.From(err)
	if e.Envelope.Status != clierr.StatusActionRequired {
		t.Fatalf("status = %q, want action_required", e.Envelope.Status)
	}
	if e.ExitCode() != clierr.ExitActionRequired {
		t.Fatalf("exit = %d, want %d", e.ExitCode(), clierr.ExitActionRequired)
	}
	if len(e.Envelope.NextSteps) == 0 {
		t.Fatal("an action_required error must name the action")
	}
}

func TestRateLimitedSuggestsAWait(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "600")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"code":"rate_limited","error":"slow down"}`))
	})
	_, err := c.Get(context.Background(), "/query", nil)
	e := clierr.From(err)
	if e.Code() != clierr.CodeRateLimited {
		t.Fatalf("code = %q", e.Code())
	}
	if e.ExitCode() != clierr.ExitActionRequired {
		t.Fatalf("rate limiting is recoverable, want exit %d", clierr.ExitActionRequired)
	}
	joined := strings.Join(e.Envelope.NextSteps, " ")
	if !strings.Contains(joined, "42s") {
		t.Fatalf("next steps should name the wait, got %q", joined)
	}
	if !strings.Contains(e.Envelope.Error, "600") {
		t.Fatalf("message should name the real ceiling, got %q", e.Envelope.Error)
	}
}

func TestServerErrorAsksForTheRequestID(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "ch-node07-500")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{}`))
	})
	_, err := c.Get(context.Background(), "/query", nil)
	e := clierr.From(err)
	if e.Envelope.RequestID != "ch-node07-500" {
		t.Fatalf("request id = %q", e.Envelope.RequestID)
	}
	if len(e.Envelope.NextSteps) == 0 {
		t.Fatal("a 500 should tell the user to quote the request id")
	}
}

func TestUnreachableHostIsNetworkError(t *testing.T) {
	c := New("http://127.0.0.1:1", "stbl_test", "t")
	c.HTTP.Timeout = 2 * time.Second
	_, err := c.Get(context.Background(), "/sites", nil)
	e := clierr.From(err)
	if e.Code() != clierr.CodeNetwork {
		t.Fatalf("code = %q, want %q", e.Code(), clierr.CodeNetwork)
	}
}

func TestOversizedBodyIsRefused(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		chunk := strings.Repeat("x", 1<<20)
		for i := 0; i < 10; i++ {
			w.Write([]byte(chunk))
		}
	})
	_, err := c.Get(context.Background(), "/query", nil)
	if err == nil {
		t.Fatal("an oversized body must be refused, not buffered")
	}
}

func TestResetSecondsAreTreatedAsDuration(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "100")
		w.Header().Set("X-RateLimit-Reset", "30")
		w.Write([]byte(`{}`))
	})
	resp, err := c.Get(context.Background(), "/sites", nil)
	if err != nil {
		t.Fatal(err)
	}
	d := time.Until(resp.Rate.ResetAt)
	if d < 25*time.Second || d > 35*time.Second {
		t.Fatalf("reset resolved to %v, want about 30s from now", d)
	}
}

// TestRequestIDSurvivesEveryErrorPath. Mutation testing found that dropping
// the request id from the 401 branch broke no test: the id is the one value
// support can act on, and each status is a separate branch, so each needs its
// own assertion.
func TestRequestIDSurvivesEveryErrorPath(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"code":"unauthorized","error":"bad key"}`},
		{"rate limited", http.StatusTooManyRequests, `{"code":"rate_limited","error":"slow down"}`},
		{"bad request", http.StatusBadRequest, `{"code":"unknown_metric","error":"no"}`},
		{"forbidden", http.StatusForbidden, `{"code":"forbidden","error":"no"}`},
		{"not found", http.StatusNotFound, `{"code":"not_found","error":"no"}`},
		{"server error", http.StatusInternalServerError, `{}`},
		{"bad gateway", http.StatusBadGateway, `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := serve(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Request-ID", "ch-node01-"+tc.name)
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			})
			_, err := c.Get(context.Background(), "/sites", nil)
			e := clierr.From(err)
			if e == nil {
				t.Fatal("expected an error")
			}
			if e.Envelope.RequestID != "ch-node01-"+tc.name {
				t.Fatalf("request id = %q, want it kept on a %d", e.Envelope.RequestID, tc.status)
			}
		})
	}
}

// TestRequestIDFallsBackToTheHeader: the body repeats the id, but a failure
// that produces no JSON body still has to carry it.
func TestRequestIDFallsBackToTheHeader(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "ch-node01-headeronly")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`not json at all`))
	})
	_, err := c.Get(context.Background(), "/sites", nil)
	e := clierr.From(err)
	if e.Envelope.RequestID != "ch-node01-headeronly" {
		t.Fatalf("request id = %q, want the header value when the body is unusable", e.Envelope.RequestID)
	}
	if e.Code() == "" {
		t.Fatal("an unparseable error body must still produce a code")
	}
}

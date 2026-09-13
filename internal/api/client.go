// Package api is the HTTP client for the Statable Stats API v1.
//
// Two things here are not incidental:
//
//   - The rate limit ceiling is read from the response headers, never
//     hard-coded. The API documents that the ceiling is tuned server-side
//     without a release, so any number compiled in would go stale silently.
//   - Every response carries X-Request-ID, and it is kept on success as well
//     as on failure. Its leading segment names the machine that served the
//     call, which is what makes a log line findable at all.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/key-arg/statable-cli/internal/clierr"
)

// DefaultBaseURL is the production API.
const DefaultBaseURL = "https://statable.com/api/v1"

// maxBody caps how much of a response we will read into memory. The API's
// largest documented response is a 1000-row breakdown; this leaves generous
// headroom while making a runaway or hostile body a bounded failure rather
// than an unbounded allocation.
const maxBody = 8 << 20 // 8 MiB

// RateLimit is what the server said about the budget on the last call.
// Zero values mean the server did not report that field.
type RateLimit struct {
	Limit     int
	Remaining int
	ResetAt   time.Time
}

// Known reports whether the server told us anything at all.
func (r RateLimit) Known() bool { return r.Limit > 0 }

// Response is one completed call.
type Response struct {
	Status    int
	Body      []byte
	RequestID string
	Rate      RateLimit
}

// Client talks to one API base URL with one key.
type Client struct {
	BaseURL   string
	Key       string
	UserAgent string
	HTTP      *http.Client

	// LastRate is updated after every call so a caller can pace itself
	// against the real budget rather than a guess.
	LastRate RateLimit

	// Extra carries additional request headers, used by the escape-hatch
	// command. Authorization is set from Key and cannot be overridden here.
	Extra [][2]string
}

// New builds a client with sane timeouts.
func New(baseURL, key, userAgent string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		Key:       key,
		UserAgent: userAgent,
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Do performs one request. path is relative to the base URL and must begin
// with a slash. body may be nil.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body any) (*Response, error) {
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, clierr.Wrap(err, clierr.CodeUnexpected, "could not encode the request body")
		}
		rdr = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, clierr.Wrap(err, clierr.CodeUnexpected, "could not build the request")
	}
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	for _, h := range c.Extra {
		if strings.EqualFold(h[0], "Authorization") {
			continue
		}
		// Add, not Set: a header may legitimately repeat (Cookie, Link,
		// Accept), and Set silently kept only the last one.
		req.Header.Add(h[0], h[1])
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		// A cancelled request is the user pressing Ctrl-C, not a broken
		// network. Reporting it as unreachable sends them to debug DNS.
		if ctxErr := ctx.Err(); ctxErr != nil {
			if errors.Is(ctxErr, context.DeadlineExceeded) {
				return nil, clierr.Wrap(err, CodeTimeout,
					fmt.Sprintf("%s did not answer in time", c.BaseURL))
			}
			return nil, clierr.Wrap(err, CodeCancelled, "interrupted").WithExit(ExitInterrupted)
		}
		// http.Client.Timeout uses its own timer and never touches the
		// caller's context, so a request that ran out of time arrived here as
		// a plain error and was reported as an unreachable host. That sends
		// the user to debug DNS for a server that answered too slowly.
		var nerr net.Error
		if errors.As(err, &nerr) && nerr.Timeout() {
			return nil, clierr.Wrap(err, CodeTimeout,
				fmt.Sprintf("%s did not answer within %s", c.BaseURL, c.HTTP.Timeout))
		}
		return nil, clierr.Wrap(err, clierr.CodeNetwork,
			fmt.Sprintf("could not reach %s", c.BaseURL))
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, clierr.Wrap(err, clierr.CodeNetwork, "the connection dropped mid-response")
	}
	if len(b) > maxBody {
		return nil, clierr.Fail(clierr.CodeServer,
			"the response was larger than this client will read")
	}

	out := &Response{
		Status:    resp.StatusCode,
		Body:      b,
		RequestID: resp.Header.Get("X-Request-ID"),
		Rate:      parseRate(resp.Header),
	}
	c.LastRate = out.Rate

	if resp.StatusCode >= 400 {
		return out, c.asError(out, resp.Header)
	}
	return out, nil
}

func parseRate(h http.Header) RateLimit {
	var r RateLimit
	if v := h.Get("X-RateLimit-Limit"); v != "" {
		r.Limit, _ = strconv.Atoi(v)
	}
	if v := h.Get("X-RateLimit-Remaining"); v != "" {
		r.Remaining, _ = strconv.Atoi(v)
	}
	if v := h.Get("X-RateLimit-Reset"); v != "" {
		// Documented as "seconds until the window frees, not a timestamp".
		// Guessing between the two meant a large value was read as an epoch
		// and produced a wait in the past.
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			r.ResetAt = time.Now().Add(time.Duration(n) * time.Second)
		}
	}
	return r
}

// apiError is the documented failure shape: a stable code, human prose that
// may be reworded at any time, and the request id.
type apiError struct {
	Code      string `json:"code"`
	Error     string `json:"error"`
	RequestID string `json:"request_id"`
	// Hint is the API's own one-sentence "what to do next", documented for
	// not_found and method_not_allowed. Discarding it threw away the only
	// actionable half of those errors.
	Hint string `json:"hint"`
	Docs string `json:"docs"`
}

func (c *Client) asError(r *Response, h http.Header) error {
	var ae apiError
	_ = json.Unmarshal(r.Body, &ae)

	code := ae.Code
	msg := ae.Error
	reqID := ae.RequestID
	if reqID == "" {
		reqID = r.RequestID
	}

	// Everything the server offered as a next step, in the order a reader
	// would want it.
	var steps []string
	if ae.Hint != "" {
		steps = append(steps, ae.Hint)
	}
	if allow := h.Get("Allow"); allow != "" {
		steps = append(steps, "this path accepts "+allow)
	}
	if ae.Docs != "" {
		steps = append(steps, ae.Docs)
	}

	switch {
	case r.Status == http.StatusUnauthorized:
		e := clierr.ActionRequired(clierr.CodeNotAuthenticated,
			"the API key was rejected",
			append(steps, "statable auth login", "statable auth status")...)
		e.Envelope.RequestID = reqID
		return e

	case r.Status == http.StatusTooManyRequests:
		e := clierr.ActionRequired(clierr.CodeRateLimited, rateMessage(r.Rate, ae.Error))
		e.Envelope.RequestID = reqID
		if wait := retryAfter(r.Rate, h); wait > 0 {
			e.Envelope.NextSteps = []string{
				fmt.Sprintf("wait %s, then run the same command again", wait.Round(time.Second)),
			}
		}
		return e

	case r.Status >= 500:
		if code == "" {
			code = clierr.CodeServer
		}
		if msg == "" {
			msg = fmt.Sprintf("the server returned %d", r.Status)
		}
		e := clierr.Fail(code, msg)
		e.Envelope.RequestID = reqID
		e.Envelope.NextSteps = steps
		if reqID != "" {
			e.Envelope.NextSteps = append(e.Envelope.NextSteps,
				"quote the request id above when reporting this")
		}
		return e
	}

	// 4xx other than 401 and 429: the request itself was wrong. Nothing ran,
	// and repeating it unchanged will fail identically. The API's own code
	// passes through untouched.
	if code == "" {
		code = fmt.Sprintf("http_%d", r.Status)
	}
	if msg == "" {
		msg = fmt.Sprintf("the request was rejected with %d", r.Status)
	}

	// A wrong site, an unscoped key or a missing funnel are things the caller
	// fixes by changing the command, so they are action_required and exit 1.
	// Reporting them as a failure made a script page a human for a typo, and
	// disagreed with the identical error raised locally.
	status := clierr.StatusError
	switch r.Status {
	case http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed,
		http.StatusConflict, http.StatusUnprocessableEntity:
		status = clierr.StatusActionRequired
	}

	e := &clierr.Err{Envelope: clierr.Envelope{
		Status:    status,
		Error:     msg,
		Issues:    []clierr.Issue{{Code: code, Message: msg}},
		NextSteps: steps,
		RequestID: reqID,
	}}
	return e
}

// rateMessage keeps whatever the server said about which budget ran out.
// errors.md documents that field as naming the write, account or api key
// budget, and replacing it with a generic sentence discarded the one detail
// that tells the caller what to change.
func rateMessage(r RateLimit, serverSaid string) string {
	switch {
	case serverSaid != "" && r.Known():
		return fmt.Sprintf("rate limited: %s (hourly budget %d)", serverSaid, r.Limit)
	case serverSaid != "":
		return "rate limited: " + serverSaid
	case r.Known():
		return fmt.Sprintf("rate limited: the hourly budget of %d is spent", r.Limit)
	}
	return "rate limited"
}

// retryAfter prefers the explicit header, then the reset timestamp.
func retryAfter(r RateLimit, h http.Header) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
		if t, err := http.ParseTime(v); err == nil {
			if d := time.Until(t); d > 0 {
				return d
			}
		}
	}
	if !r.ResetAt.IsZero() {
		if d := time.Until(r.ResetAt); d > 0 {
			return d
		}
	}
	return 0
}

// Get is a convenience wrapper.
func (c *Client) Get(ctx context.Context, path string, query url.Values) (*Response, error) {
	return c.Do(ctx, http.MethodGet, path, query, nil)
}

// Post is a convenience wrapper.
func (c *Client) Post(ctx context.Context, path string, body any) (*Response, error) {
	return c.Do(ctx, http.MethodPost, path, nil, body)
}

// Put replaces a resource whole. The API means it literally: a field left out
// of the body is not left alone, it is cleared. Callers say so in their help.
func (c *Client) Put(ctx context.Context, path string, body any) (*Response, error) {
	return c.Do(ctx, http.MethodPut, path, nil, body)
}

// Patch changes only the fields it carries.
func (c *Client) Patch(ctx context.Context, path string, body any) (*Response, error) {
	return c.Do(ctx, http.MethodPatch, path, nil, body)
}

// Delete removes a resource. It takes no body: every delete in this API is
// addressed entirely by its path.
func (c *Client) Delete(ctx context.Context, path string) (*Response, error) {
	return c.Do(ctx, http.MethodDelete, path, nil, nil)
}

// DecodeInto unmarshals a successful response body.
func DecodeInto(r *Response, v any) error {
	if err := json.Unmarshal(r.Body, v); err != nil {
		return clierr.Wrap(err, clierr.CodeServer, "the server sent a response this client could not read")
	}
	return nil
}

// CodeCancelled marks a request the caller interrupted, and CodeTimeout one
// the server did not answer in time. Both used to be reported as an
// unreachable host, which sends the user to debug the wrong thing.
const (
	CodeCancelled = "CANCELLED"
	CodeTimeout   = "TIMEOUT"
)

// ExitInterrupted is the conventional shell status for a process ended by a
// signal: 128 plus the signal number. The caller decides which signal it was,
// because this package cannot tell SIGINT from SIGTERM and reporting a CI
// runner's own timeout as "the operator pressed Ctrl-C" is a lie.
var ExitInterrupted = 130

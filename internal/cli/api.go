package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/api"
	"github.com/key-arg/statable-cli/internal/clierr"
	"github.com/key-arg/statable-cli/internal/execctx"
	"github.com/key-arg/statable-cli/internal/openapi"
	"github.com/key-arg/statable-cli/internal/output"
)

func newAPICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "api",
		Short: "Call any endpoint directly",
		Long: "The escape hatch. Every endpoint is reachable here, including ones the\n" +
			"shorter commands do not wrap, and the response is passed through exactly\n" +
			"as the server sent it.\n\n" +
			"`api search` and `api describe` read the API's own OpenAPI document, so\n" +
			"this command can teach its own surface rather than requiring a second\n" +
			"window with the reference open.",
		RunE: func(c *cobra.Command, args []string) error { return helpOrUsage(c) },
	}
	cmd.AddCommand(newAPICallCmd(), newAPISearchCmd(), newAPIDescribeCmd())
	return cmd
}

func newAPICallCmd() *cobra.Command {
	var vars, rawVars, headers []string
	var body string
	var allowErrors bool

	cmd := &cobra.Command{
		Use:   "call <METHOD> <path>",
		Short: "Send one request and print the response",
		Long: "Send one request to the API.\n\n" +
			"`--var` values are parsed as JSON, so `--var limit=25` sends a number and\n" +
			"`--var metrics='[\"visitors\"]'` sends an array. `--raw-var` always sends a\n" +
			"string, which is how you send the literal text \"25\". Guessing between the\n" +
			"two is exactly the ambiguity these two flags exist to remove.\n\n" +
			"Examples:\n" +
			"  statable api call GET /sites\n" +
			"  statable api call POST /query --var site_id=123 --var metrics='[\"visitors\"]' --var date_range=7d\n" +
			"  statable api call GET /current-visitors --var site_id=123",
		Args:    cobra.RangeArgs(1, 2),
		Aliases: []string{"do"},
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())

			method, path := http.MethodGet, args[0]
			if len(args) == 2 {
				method, path = strings.ToUpper(args[0]), args[1]
				if !isMethod(method) {
					// Nothing was sent, so this is a usage error rather than
					// a failure; the transport would otherwise reject it with
					// a message naming nothing the user can act on.
					return clierr.Failf("BAD_METHOD",
						"%q is not an HTTP method", args[0]).WithExit(clierr.ExitUsage)
				}
			} else if m := strings.ToUpper(args[0]); isMethod(m) {
				return clierr.Failf("NO_PATH", "%s needs a path", m).WithExit(clierr.ExitUsage)
			}
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}

			payload, err := buildVars(vars, rawVars)
			if err != nil {
				return err
			}
			var explicitBody any
			if body != "" {
				if len(payload) > 0 {
					return clierr.Fail("BODY_CONFLICT",
						"--body and --var set the same thing; use one of them").
						WithExit(clierr.ExitUsage)
				}
				explicitBody, err = readBody(body)
				if err != nil {
					return err
				}
			}

			// A GET carries its variables in the query string; anything with a
			// body carries them there. Sending a body on a GET is a good way
			// to have it silently dropped by something in between.
			var q url.Values
			var reqBody any
			if method == http.MethodGet || method == http.MethodHead {
				q = url.Values{}
				for k, v := range payload {
					q.Set(k, queryValue(v))
				}
			} else if explicitBody != nil {
				reqBody = explicitBody
			} else if len(payload) > 0 {
				reqBody = payload
			}

			client, err := rt.Client(cmd.Context())
			if err != nil {
				return err
			}
			for _, h := range headers {
				k, v, ok := strings.Cut(h, ":")
				if !ok {
					return clierr.Failf("BAD_HEADER", "header %q needs a colon", h).
						WithExit(clierr.ExitUsage)
				}
				k, v = strings.TrimSpace(k), strings.TrimSpace(v)
				// A newline in a header is a request-splitting attempt, and
				// the transport rejects it with a generic failure that reads
				// as "the network is down". Saying what is actually wrong is
				// the difference between a one-second fix and an hour.
				if !validHeaderName(k) {
					return clierr.Failf("BAD_HEADER",
						"%q is not a valid header name", k).WithExit(clierr.ExitUsage)
				}
				if strings.ContainsAny(v, "\r\n") {
					return clierr.Failf("BAD_HEADER",
						"the value of %s contains a line break", k).WithExit(clierr.ExitUsage)
				}
				client.Extra = append(client.Extra, [2]string{k, v})
			}

			resp, err := client.Do(cmd.Context(), method, path, q, reqBody)
			rt.traceResponse(resp)

			if err != nil && !allowErrors {
				return err
			}
			if resp == nil {
				return err
			}
			// --allow-errors separates "the transport worked" from "the
			// operation failed": the body is printed so a caller can inspect
			// a 400 without the command refusing to show it.
			if werr := rt.Out.Raw(withNewline(resp.Body)); werr != nil {
				return werr
			}
			if err != nil {
				// The classified error is returned unchanged. Flattening it
				// here would cost the caller the stable code, the request id
				// and, worse, the exit code: a 401 or a 429 is recoverable
				// and must stay exit 1, or a script that retries on 1 and
				// pages a human on 2 pages a human for an expired key.
				e := clierr.From(err)
				e.Reported = true
				return e
			}
			if resp.Status >= 400 {
				rt.Out.Note("HTTP %d", resp.Status)
				return clierr.Silent(clierr.ExitError)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&vars, "var", nil, "key=value, parsed as JSON when it looks like JSON")
	f.StringArrayVar(&rawVars, "raw-var", nil, "key=value, always sent as a string")
	f.StringArrayVarP(&headers, "header", "H", nil, "extra request header, as Name: value")
	f.StringVar(&body, "body", "", "request body as JSON, or @file, or - for stdin")
	f.BoolVar(&allowErrors, "allow-errors", false,
		"print the response body and keep going when the server answers 4xx or 5xx")
	return cmd
}

func isMethod(s string) bool {
	switch s {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

func withNewline(b []byte) []byte {
	if len(b) == 0 || b[len(b)-1] == '\n' {
		return b
	}
	return append(b, '\n')
}

// buildVars turns the two flags into one body.
//
// --var is parsed as JSON so a number stays a number and a list stays a list;
// --raw-var never is. Without the split, "is count=5 a number or the text 5"
// has no answer the caller can give.
func buildVars(vars, rawVars []string) (map[string]any, error) {
	out := map[string]any{}
	for _, kv := range rawVars {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, clierr.Failf("BAD_VAR", "--raw-var %q needs key=value", kv).
				WithExit(clierr.ExitUsage)
		}
		out[k] = v
	}
	for _, kv := range vars {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, clierr.Failf("BAD_VAR", "--var %q needs key=value", kv).
				WithExit(clierr.ExitUsage)
		}
		if _, clash := out[k]; clash {
			// These two flags exist to remove the ambiguity about a value's
			// type. Letting one silently overwrite the other puts it back.
			return nil, clierr.Failf("BAD_VAR",
				"%q is set by both --var and --raw-var; pick one", k).
				WithExit(clierr.ExitUsage)
		}
		var parsed any
		if err := json.Unmarshal([]byte(v), &parsed); err == nil {
			out[k] = parsed
		} else {
			// Not valid JSON, so it is a bare string. This is the common case
			// and does not deserve an error: --var date_range=7d should work.
			out[k] = v
		}
	}
	return out, nil
}

// maxBodyBytes caps a request body from either source. Both paths are capped
// and both say so: stdin used to be truncated at the cap and the result then
// reported as invalid JSON, which is false and sends the user to debug their
// own file, and a file had no cap at all — a 60 MB one cost 267 MB of memory.
const maxBodyBytes = 8 << 20

func readBody(spec string) (any, error) {
	var raw []byte
	var err error
	switch {
	case spec == "-":
		raw, err = io.ReadAll(io.LimitReader(os.Stdin, maxBodyBytes+1))
	case strings.HasPrefix(spec, "@"):
		raw, err = readCappedFile(strings.TrimPrefix(spec, "@"))
	default:
		raw = []byte(spec)
	}
	if err != nil {
		return nil, clierr.Wrap(err, "BAD_BODY", "could not read the request body")
	}
	if len(raw) > maxBodyBytes {
		return nil, clierr.Failf("BAD_BODY",
			"the request body is larger than the %d MiB this client will send",
			maxBodyBytes>>20)
	}
	// Any JSON value is accepted, not only an object: an endpoint taking an
	// array body is unreachable otherwise, and the command promises every
	// endpoint is reachable.
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, clierr.Wrap(err, "BAD_BODY", "the request body is not valid JSON")
	}
	return out, nil
}

func readCappedFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxBodyBytes+1))
}

// specTTL bounds how long a cached OpenAPI document is reused. The document
// changes with a release, not with a request, and refetching it on every
// `api search` would spend the caller's hourly budget on documentation.
const specTTL = 24 * time.Hour

// specPath keys the cache by the API it describes. One shared filename meant
// a staging description was served for production, and the wrong parameter
// set for `api describe`, for up to a day with no indication.
func (r *Runtime) specPath() string {
	sum := sha256.Sum256([]byte(r.baseURL))
	return filepath.Join(r.Store.ConfigDir, "openapi-"+hex.EncodeToString(sum[:8])+".yaml")
}

// refreshSpec is set by --refresh on the search and describe commands.
var refreshSpec bool

func (r *Runtime) loadSpec(ctx context.Context) (*openapi.Spec, error) {
	if refreshSpec {
		os.Remove(r.specPath())
	}
	// A negative age means the file claims to be from the future — a clock
	// that stepped back, a restored backup, an archive with preserved
	// timestamps. Treating that as fresh pinned the cache permanently.
	if fi, err := os.Stat(r.specPath()); err == nil {
		if age := time.Since(fi.ModTime()); age >= 0 && age < specTTL {
			if b, err := os.ReadFile(r.specPath()); err == nil {
				if s, err := openapi.Parse(b); err == nil {
					return s, nil
				}
				r.Out.Note("%s could not be read; fetching the description again", r.specPath())
			}
		}
	}
	if refreshSpec {
		r.Out.Note("ignoring the cached description")
	}

	client, err := r.Client(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := client.Get(ctx, "/openapi.yaml", nil)
	r.traceResponse(resp)
	if err != nil {
		return nil, err
	}
	spec, err := openapi.Parse(resp.Body)
	if err != nil {
		return nil, clierr.Wrap(err, clierr.CodeServer,
			"the API description could not be read")
	}

	if err := r.cacheSpec(resp.Body); err != nil && r.Ctx.Verbose {
		// Not fatal: the description was fetched and is usable. But a cache
		// that silently never works spends the hourly budget on documentation
		// on every invocation, which is the cost the cache exists to avoid.
		r.Out.Note("the description could not be cached: %v", err)
	}
	return spec, nil
}

func addRefreshFlag(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&refreshSpec, "refresh", false,
		"fetch the API description again instead of using the cached copy")
}

func newAPISearchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search [term]",
		Short: "Find the endpoints that mention a word",
		Long: "Search the API's own description.\n\n" +
			"With no term it lists everything, which makes it a table of contents.\n" +
			"The document is cached for a day, so repeated searches cost nothing.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			spec, err := rt.loadSpec(cmd.Context())
			if err != nil {
				return err
			}
			term := ""
			if len(args) == 1 {
				term = args[0]
			}
			found := spec.Search(term)

			t := output.Table{Columns: []string{"method", "path", "summary"}}
			for _, op := range found {
				t.Rows = append(t.Rows, []string{op.Method, op.Path, op.Summary})
			}
			if len(t.Rows) == 0 {
				rt.Out.Note("nothing matches %q", term)
			}
			return rt.Out.Emit(t)
		},
	}
	addRefreshFlag(cmd)
	return cmd
}

func newAPIDescribeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "describe <path>",
		Short: "Show what one endpoint takes",
		Long: "Describe one endpoint: its parameters and the fields of its request\n" +
			"body, with the required ones first.\n\n" +
			"The path may be given with or without a method and with or without the\n" +
			"leading slash: `/query`, `query` and `POST /query` all work.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := From(cmd.Context())
			spec, err := rt.loadSpec(cmd.Context())
			if err != nil {
				return err
			}
			op, ok := spec.Find(args[0])
			if !ok {
				msg := fmt.Sprintf("no endpoint called %q", args[0])
				steps := []string{"statable api search"}
				// The path may exist under a different method, which is the
				// least useful thing to answer with "it does not exist".
				if others := spec.MethodsFor(args[0]); len(others) > 0 {
					msg = fmt.Sprintf("%s exists, but only as %s",
						pathOf(args[0]), strings.Join(others, ", "))
					steps = []string{"statable api describe " + others[0] + " " + pathOf(args[0])}
				} else if near := spec.Search(strings.TrimPrefix(args[0], "/")); len(near) > 0 {
					steps = append(steps, "statable api describe "+near[0].Path)
				}
				return &clierr.Err{Envelope: clierr.Envelope{
					Status:    clierr.StatusActionRequired,
					Error:     msg,
					Issues:    []clierr.Issue{{Code: "UNKNOWN_ENDPOINT"}},
					NextSteps: steps,
				}}
			}

			// CSV is a table of parameters, not a re-encoded object: asking
			// for CSV and receiving JSON is the contract break Record exists
			// to prevent, and this was the one command still doing it.
			if rt.Ctx.Format == execctx.FormatJSON {
				return rt.Out.JSON(op)
			}
			if rt.Ctx.Format == execctx.FormatCSV {
				return rt.Out.Emit(paramTable(op, false))
			}

			w := rt.Out.Human()
			fmt.Fprintf(w, "%s\n", output.Sanitize(op.Ref()))
			if op.Summary != "" {
				fmt.Fprintf(w, "%s\n", output.Sanitize(op.Summary))
			}
			if len(op.Params)+len(op.BodyFields) == 0 {
				fmt.Fprintln(w, "\ntakes no parameters")
				return nil
			}
			fmt.Fprintln(w)
			return rt.Out.Emit(paramTable(op, true))
		},
	}
	addRefreshFlag(cmd)
	return cmd
}

// ensure the api package stays imported for the response type.
var _ = api.Response{}

// firstSentence trims a long description down to something that fits a table
// cell, preferring a sentence boundary and falling back to a word one.
func firstSentence(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	if i := strings.Index(s, ". "); i > 0 && i+1 <= max {
		return s[:i+1]
	}
	// Cut on a rune boundary: byte-slicing a description in Cyrillic, CJK or
	// with an emoji produces invalid UTF-8 in the table.
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	cut := string(r[:max])
	if i := strings.LastIndex(cut, " "); i > len(cut)/2 {
		cut = cut[:i]
	}
	return cut + "…"
}

// validHeaderName reports whether s is a token per RFC 9110. Anything else
// cannot be sent and is worth refusing by name.
func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	const specials = "!#$%&'*+-.^_`|~"
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune(specials, r):
		default:
			return false
		}
	}
	return true
}

// queryValue renders a --var value for a query string.
//
// %v prints Go syntax, not JSON: an array became "[a b]", an object became
// "map[a:1]" and null became "<nil>", silently and with a zero exit code.
// A string is sent as itself so date_range=7d stays 7d; anything else is
// re-encoded as the JSON it was parsed from.
func queryValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// paramTable renders an operation's parameters. The human form trims each
// description to a first sentence, because the table is an index and not the
// reference; CSV keeps the description whole, because a machine reader is not
// scanning a screen.
func paramTable(op openapi.Operation, human bool) output.Table {
	all := append(append([]openapi.Param{}, op.Params...), op.BodyFields...)
	t := output.Table{Columns: []string{"name", "in", "type", "required", "description"}}
	for _, p := range all {
		req := "no"
		if p.Required {
			req = "yes"
		}
		desc := p.Description
		if human {
			desc = firstSentence(desc, 110)
		}
		t.Rows = append(t.Rows, []string{p.Name, p.In, p.Type, req, desc})
	}
	return t
}

func (r *Runtime) cacheSpec(body []byte) error {
	if err := os.MkdirAll(r.Store.ConfigDir, 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", r.specPath(), os.Getpid())
	// The scratch file is removed on every failure path. os.WriteFile creates
	// it before writing, so a write that fails partway leaves a partial file
	// that nothing else would ever clean up.
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, r.specPath()); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// pathOf strips a leading method from a reference the way Find does.
func pathOf(ref string) string {
	ref = strings.TrimSpace(ref)
	if _, b, found := strings.Cut(ref, " "); found {
		ref = strings.TrimSpace(b)
	}
	if !strings.HasPrefix(ref, "/") {
		ref = "/" + ref
	}
	return ref
}

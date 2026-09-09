package openapi

import (
	"strings"
	"testing"
)

// FuzzParse drives the OpenAPI reader with arbitrary bytes.
//
// The document comes from the network, so anything it can make the reader do
// is something a server can make the CLI do. A panic here is a crash on a
// response; unbounded recursion is worse, because a stack overflow cannot be
// recovered from at all.
func FuzzParse(f *testing.F) {
	f.Add("")
	f.Add("paths: {}")
	f.Add("paths: {/x: {get: {summary: s}}}")
	f.Add(`paths: {/x: {post: {requestBody: {content: {application/json: {schema: {$ref: "#/components/schemas/A"}}}}}}}
components: {schemas: {A: {type: array, items: {$ref: "#/components/schemas/A"}}}}`)
	f.Add(`paths: {/x: {get: {parameters: [{name: q, in: query, schema: {oneOf: [{$ref: "#/components/schemas/A"}]}}]}}}
components: {schemas: {A: {oneOf: [{$ref: "#/components/schemas/A"}]}}}`)
	f.Add("paths: [1, 2, 3]")
	f.Add("paths: {/x: {get: 5}}")
	f.Add("\x00\x01\x02")
	f.Add(strings.Repeat("a: b\n", 200))

	f.Fuzz(func(t *testing.T, in string) {
		s, err := Parse([]byte(in))
		if err != nil {
			return
		}
		if s == nil {
			t.Fatal("no error and no spec")
		}
		// Whatever comes back has to be internally consistent: every
		// operation reachable by the accessors the CLI actually calls.
		for _, op := range s.Operations {
			if op.Method == "" || op.Path == "" {
				t.Fatalf("operation with no method or path: %+v", op)
			}
			if !strings.HasPrefix(op.Path, "/") {
				t.Fatalf("path %q does not start with a slash", op.Path)
			}
			if _, ok := s.Find(op.Method + " " + op.Path); !ok {
				t.Fatalf("listed operation %s is not findable", op.Ref())
			}
			if len(s.MethodsFor(op.Path)) == 0 {
				t.Fatalf("listed operation %s reports no methods for its path", op.Ref())
			}
		}
		// Search over the whole document must terminate and stay a subset.
		if got := len(s.Search("")); got != len(s.Operations) {
			t.Fatalf("an empty search returned %d of %d", got, len(s.Operations))
		}
		if got := len(s.Search("zzzz-no-such-term")); got > len(s.Operations) {
			t.Fatal("search returned more than exists")
		}
	})
}

package openapi

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// parseWithin runs Parse with a deadline. A description is fetched from the
// network, so a document that makes the reader hang or allocate without bound
// would be a denial of service triggered by whatever the server sent.
func parseWithin(t *testing.T, name, doc string) *Spec {
	t.Helper()
	type result struct {
		s   *Spec
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := Parse([]byte(doc))
		ch <- result{s, err}
	}()
	select {
	case r := <-ch:
		return r.s
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: parsing did not finish", name)
		return nil
	}
}

// TestHostileDocuments walks the shapes a description could take that are not
// the shape the reader expects. None may panic, hang, or be treated as fatal:
// a document that grew something this version has never seen is still useful.
func TestHostileDocuments(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{"external ref", `paths: {/x: {post: {requestBody: {content: {application/json: {schema: {$ref: "https://evil.example/#/x"}}}}}}}`},
		{"ref to nowhere", `paths: {/x: {post: {requestBody: {content: {application/json: {schema: {$ref: "#/components/schemas/Nope"}}}}}}}`},
		{"ref into a scalar", `
paths: {/x: {post: {requestBody: {content: {application/json: {schema: {$ref: "#/components/schemas/A"}}}}}}}
components: {schemas: {A: "just a string"}}`},
		{"paths is a list", "paths:\n  - /x\n  - /y"},
		{"paths is a scalar", "paths: 42"},
		{"empty document", ""},
		{"scalar document", "42"},
		{"no paths at all", "info: {title: t}"},
		{"method is a scalar", `paths: {/x: {get: 12345}}`},
		{"parameters is a scalar", `paths: {/x: {get: {parameters: "nope"}}}`},
		{"body without properties", `paths: {/x: {post: {requestBody: {content: {application/json: {schema: {type: object}}}}}}}`},
		{"allOf composition", `paths: {/x: {post: {requestBody: {content: {application/json: {schema: {allOf: [{type: object, properties: {a: {type: string}}}]}}}}}}}`},
		{"mutual refs", `
paths: {/x: {post: {requestBody: {content: {application/json: {schema: {$ref: "#/components/schemas/A"}}}}}}}
components: {schemas: {A: {type: object, properties: {b: {$ref: "#/components/schemas/B"}}}, B: {type: object, properties: {a: {$ref: "#/components/schemas/A"}}}}}`},
		{"anchors and aliases", `
x: &a ["a","a","a","a","a","a","a","a"]
y: &b [*a,*a,*a,*a,*a,*a,*a,*a]
z: &c [*b,*b,*b,*b,*b,*b,*b,*b]
paths: {}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parseWithin(t, tc.name, tc.doc)
		})
	}
}

// TestDeepRefChainTerminates: a chain of references longer than the depth cap
// must stop the walk, not the process.
func TestDeepRefChainTerminates(t *testing.T) {
	var b strings.Builder
	b.WriteString("paths: {/x: {post: {requestBody: {content: {application/json: {schema: {$ref: \"#/components/schemas/S0\"}}}}}}}\ncomponents:\n  schemas:\n")
	const depth = 200
	for i := 0; i < depth; i++ {
		fmt.Fprintf(&b, "    S%d: {$ref: \"#/components/schemas/S%d\"}\n", i, i+1)
	}
	fmt.Fprintf(&b, "    S%d: {type: object, properties: {a: {type: string}}}\n", depth)

	s := parseWithin(t, "deep chain", b.String())
	if s == nil || len(s.Operations) != 1 {
		t.Fatalf("the operation itself must survive a chain too deep to follow")
	}
}

// TestManyPathsIsLinear: a large description must not become quadratic.
func TestManyPathsIsLinear(t *testing.T) {
	build := func(n int) string {
		var b strings.Builder
		b.WriteString("paths:\n")
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "  /p%d: {get: {summary: s%d}}\n", i, i)
		}
		return b.String()
	}
	s := parseWithin(t, "many paths", build(5000))
	if s == nil || len(s.Operations) != 5000 {
		t.Fatalf("operations = %d, want 5000", len(s.Operations))
	}
	// Search over the whole set must also finish.
	if got := len(s.Search("s4999")); got != 1 {
		t.Fatalf("search over a large document returned %d", got)
	}
}

// TestExternalRefIsNotFetched: following a remote pointer would mean fetching
// a second document, chosen by the first, to describe the API.
func TestExternalRefIsNotFetched(t *testing.T) {
	s := parseWithin(t, "external", `
paths: {/x: {post: {summary: s, requestBody: {content: {application/json: {schema: {$ref: "https://evil.example/spec.yaml#/components/schemas/A"}}}}}}}`)
	op, ok := s.Find("POST /x")
	if !ok {
		t.Fatal("the operation must still be listed")
	}
	if len(op.BodyFields) != 0 {
		t.Fatalf("a remote reference must resolve to nothing, got %+v", op.BodyFields)
	}
}

// TestCyclicRefChainTerminates. A chain of pure $ref nodes that loops back on
// itself recurses forever without the depth cap, and a stack overflow is not
// recoverable: the whole process dies on a document the server chose.
//
// The mutual-reference case elsewhere in this file does not exercise the cap,
// because those schemas carry properties and the recursion is bounded by the
// property walk instead. Only a ref pointing at a ref reaches it.
func TestCyclicRefChainTerminates(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
	}{
		{"two links", `
paths: {/x: {post: {requestBody: {content: {application/json: {schema: {$ref: "#/components/schemas/A"}}}}}}}
components: {schemas: {A: {$ref: "#/components/schemas/B"}, B: {$ref: "#/components/schemas/A"}}}`},
		{"self", `
paths: {/x: {post: {requestBody: {content: {application/json: {schema: {$ref: "#/components/schemas/A"}}}}}}}
components: {schemas: {A: {$ref: "#/components/schemas/A"}}}`},
		{"three links", `
paths: {/x: {post: {requestBody: {content: {application/json: {schema: {$ref: "#/components/schemas/A"}}}}}}}
components: {schemas: {A: {$ref: "#/components/schemas/B"}, B: {$ref: "#/components/schemas/C"}, C: {$ref: "#/components/schemas/A"}}}`},
		{"cycle in a parameter schema", `
paths: {/x: {get: {parameters: [{name: q, in: query, schema: {$ref: "#/components/schemas/A"}}]}}}
components: {schemas: {A: {$ref: "#/components/schemas/A"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := parseWithin(t, tc.name, tc.doc)
			if s == nil || len(s.Operations) != 1 {
				t.Fatalf("the operation must survive a cycle it cannot follow")
			}
		})
	}
}

// TestCycleThroughArrayOrUnionTerminates.
//
// deref's depth cap is a balanced counter — it returns to zero when deref
// returns — so it bounds a chain of pure $ref nodes and nothing else. A cycle
// reached through an array's items or a union recurses on schemaType's own
// stack, and a stack overflow is not a panic: recover cannot catch it and the
// process dies. A recursive array schema is legal, idiomatic OpenAPI, so this
// is not only a hostile-input concern.
//
// The cycle tests above all go through deref and therefore never touched this
// path, which is why it survived three review passes.
func TestCycleThroughArrayOrUnionTerminates(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{"array items point at themselves", `
paths: {/x: {get: {parameters: [{name: q, in: query, schema: {$ref: "#/components/schemas/A"}}]}}}
components: {schemas: {A: {type: array, items: {$ref: "#/components/schemas/A"}}}}`},
		{"union points at itself", `
paths: {/x: {get: {parameters: [{name: q, in: query, schema: {$ref: "#/components/schemas/A"}}]}}}
components: {schemas: {A: {oneOf: [{$ref: "#/components/schemas/A"}]}}}`},
		{"two arrays point at each other", `
paths: {/x: {get: {parameters: [{name: q, in: query, schema: {$ref: "#/components/schemas/A"}}]}}}
components: {schemas: {A: {type: array, items: {$ref: "#/components/schemas/B"}}, B: {type: array, items: {$ref: "#/components/schemas/A"}}}}`},
		{"cycle in a body field type", `
paths: {/x: {post: {requestBody: {content: {application/json: {schema: {type: object, properties: {tree: {$ref: "#/components/schemas/A"}}}}}}}}}
components: {schemas: {A: {type: array, items: {$ref: "#/components/schemas/A"}}}}`},
		{"anyOf chain through an array", `
paths: {/x: {get: {parameters: [{name: q, in: query, schema: {$ref: "#/components/schemas/A"}}]}}}
components: {schemas: {A: {anyOf: [{type: array, items: {$ref: "#/components/schemas/A"}}, {type: string}]}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := parseWithin(t, tc.name, tc.doc)
			if s == nil || len(s.Operations) != 1 {
				t.Fatalf("the operation must survive a cycle it cannot follow")
			}
		})
	}
}

// TestNestedArraysStillRender: the depth cap must not turn an ordinary nested
// type into nothing.
func TestNestedArraysStillRender(t *testing.T) {
	s := parseWithin(t, "nested", `
paths:
  /x:
    get:
      parameters:
        - name: q
          in: query
          schema:
            type: array
            items:
              type: array
              items: { type: string }`)
	op, _ := s.Find("GET /x")
	if len(op.Params) != 1 {
		t.Fatalf("params = %+v", op.Params)
	}
	if got := op.Params[0].Type; got != "array of array of string" {
		t.Fatalf("type = %q", got)
	}
}

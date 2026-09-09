package openapi

import (
	"strings"
	"testing"
)

const doc = `
openapi: 3.1.0
info:
  title: Test API
  version: "1.0.0"
paths:
  /sites:
    get:
      tags: [Stats]
      summary: List the sites a key can read
      parameters:
        - name: date_range
          in: query
          required: false
          schema: { type: string }
          description: Adds headline numbers
  /query:
    post:
      tags: [Stats]
      summary: Run one analytics query
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/QueryRequest"
  /funnels/{id}/report:
    parameters:
      - name: id
        in: path
        required: true
        schema: { type: integer }
    post:
      summary: Run a saved funnel
components:
  schemas:
    QueryRequest:
      type: object
      required: [metrics, date_range]
      properties:
        site_id:
          type: integer
          description: Required on an all-sites key
        metrics:
          type: array
          items: { type: string }
        date_range:
          oneOf:
            - type: string
            - type: array
              items: { type: string }
        filters:
          type: array
          items:
            $ref: "#/components/schemas/Filter"
    Filter:
      type: object
      properties:
        field: { type: string }
`

func parse(t *testing.T) *Spec {
	t.Helper()
	s, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestRefsAreFollowed. A specification names its shapes once under components
// and points at them. A reader that does not follow a $ref reported that
// POST /query "takes no parameters", which is the opposite of true.
func TestRefsAreFollowed(t *testing.T) {
	op, ok := parse(t).Find("POST /query")
	if !ok {
		t.Fatal("POST /query not found")
	}
	if len(op.BodyFields) == 0 {
		t.Fatal("the request body was not resolved through its $ref")
	}
	byName := map[string]Param{}
	for _, f := range op.BodyFields {
		byName[f.Name] = f
	}
	if !byName["metrics"].Required || !byName["date_range"].Required {
		t.Fatalf("required fields lost: %+v", op.BodyFields)
	}
	if byName["site_id"].Required {
		t.Fatal("an optional field must not be marked required")
	}
	if got := byName["metrics"].Type; got != "array of string" {
		t.Fatalf("metrics type = %q", got)
	}
	if got := byName["date_range"].Type; !strings.Contains(got, " or ") {
		t.Fatalf("a union type should render both shapes, got %q", got)
	}
	// A $ref inside an array's items has to resolve too.
	if got := byName["filters"].Type; got != "array of object" {
		t.Fatalf("filters type = %q", got)
	}
}

// TestRequiredFieldsComeFirst: they are what a caller has to supply.
func TestRequiredFieldsComeFirst(t *testing.T) {
	op, _ := parse(t).Find("/query")
	seenOptional := false
	for _, f := range op.BodyFields {
		if !f.Required {
			seenOptional = true
			continue
		}
		if seenOptional {
			t.Fatalf("required field %q listed after an optional one", f.Name)
		}
	}
}

// TestPathLevelParametersApplyToEveryMethod: they are declared once for the
// whole path and would otherwise be dropped.
func TestPathLevelParametersApplyToEveryMethod(t *testing.T) {
	op, ok := parse(t).Find("POST /funnels/{id}/report")
	if !ok {
		t.Fatal("not found")
	}
	if len(op.Params) != 1 || op.Params[0].Name != "id" || !op.Params[0].Required {
		t.Fatalf("path parameter lost: %+v", op.Params)
	}
}

func TestFindAcceptsHowPeopleType(t *testing.T) {
	s := parse(t)
	for _, ref := range []string{"/query", "query", "POST /query", "post /query", " /query "} {
		if _, ok := s.Find(ref); !ok {
			t.Fatalf("%q should resolve", ref)
		}
	}
	if _, ok := s.Find("GET /query"); ok {
		t.Fatal("a method that does not exist on the path must not match")
	}
	if _, ok := s.Find("/nope"); ok {
		t.Fatal("an unknown path must not match")
	}
}

func TestSearch(t *testing.T) {
	s := parse(t)
	if got := len(s.Search("")); got != 3 {
		t.Fatalf("an empty term should list everything, got %d", got)
	}
	if got := s.Search("funnel"); len(got) != 1 || got[0].Path != "/funnels/{id}/report" {
		t.Fatalf("path match failed: %+v", got)
	}
	if got := s.Search("headline numbers"); len(got) != 1 || got[0].Path != "/sites" {
		t.Fatalf("a parameter description should be searchable: %+v", got)
	}
	if got := s.Search("all-sites key"); len(got) != 1 || got[0].Path != "/query" {
		t.Fatalf("a body field description should be searchable: %+v", got)
	}
	if got := s.Search("nothing here"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

// TestSelfReferencingSchemaTerminates: a schema that refers to itself is legal
// and common, and must stop the walk rather than the process.
func TestSelfReferencingSchemaTerminates(t *testing.T) {
	cyclic := `
paths:
  /x:
    post:
      requestBody:
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/Node"
components:
  schemas:
    Node:
      type: object
      properties:
        child:
          $ref: "#/components/schemas/Node"
`
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := Parse([]byte(cyclic)); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-done:
	case <-timeout():
		t.Fatal("a self-referencing schema did not terminate")
	}
}

func TestUnparseableDocumentIsAnError(t *testing.T) {
	if _, err := Parse([]byte("\tnot: [valid")); err == nil {
		t.Fatal("a broken document must be reported, not silently empty")
	}
}

// TestUnknownShapesAreSkippedNotFatal: a document that grew a field this
// version has never seen is still useful.
func TestUnknownShapesAreSkippedNotFatal(t *testing.T) {
	s, err := Parse([]byte(`
paths:
  /ok:
    get:
      summary: fine
  /weird: "not an object"
  /partial:
    get: 12345
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Operations) != 1 || s.Operations[0].Path != "/ok" {
		t.Fatalf("operations = %+v", s.Operations)
	}
}

// TestAllOfIsComposed. A schema built out of allOf reported no fields at all,
// which is the same "opposite of true" a missed $ref produced: an endpoint
// that takes a body was described as taking nothing.
func TestAllOfIsComposed(t *testing.T) {
	s, err := Parse([]byte(`
paths:
  /x:
    post:
      requestBody:
        content:
          application/json:
            schema:
              allOf:
                - $ref: "#/components/schemas/Base"
                - type: object
                  required: [extra]
                  properties:
                    extra: { type: string }
components:
  schemas:
    Base:
      type: object
      required: [id]
      properties:
        id: { type: integer }
        note: { type: string }
`))
	if err != nil {
		t.Fatal(err)
	}
	op, ok := s.Find("POST /x")
	if !ok {
		t.Fatal("not found")
	}
	byName := map[string]Param{}
	for _, f := range op.BodyFields {
		byName[f.Name] = f
	}
	for _, want := range []string{"id", "note", "extra"} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("field %q lost from the composition: %+v", want, op.BodyFields)
		}
	}
	if !byName["id"].Required || !byName["extra"].Required {
		t.Fatalf("allOf makes both branches' required fields required: %+v", op.BodyFields)
	}
	if byName["note"].Required {
		t.Fatal("an optional field must not become required")
	}
}

// TestOneOfDoesNotMakeFieldsRequired: a field required by one branch of a
// choice is not required by the choice.
func TestOneOfDoesNotMakeFieldsRequired(t *testing.T) {
	s, err := Parse([]byte(`
paths:
  /x:
    post:
      requestBody:
        content:
          application/json:
            schema:
              oneOf:
                - type: object
                  required: [a]
                  properties: { a: { type: string } }
                - type: object
                  required: [b]
                  properties: { b: { type: string } }
`))
	if err != nil {
		t.Fatal(err)
	}
	op, _ := s.Find("POST /x")
	if len(op.BodyFields) != 2 {
		t.Fatalf("both branches' fields should be listed: %+v", op.BodyFields)
	}
	for _, f := range op.BodyFields {
		if f.Required {
			t.Fatalf("%q must not be required by a choice: %+v", f.Name, op.BodyFields)
		}
	}
}

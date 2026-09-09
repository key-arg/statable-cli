// Package openapi reads just enough of the API's own OpenAPI document to make
// the CLI self-teaching.
//
// It is a deliberately small reader, not a general OpenAPI implementation: it
// answers "which endpoints mention this word" and "what does this one take",
// which is what a person exploring the API needs and what an agent needs in
// order to call something it was not told about. Anything it cannot make sense
// of is skipped rather than treated as an error, because a document that grew
// a field this version has never seen is still useful.
package openapi

import (
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Param is one query, path or header parameter.
type Param struct {
	Name        string `json:"name"`
	In          string `json:"in"`
	Required    bool   `json:"required"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
}

// Operation is one method on one path.
type Operation struct {
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	Summary     string   `json:"summary,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Params      []Param  `json:"params,omitempty"`
	// BodyFields lists the top-level properties of a JSON request body.
	BodyFields []Param `json:"body_fields,omitempty"`
}

// Ref renders the operation the way a person would type it.
func (o Operation) Ref() string { return o.Method + " " + o.Path }

// Spec is the parsed document.
type Spec struct {
	Title      string      `json:"title,omitempty"`
	Version    string      `json:"version,omitempty"`
	Operations []Operation `json:"operations"`
}

var methods = []string{"get", "post", "put", "patch", "delete", "head", "options"}

// maxSchemaDepth bounds every recursive walk over a schema. It is generous
// enough for any hand-written document and small enough that a cycle costs
// nothing.
const maxSchemaDepth = 16

// resolver follows local $ref pointers into the document.
//
// A real specification names its shapes once under components and points at
// them, so a reader that does not follow a $ref reports that POST /query
// "takes no parameters" — which is the opposite of true and worse than saying
// nothing.
type resolver struct {
	doc   map[string]any
	depth int
}

// deref replaces a {"$ref": "#/components/schemas/X"} node with what it points
// at. Only local pointers are followed: a remote one would mean fetching a
// second document to describe the first.
func (r *resolver) deref(node map[string]any) map[string]any {
	if r.depth > maxSchemaDepth {
		// A schema that refers to itself is legal and common; the depth cap
		// stops the walk rather than the process.
		return node
	}
	ref, ok := node["$ref"].(string)
	if !ok || !strings.HasPrefix(ref, "#/") {
		return node
	}
	cur := any(r.doc)
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return node
		}
		cur, ok = m[part]
		if !ok {
			return node
		}
	}
	target, ok := cur.(map[string]any)
	if !ok {
		return node
	}
	r.depth++
	defer func() { r.depth-- }()
	return r.deref(target)
}

// Parse reads a document. Unknown shapes are skipped, never fatal.
func Parse(b []byte) (*Spec, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	res := &resolver{doc: doc}

	s := &Spec{}
	if info, ok := doc["info"].(map[string]any); ok {
		s.Title, _ = info["title"].(string)
		s.Version, _ = info["version"].(string)
	}

	paths, _ := doc["paths"].(map[string]any)
	for path, raw := range paths {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		// A path key must be a URL path. Anything else is a key that landed
		// under `paths` by accident, and listing it produces an operation
		// that Find cannot look up — Find normalises a leading slash onto
		// what it is given, so "A" would be listed and then unreachable.
		if !strings.HasPrefix(path, "/") {
			continue
		}
		// Parameters declared once for the whole path apply to every method
		// on it, so they are collected before the methods are walked.
		shared := parseParams(res, item["parameters"])

		for _, m := range methods {
			opRaw, ok := item[m].(map[string]any)
			if !ok {
				continue
			}
			op := Operation{
				Method: strings.ToUpper(m),
				Path:   path,
				Params: append([]Param{}, shared...),
			}
			op.Summary, _ = opRaw["summary"].(string)
			op.Description, _ = opRaw["description"].(string)
			if tags, ok := opRaw["tags"].([]any); ok {
				for _, t := range tags {
					if str, ok := t.(string); ok {
						op.Tags = append(op.Tags, str)
					}
				}
			}
			op.Params = append(op.Params, parseParams(res, opRaw["parameters"])...)
			op.BodyFields = parseBody(res, opRaw["requestBody"])
			s.Operations = append(s.Operations, op)
		}
	}

	sort.Slice(s.Operations, func(i, j int) bool {
		if s.Operations[i].Path != s.Operations[j].Path {
			return s.Operations[i].Path < s.Operations[j].Path
		}
		return s.Operations[i].Method < s.Operations[j].Method
	})
	return s, nil
}

func parseParams(res *resolver, raw any) []Param {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]Param, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		m = res.deref(m)
		p := Param{}
		p.Name, _ = m["name"].(string)
		p.In, _ = m["in"].(string)
		p.Required, _ = m["required"].(bool)
		p.Description, _ = m["description"].(string)
		if sch, ok := m["schema"].(map[string]any); ok {
			p.Type = schemaType(res, sch)
		}
		if p.Name != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseBody(res *resolver, raw any) []Param {
	body, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	content, ok := body["content"].(map[string]any)
	if !ok {
		return nil
	}
	media, ok := content["application/json"].(map[string]any)
	if !ok {
		return nil
	}
	schema, ok := media["schema"].(map[string]any)
	if !ok {
		return nil
	}
	schema = res.deref(schema)

	// allOf composes a shape out of several. Reading only the top level of it
	// reported that the endpoint takes nothing, which is the same "opposite of
	// true" a missed $ref produced.
	props, required := collectObject(res, schema, 0)
	if len(props) == 0 {
		return nil
	}
	out := make([]Param, 0, len(props))
	for name, raw := range props {
		p := Param{Name: name, In: "body", Required: required[name]}
		if m, ok := raw.(map[string]any); ok {
			m = res.deref(m)
			p.Type = schemaType(res, m)
			p.Description, _ = m["description"].(string)
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		// Required fields first: they are what a caller has to supply.
		if out[i].Required != out[j].Required {
			return out[i].Required
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// collectObject gathers the properties and required names of a schema,
// following allOf composition. The depth cap matches the resolver's, so a
// composition that refers to itself stops the walk rather than the process.
func collectObject(res *resolver, schema map[string]any, depth int) (map[string]any, map[string]bool) {
	props := map[string]any{}
	required := map[string]bool{}
	if depth > maxSchemaDepth {
		return props, required
	}
	schema = res.deref(schema)

	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			if str, ok := r.(string); ok {
				required[str] = true
			}
		}
	}
	if p, ok := schema["properties"].(map[string]any); ok {
		for k, v := range p {
			props[k] = v
		}
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		list, ok := schema[key].([]any)
		if !ok {
			continue
		}
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			sub, subReq := collectObject(res, m, depth+1)
			for k, v := range sub {
				if _, exists := props[k]; !exists {
					props[k] = v
				}
			}
			// Only allOf makes a field genuinely required: a field required
			// by one branch of a choice is not required by the choice.
			if key == "allOf" {
				for k := range subReq {
					required[k] = true
				}
			}
		}
	}
	return props, required
}

// schemaType renders a type for a person, flattening the union forms a 3.1
// document may use.
//
// The depth parameter is not decoration. deref's own cap is a balanced
// counter — it is back to zero by the time deref returns — so it bounds a
// chain of pure $ref nodes and nothing else. A cycle reached through an
// array's items or a union recurses on this function's own stack instead,
// and a stack overflow is not a panic: recover cannot catch it and the whole
// process dies on a document the server chose. A recursive array schema is
// legal, idiomatic OpenAPI, so this is not only a hostile-input concern.
func schemaType(res *resolver, m map[string]any) string {
	return schemaTypeAt(res, m, 0)
}

func schemaTypeAt(res *resolver, m map[string]any, depth int) string {
	if depth > maxSchemaDepth {
		return ""
	}
	m = res.deref(m)
	if t, ok := m["type"].(string); ok {
		if t == "array" {
			if items, ok := m["items"].(map[string]any); ok {
				inner := schemaTypeAt(res, items, depth+1)
				if inner == "" {
					return "array"
				}
				return "array of " + inner
			}
			return "array"
		}
		return t
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		if list, ok := m[key].([]any); ok {
			parts := make([]string, 0, len(list))
			for _, item := range list {
				if im, ok := item.(map[string]any); ok {
					if t := schemaTypeAt(res, im, depth+1); t != "" {
						parts = append(parts, t)
					}
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, " or ")
			}
		}
	}
	if _, ok := m["properties"]; ok {
		return "object"
	}
	return ""
}

// Search returns the operations whose path, summary, description or tags
// mention the term. An empty term returns everything, which is what makes
// `api search` usable as a table of contents.
func (s *Spec) Search(term string) []Operation {
	term = strings.ToLower(strings.TrimSpace(term))
	if term == "" {
		return s.Operations
	}
	var out []Operation
	for _, op := range s.Operations {
		hay := strings.ToLower(strings.Join(append([]string{
			op.Path, op.Method, op.Summary, op.Description,
		}, op.Tags...), " "))
		if strings.Contains(hay, term) {
			out = append(out, op)
			continue
		}
		for _, p := range append(append([]Param{}, op.Params...), op.BodyFields...) {
			if strings.Contains(strings.ToLower(p.Name+" "+p.Description), term) {
				out = append(out, op)
				break
			}
		}
	}
	return out
}

// Find locates one operation. The path may be given with or without a method
// and with or without the leading slash, because that is how people type it.
func (s *Spec) Find(ref string) (Operation, bool) {
	ref = strings.TrimSpace(ref)
	method, path := "", ref
	if a, b, found := strings.Cut(ref, " "); found {
		method, path = strings.ToUpper(strings.TrimSpace(a)), strings.TrimSpace(b)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	for _, op := range s.Operations {
		if op.Path != path {
			continue
		}
		if method == "" || op.Method == method {
			return op, true
		}
	}
	return Operation{}, false
}

// MethodsFor lists the methods a path supports, for the case where the path
// is right and the method is not. Answering "no such endpoint" there is the
// least useful thing that could be said.
func (s *Spec) MethodsFor(ref string) []string {
	ref = strings.TrimSpace(ref)
	if _, b, found := strings.Cut(ref, " "); found {
		ref = strings.TrimSpace(b)
	}
	if !strings.HasPrefix(ref, "/") {
		ref = "/" + ref
	}
	var out []string
	for _, op := range s.Operations {
		if op.Path == ref {
			out = append(out, op.Method)
		}
	}
	return out
}

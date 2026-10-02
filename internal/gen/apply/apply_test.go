package apply

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fada4773-sketch/apigen-apitest/internal/gen/defaults"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/dict"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/yamldoc"
	"github.com/fada4773-sketch/apigen-apitest/internal/spec"
)

type run struct {
	res  *Result
	doc  *yamldoc.Doc
	dict *dict.Dict
	out  *spec.Spec // the written file, loaded again
	text string
}

// applyTo runs Apply on a copy of testdata/gen/apply.yaml with the given
// defaults and loads the result again with apitest's loader.
func applyTo(t *testing.T, defaultsJSON string, opt Options) run {
	t.Helper()
	src, err := os.ReadFile("../../../testdata/gen/apply.yaml")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "apply.yaml")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	d, _, _ := dict.Build(s, nil, dict.Options{Seed: 1})
	defs, err := defaults.Parse([]byte(defaultsJSON))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := yamldoc.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	res := Apply(doc, s, d, defs, opt)
	r := run{res: res, doc: doc, dict: d}
	if len(res.Fatal) > 0 {
		return r
	}
	b, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	r.text = string(b)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if r.out, err = spec.Load(context.Background(), path); err != nil {
		t.Fatalf("written spec does not load: %v\n%s", err, b)
	}
	return r
}

func example(t *testing.T, doc *yamldoc.Doc, keys ...string) any {
	t.Helper()
	n := yamldoc.Path(doc.Root, keys...)
	if n == nil {
		return nil
	}
	v, err := yamldoc.Decode(n)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func notes(r run, code string) []string {
	var out []string
	for _, n := range r.res.Notes {
		if n.Code == code {
			out = append(out, n.Where+": "+n.Message)
		}
	}
	return out
}

const baseDefaults = `{
  "AppCode": "a1",
  "appId": 7,
  "getApp.id": {"bind": "createApp", "pointer": "/Id"},
  "deleteUser.key": {"bind": "createApp", "pointer": "/AppCode"},
  "updateApp.x-apitest-verify": false,
  "updateApp.appId": 3,
  "noSuchField": 1
}`

func TestApplyWritesExamples(t *testing.T) {
	r := applyTo(t, baseDefaults, Options{Seed: 1})
	if len(r.res.Fatal) > 0 {
		t.Fatalf("fatal: %v", r.res.Fatal)
	}

	// request body with named examples: no "example" next to "examples"
	if v := example(t, r.doc, "paths", "/apps", "post", "requestBody", "content", "application/json", "example"); v != nil {
		t.Errorf("example written next to examples: %v", v)
	}
	// shared request body: generated, with the default, without readOnly Id
	body, _ := example(t, r.doc, "components", "requestBodies", "AppBody", "content", "application/json", "example").(map[string]any)
	if body["AppCode"] != "a1" || body["Id"] != nil || body["Name"] == nil || body["Secret"] == nil {
		t.Errorf("request example: %v", body)
	}
	// response: no writeOnly Secret, readOnly Id present
	resp, _ := example(t, r.doc, "paths", "/apps", "post", "responses", "201", "content", "application/json", "example").(map[string]any)
	if resp["Secret"] != nil || resp["Id"] == nil {
		t.Errorf("response example: %v", resp)
	}

	// named examples get the default, except with x-example-defaults: false
	if v := example(t, r.doc, "paths", "/apps", "post", "requestBody", "content", "application/json", "examples", "curated", "value", "AppCode"); v != "a1" {
		t.Errorf("curated example AppCode = %v", v)
	}
	if v := example(t, r.doc, "paths", "/apps", "post", "requestBody", "content", "application/json", "examples", "wrongApp", "value", "AppCode"); v != "missing" {
		t.Errorf("negative example was changed: %v", v)
	}

	// a valid existing example is kept, but gets the default
	kept, _ := example(t, r.doc, "paths", "/apps/{id}", "get", "responses", "200", "content", "application/json", "example").(map[string]any)
	if kept["Name"] != "Kept app" || kept["AppCode"] != "a1" {
		t.Errorf("kept example: %v", kept)
	}
	// an invalid existing example is replaced
	if len(notes(r, CodeReplaced)) != 1 {
		t.Errorf("replaced: %v", notes(r, CodeReplaced))
	}

	// the shared request body was written at its target, the $ref stays
	if yamldoc.Get(yamldoc.Path(r.doc.Root, "paths", "/apps/{id}", "put"), "requestBody").Content[1].Value != "#/components/requestBodies/AppBody" {
		t.Error("the $ref of the request body was changed")
	}

	// generic {id}: /apps/{id} takes appId
	if v, _ := example(t, r.doc, "paths", "/apps/{id}", "get", "parameters", "0", "example").(json.Number); v != "7" {
		t.Errorf("getApp id = %v, want 7 from appId", v)
	}
	// generic {key} without default: generated and kept per path
	if _, ok := r.dict.Paths["/users/{key}"]; !ok {
		t.Errorf("no value for /users/{key} in the dictionary: %v", r.dict.Paths)
	}

	// a shared parameter cannot take an operation-specific default
	if len(notes(r, CodeSharedParam)) == 0 {
		t.Error("SHARED_PARAM_CONFLICT missing")
	}

	// extension and bindings
	if v := example(t, r.doc, "paths", "/apps/{id}", "put", "x-apitest-verify"); v != false {
		t.Errorf("x-apitest-verify = %v", v)
	}
	bind, _ := example(t, r.doc, "paths", "/apps/{id}", "get", "parameters", "0", "x-apitest-bind").(map[string]any)
	if bind["from"] != "createApp" || bind["pointer"] != "/Id" {
		t.Errorf("x-apitest-bind: %v", bind)
	}
	link, _ := example(t, r.doc, "paths", "/apps", "post", "responses", "201", "links", "deleteUser_key").(map[string]any)
	if link["operationId"] != "deleteUser" || !reflect.DeepEqual(link["parameters"], map[string]any{"key": "$response.body#/AppCode"}) {
		t.Errorf("link: %v", link)
	}

	// unused defaults are reported
	if u := notes(r, CodeDefaultUnused); len(u) != 1 || !strings.Contains(u[0], "noSuchField") {
		t.Errorf("unused: %v", u)
	}
	// comments survive
	if !strings.Contains(r.text, "# comment that must survive") {
		t.Error("comment lost")
	}
	// the written spec loads and its examples fit their schemas
	for _, f := range r.out.Findings {
		if f.Kind == spec.FindingExampleSchema {
			t.Errorf("finding in the written spec: %s %s", f.Where, f.Message)
		}
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	first := applyTo(t, baseDefaults, Options{Seed: 1})
	s := first.out
	doc, err := yamldoc.Parse([]byte(first.text))
	if err != nil {
		t.Fatal(err)
	}
	defs, _ := defaults.Parse([]byte(baseDefaults))
	res := Apply(doc, s, first.dict, defs, Options{Seed: 1})
	if res.Changed || res.Stats.Added+res.Stats.Replaced+res.Stats.DefaultsApplied+res.Stats.Extensions+res.Stats.Bindings != 0 {
		t.Errorf("second run changed something: %+v %v", res.Stats, res.Notes)
	}
}

func TestApplyFatal(t *testing.T) {
	for name, tc := range map[string]struct{ defaults, want string }{
		"default violates schema": {`{"AppCode": "this is far too long for ten"}`, "DEFAULT_INVALID"},
		"extension type":          {`{"updateApp.x-apitest-verify": "false"}`, "EXT_INVALID"},
		"extension on operation":  {`{"updateApp.x-apitest-bind": true}`, "EXT_INVALID"},
		"unknown producer":        {`{"getApp.id": {"bind": "nope", "pointer": "/Id"}}`, "BIND_INVALID"},
	} {
		t.Run(name, func(t *testing.T) {
			r := applyTo(t, tc.defaults, Options{Seed: 1})
			if len(r.res.Fatal) == 0 || !strings.Contains(strings.Join(r.res.Fatal, "\n"), tc.want) {
				t.Errorf("fatal: %v", r.res.Fatal)
			}
		})
	}
}

func TestApplyOverwrite(t *testing.T) {
	r := applyTo(t, `{}`, Options{Seed: 1, Overwrite: true})
	kept, _ := example(t, r.doc, "paths", "/apps/{id}", "get", "responses", "200", "content", "application/json", "example").(map[string]any)
	if kept["Name"] == "Kept app" {
		t.Errorf("Overwrite kept the example: %v", kept)
	}
}

func TestResourceKeys(t *testing.T) {
	for path, want := range map[string][]string{
		"/apps/{id}":           {"appId", "appsId"},
		"/categories/{id}":     {"categoryId", "categoriesId"},
		"/statuses/{id}":       {"statuseId", "statusId", "statusesId"},
		"/app-versions/{id}":   {"appVersionId", "appVersionsId"},
		"/apps/{appId}/x/{id}": {"xId"},
		"/{id}":                nil,
		"/a/{other}/{id}":      nil,
	} {
		if got := resourceKeys(path, "id"); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", path, got, want)
		}
	}
}

func TestCoerce(t *testing.T) {
	str, _ := spec.Load(context.Background(), "../../../testdata/gen/apply.yaml")
	app := str.Doc.Components.Schemas["App"].Value
	id, code := app.Properties["Id"].Value, app.Properties["AppCode"].Value
	if v := coerce("42", id); v.(interface{ String() string }).String() != "42" {
		t.Errorf("string to number: %#v", v)
	}
	if v := coerce([]any{"x", "y"}, code); v != "x" {
		t.Errorf("array to scalar: %#v", v)
	}
	if v := coerce(true, code); v != "true" {
		t.Errorf("bool to string: %#v", v)
	}
}

package defaults

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAndLookup(t *testing.T) {
	d, err := Parse([]byte(`{
		"$comment": "ignored",
		"AppCode": "a",
		"Garden.Name": "Mitte",
		"listApps.Name": "Op",
		"/apps/{id}": 7,
		"UpdateApp.x-apitest-verify": false,
		"UpdateApp.x-apitest-order": 2,
		"GetApp.id": {"bind": "CreateApp", "pointer": "/Id"},
		"planetCode": {"from": "GET /Planet", "pick": "/0/Code"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if d.Len() != 8 {
		t.Errorf("Len = %d", d.Len())
	}
	// case-insensitive, DTO before operation before plain name
	if e := d.Field("garden", "listApps", "name"); e == nil || e.Value != "Mitte" {
		t.Errorf("DTO key: %+v", e)
	}
	if e := d.Field("Other", "LISTAPPS", "Name"); e == nil || e.Value != "Op" {
		t.Errorf("operation key: %+v", e)
	}
	if e := d.Field("", "", "appcode"); e == nil || e.Value != "a" {
		t.Errorf("plain key: %+v", e)
	}
	if d.Field("", "", "ParentAppCode") != nil {
		t.Error("no substring matches")
	}
	if e := d.Plain("/apps/{id}"); e == nil {
		t.Error("path key")
	}
	ext := d.Extensions("updateapp")
	if len(ext) != 2 || ext[0].Name != "x-apitest-verify" || ext[1].Name != "x-apitest-order" {
		t.Errorf("extensions: %+v", ext)
	}
	if b := d.Binding("getapp", "ID"); b == nil || b.Bind.From != "CreateApp" || b.Bind.Pointer != "/Id" {
		t.Errorf("binding: %+v", b)
	}
	if d.Field("", "GetApp", "id") != nil {
		t.Error("a binding is not a value")
	}
	if src := d.Sources(); len(src) != 1 || src[0].From.Pick != "/0/Code" {
		t.Errorf("sources: %+v", src)
	}

	d.Use(d.Plain("AppCode"))
	unused := d.Unused()
	if len(unused) != 6 || unused[0] != "Garden.Name" {
		t.Errorf("unused: %v", unused)
	}
	if u := d.Usage(); len(u) != 7 || u[0].Key != "AppCode" || u[0].Uses != 1 {
		t.Errorf("usage: %+v", u)
	}
}

func TestParseErrors(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"duplicate":        {`{"AppCode": 1, "appcode": 2}`, "same key"},
		"not an object":    {`[1]`, "JSON object"},
		"bind without key": {`{"id": {"bind": "X", "pointer": "/Id"}}`, "<operationId>.<parameter>"},
		"bind both":        {`{"a.id": {"bind": "X", "pointer": "/Id", "header": "Location"}}`, "either"},
		"bind pointer":     {`{"a.id": {"bind": "X", "pointer": "Id"}}`, "must start with /"},
		"source method":    {`{"code": {"from": "POST /x", "pick": "/0"}}`, "GET /path"},
		"broken":           {`{"a": }`, "invalid"},
	} {
		if _, err := Parse([]byte(tc.in)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

func TestLoad(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "none.json")
	d, err := Load(missing)
	if err != nil || d.Len() != 0 {
		t.Errorf("missing file: %v %v", d, err)
	}
	broken := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(broken, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(broken); err == nil || !strings.Contains(err.Error(), broken) {
		t.Errorf("broken file: %v", err)
	}
}

func TestNullAndRejected(t *testing.T) {
	d, err := Parse([]byte(`{"zone": null, "code": "a", "$rejected": ["getApp.id"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !d.IsTodo("Zone") || d.Field("", "", "zone") != nil {
		t.Error("null must be a todo without value")
	}
	if u := d.Unused(); len(u) != 1 || u[0] != "code" {
		t.Errorf("unused: %v", u)
	}
	if len(d.Rejected) != 1 || d.Rejected[0] != "getApp.id" {
		t.Errorf("rejected: %v", d.Rejected)
	}
}

// Update keeps the order and values of a file, adds new keys once and
// replaces the "$review" block.
func TestUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "defaults.json")
	if err := os.WriteFile(path, []byte(`{"$review": "old", "b": 1, "a": {"x": [1, 2]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := Update(path, []Pair{{"B", 9}, {"c", nil}, {"d", "<x>"}}, map[string]string{"about": "new"})
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	got, _ := os.ReadFile(path)
	want := `{
  "$review": {
    "about": "new"
  },
  "b": 1,
  "a": {
    "x": [
      1,
      2
    ]
  },
  "c": null,
  "d": "<x>"
}
`
	if string(got) != want {
		t.Errorf("got\n%s", got)
	}
	if changed, _ := Update(path, []Pair{{"c", 1}}, map[string]string{"about": "new"}); changed {
		t.Error("nothing new, but the file changed")
	}
	if _, err := Update(path, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); strings.Contains(string(got), ReviewKey) {
		t.Errorf("review block not removed:\n%s", got)
	}
	missing := filepath.Join(t.TempDir(), "new.json")
	if _, err := Update(missing, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(missing); string(got) != "{}\n" {
		t.Errorf("new file: %q", got)
	}
}

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

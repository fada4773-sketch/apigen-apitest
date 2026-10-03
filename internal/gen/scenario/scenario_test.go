package scenario

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fada4773-sketch/apigen-apitest/internal/gen/apply"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/defaults"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/dict"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/discover"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/model"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/yamldoc"
	"github.com/fada4773-sketch/apigen-apitest/internal/spec"
)

// pipeline runs apply and Run on a spec file the way apitest-gen does and
// writes the result back.
type pipeline struct {
	t    *testing.T
	path string
	dict *dict.Dict
	defs string
	// fetch is used instead of generated records
	fetch Fetcher
}

type outcome struct {
	res      *Result
	text     string
	written  *spec.Spec
	problems []string // of Verify
}

func newPipeline(t *testing.T, defaultsJSON string) *pipeline {
	t.Helper()
	return newPipelineFile(t, "records.yaml", defaultsJSON)
}

func newPipelineFile(t *testing.T, file, defaultsJSON string) *pipeline {
	t.Helper()
	src, err := os.ReadFile("../../../testdata/gen/" + file)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), file)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	return &pipeline{t: t, path: path, defs: defaultsJSON}
}

func (p *pipeline) load(path string) *spec.Spec {
	p.t.Helper()
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		p.t.Fatal(err)
	}
	return s
}

func (p *pipeline) save(doc *yamldoc.Doc, path string) {
	p.t.Helper()
	b, err := doc.Bytes()
	if err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		p.t.Fatal(err)
	}
}

func (p *pipeline) run() outcome {
	t := p.t
	t.Helper()
	s := p.load(p.path)
	d, _, _ := dict.Build(s, p.dict, dict.Options{Seed: 1})
	defs, err := defaults.Parse([]byte(p.defs))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := yamldoc.Load(p.path)
	if err != nil {
		t.Fatal(err)
	}
	if ar := apply.Apply(doc, s, d, defs, apply.Options{Seed: 1}); len(ar.Fatal) > 0 {
		t.Fatalf("apply: %v", ar.Fatal)
	}
	mid := filepath.Join(filepath.Dir(p.path), "mid.yaml")
	p.save(doc, mid)
	ms := p.load(mid)
	res := Run(context.Background(), Input{Doc: doc, Spec: ms, Dict: d, Defaults: defs, Model: model.Detect(ms, defs.Model), Seed: 1, Fetch: p.fetch})
	o := outcome{res: res}
	if len(res.Problems) > 0 {
		return o
	}
	p.save(doc, p.path)
	b, _ := os.ReadFile(p.path)
	o.text = string(b)
	o.written = p.load(p.path)
	o.problems = Verify(o.written, defs, res.Records)
	p.dict = d
	return o
}

// example returns the example of a response or the request body (code "").
func example(t *testing.T, s *spec.Spec, opID, code string) any {
	t.Helper()
	op := s.Op(opID)
	if op == nil {
		t.Fatalf("no operation %s", opID)
	}
	if code == "" {
		return op.Op.RequestBody.Value.Content["application/json"].Example
	}
	return op.Op.Responses.Value(code).Value.Content["application/json"].Example
}

func param(t *testing.T, s *spec.Spec, opID, name string) any {
	t.Helper()
	for _, p := range s.Op(opID).Params {
		if p.Name == name {
			return spec.Normalize(p.Example)
		}
	}
	t.Fatalf("%s has no parameter %s", opID, name)
	return nil
}

func field(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			l, _ := v.([]any)
			if k >= len(l) {
				return nil
			}
			v = l[k]
		}
	}
	return spec.Normalize(v)
}

func notes(res *Result) string {
	var out []string
	for _, n := range append(append([]Note(nil), res.Notes...), res.Problems...) {
		out = append(out, n.String())
	}
	return strings.Join(out, "\n")
}

const order = `{"$apitest": {"MethodOrder": ["POST", "PUT", "GET", "DELETE"], "DeleteLast": true}}`

// With PUT before GET every GET expects what the last update sent; keys
// and the shared parameters stay consistent, and a second run changes
// nothing.
func TestRunGeneratedFollowsUpdates(t *testing.T) {
	p := newPipeline(t, order)
	o := p.run()
	if len(o.res.Problems) > 0 || len(o.problems) > 0 {
		t.Fatalf("problems: %v %v\n%s", o.res.Problems, o.problems, notes(o.res))
	}
	s := o.written
	byID, byCode := example(t, s, "UpdateDockById", ""), example(t, s, "UpdateDock", "")
	if field(byID, "Name") == field(byCode, "Name") {
		t.Errorf("both updates send the same name %v", field(byID, "Name"))
	}
	last := field(byCode, "Name") // UpdateDock runs after UpdateDockById
	for _, id := range []string{"GetDockById", "GetDock"} {
		if got := field(example(t, s, id, "200"), "Name"); got != last {
			t.Errorf("%s expects Name %v, the last update sent %v", id, got, last)
		}
	}
	if got := field(example(t, s, "GetDocks", "200"), 0, "Name"); got != last {
		t.Errorf("GetDocks expects Name %v, want %v", got, last)
	}
	dockID := field(example(t, s, "GetDockById", "200"), "Id")
	code := field(example(t, s, "GetDock", "200"), "Code")
	if param(t, s, "GetDockById", "id") != dockID || param(t, s, "GetDock", "Code") != code || param(t, s, "GetShips", "Code") != code {
		t.Errorf("parameters: id %v Code %v, record Id %v Code %v", param(t, s, "GetDockById", "id"), param(t, s, "GetDock", "Code"), dockID, code)
	}
	if !strings.Contains(fmt.Sprint(code), "") || strings.ToLower(fmt.Sprint(code)) != fmt.Sprint(code) {
		t.Errorf("Code %v does not fit the pattern of {Code}", code)
	}
	if field(byID, "Code") != code || field(byCode, "Code") != code {
		t.Error("an update changed the key Code")
	}
	if got := field(example(t, s, "UpdateDock", "200"), "Id"); got != dockID {
		t.Errorf("Response.Id %v, want the Dock Id %v", got, dockID)
	}
	if got := field(example(t, s, "UpdateDock", "200"), "Message"); got != "Successfully updated Dock" {
		t.Errorf("message %v", got)
	}
	if got := field(example(t, s, "GetDock", "404"), "Message"); got != "Error while processing the request" {
		t.Errorf("shared error message %v", got)
	}
	// the Ship paths share {id} with the Dock paths: one of them is copied
	shipID := field(example(t, s, "GetShipById", "200"), "Id")
	if param(t, s, "GetShipById", "id") != shipID || param(t, s, "GetDockById", "id") != dockID {
		t.Errorf("{id}: ship %v (record %v), dock %v (record %v)", param(t, s, "GetShipById", "id"), shipID, param(t, s, "GetDockById", "id"), dockID)
	}
	if o.res.Stats.Inlined == 0 || !strings.Contains(notes(o.res), CodeInlined) {
		t.Errorf("no parameter was copied:\n%s", notes(o.res))
	}
	if got := field(example(t, s, "GetShips", "200"), 0, "DockId"); got != dockID {
		t.Errorf("the ship refers to Dock %v, want %v", got, dockID)
	}
	if len(p.dict.Records["Dock"]) != 1 || p.dict.Records["Dock"][0]["Id"] != dockID {
		t.Errorf("dictionary records: %v", p.dict.Records)
	}

	again := p.run()
	if again.text != o.text {
		t.Error("the second run changed the spec")
	}
}

// In apitest's default order the reads run before the updates and show
// the start record.
func TestRunDefaultOrder(t *testing.T) {
	o := newPipeline(t, `{}`).run()
	if len(o.problems) > 0 {
		t.Fatal(o.problems)
	}
	start := field(o.written.Op("GetDocks").Op.Responses.Value("200").Value.Content["application/json"].Example, 0, "Name")
	if got := field(example(t, o.written, "GetDock", "200"), "Name"); got != start {
		t.Errorf("GetDock expects %v, the start record has %v", got, start)
	}
}

// Verify finds an example that does not show the record of its position.
func TestVerifyFindsStaleExample(t *testing.T) {
	p := newPipeline(t, order)
	o := p.run()
	name := fmt.Sprint(field(example(t, o.written, "GetDock", "200"), "Name"))
	stale := strings.Replace(o.text, "Name: "+name+"\n", "Name: Old Dock\n", 1)
	if stale == o.text {
		t.Fatal("nothing replaced")
	}
	if err := os.WriteFile(p.path, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	defs, _ := defaults.Parse([]byte(order))
	problems := Verify(p.load(p.path), defs, o.res.Records)
	if len(problems) == 0 || !strings.Contains(problems[0], CodeStale) || !strings.Contains(problems[0], `"Old Dock"`) || !strings.Contains(problems[0], "changed by Dock/UpdateDock/default") {
		t.Fatalf("problems: %v", problems)
	}
}

// fakeInstance serves docks and ships like a seeded test database.
func fakeInstance(t *testing.T, docks []map[string]any, detailName string) Fetcher {
	t.Helper()
	ships := []map[string]any{{"Id": 31, "Name": "Pilot Ship", "DockId": 7}}
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	find := func(field string, v any) map[string]any {
		for _, d := range docks {
			if fmt.Sprint(d[field]) == fmt.Sprint(v) {
				out := map[string]any{}
				for k, x := range d {
					out[k] = x
				}
				if detailName != "" {
					out["Name"] = detailName
				}
				return out
			}
		}
		return nil
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		var v any
		switch {
		case len(segs) == 1 && segs[0] == "Dock":
			v = docks
		case len(segs) == 3 && segs[0] == "Dock" && segs[1] == "id":
			v = find("Id", segs[2])
		case len(segs) == 2 && segs[0] == "Dock":
			v = find("Code", segs[1])
		case len(segs) == 3 && segs[0] == "Dock" && segs[2] == "Ship":
			v = ships
		case len(segs) == 3 && segs[0] == "Ship":
			v = ships[0]
		}
		if m, ok := v.(map[string]any); v == nil || (ok && m == nil) {
			http.NotFound(w, r)
			return
		}
		write(w, v)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	opt := discover.Options{BaseURL: srv.URL, Client: srv.Client()}
	return func(ctx context.Context, path string) (any, error) { return discover.Get(ctx, opt, path) }
}

var seeded = []map[string]any{
	{"Id": 7, "Code": "abc", "Name": "Moon Dock"},
	{"Id": 8, "Code": "def", "Name": "Planet Dock"},
	{"Id": 9, "Code": "ghi", "Name": "Garden Dock"},
}

// The snapshot takes "count" elements of the list; the examples show the
// fetched records, the list example the fetched elements.
func TestRunSnapshot(t *testing.T) {
	p := newPipeline(t, `{"$snapshot": {"Dock": {"from": "GetDocks", "count": 2}}, "DockRead.Name": "Ignored"}`)
	p.fetch = fakeInstance(t, seeded, "")
	o := p.run()
	if len(o.res.Problems) > 0 || len(o.problems) > 0 {
		t.Fatalf("problems: %v %v", o.res.Problems, o.problems)
	}
	s := o.written
	list := example(t, s, "GetDocks", "200").([]any)
	if len(list) != 2 || field(list, 1, "Name") != "Planet Dock" || field(list, 0, "Code") != "abc" {
		t.Errorf("list: %v", list)
	}
	if param(t, s, "GetDock", "Code") != "abc" || field(example(t, s, "GetDock", "200"), "Name") != "Moon Dock" {
		t.Errorf("GetDock: %v %v", param(t, s, "GetDock", "Code"), example(t, s, "GetDock", "200"))
	}
	if got := field(example(t, s, "GetShipById", "200"), "Name"); got != "Pilot Ship" {
		t.Errorf("ship: %v", got)
	}
	all := notes(o.res)
	for _, want := range []string{"SNAPSHOT Dock: 2 of 3 elements from GET /Dock (GetDocks)", CodeSnapshotWins, `"DockRead.Name" = "Ignored"`} {
		if !strings.Contains(all, want) {
			t.Errorf("notes lack %q:\n%s", want, all)
		}
	}
	if o.res.Stats.Fetched < 4 {
		t.Errorf("fetched %d", o.res.Stats.Fetched)
	}
}

// A key default selects the fetched record with that key.
func TestRunSnapshotKeyDefault(t *testing.T) {
	p := newPipeline(t, `{"GetDock.Code": "def"}`)
	p.fetch = fakeInstance(t, seeded, "")
	o := p.run()
	if len(o.res.Problems) > 0 {
		t.Fatal(o.res.Problems)
	}
	if got := field(example(t, o.written, "GetDockById", "200"), "Id"); fmt.Sprint(got) != "8" {
		t.Errorf("selected Dock %v, want 8", got)
	}

	p = newPipeline(t, `{"GetDock.Code": "zzz"}`)
	p.fetch = fakeInstance(t, seeded, "")
	if o := p.run(); len(o.res.Problems) == 0 || o.res.Problems[0].Code != CodeSnapshotKey {
		t.Errorf("problems: %v", o.res.Problems)
	}
}

func TestRunSnapshotProblems(t *testing.T) {
	for name, c := range map[string]struct {
		defs   string
		docks  []map[string]any
		detail string
		code   string
	}{
		"short":    {`{"$snapshot": {"Dock": {"from": "GetDocks", "count": 5}}}`, seeded, "", CodeSnapshotShort},
		"mismatch": {`{}`, seeded, "Other Name", CodeSnapshotDiff},
		"source":   {`{"$snapshot": {"Dock": {"from": "GetShips"}}}`, seeded, "", CodeSnapshotFail},
		"schema":   {`{}`, []map[string]any{{"Id": "seven", "Code": "abc", "Name": "x"}}, "", CodeSnapshotFail},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPipeline(t, c.defs)
			p.fetch = fakeInstance(t, c.docks, c.detail)
			o := p.run()
			if len(o.res.Problems) == 0 || o.res.Problems[0].Code != c.code {
				t.Fatalf("problems: %v", o.res.Problems)
			}
		})
	}
}

// An empty list means the test starts without such records.
func TestRunSnapshotEmpty(t *testing.T) {
	p := newPipeline(t, `{}`)
	p.fetch = fakeInstance(t, []map[string]any{}, "")
	o := p.run()
	if !strings.Contains(notes(o.res), CodeSnapshotEmpty) {
		t.Errorf("notes:\n%s", notes(o.res))
	}
	if len(o.res.Records.Records("Dock")) != 0 {
		t.Errorf("records: %v", o.res.Records.Records("Dock"))
	}
}

func TestRunFetchFails(t *testing.T) {
	p := newPipeline(t, `{}`)
	p.fetch = func(context.Context, string) (any, error) { return nil, errors.New("connection refused") }
	o := p.run()
	if len(o.res.Problems) == 0 || !strings.Contains(o.res.Problems[0].Message, "connection refused") {
		t.Fatalf("problems: %v", o.res.Problems)
	}
}

func TestOrderRejectsUnknownTag(t *testing.T) {
	s, err := spec.Load(context.Background(), "../../../testdata/gen/records.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Order(s, defaults.Run{Tags: []string{"Nope"}}); err == nil || !strings.Contains(err.Error(), "$apitest") {
		t.Errorf("err = %v", err)
	}
}

// Key defaults set the keys of the first record: "/Planet/id/{id}" its Id,
// "Code" its Code; every example of the planet and its moons follows.
func TestRunKeyDefaults(t *testing.T) {
	o := newPipelineFile(t, "lists.yaml", `{"/Planet/id/{id}": 100, "Code": "terra"}`).run()
	if len(o.res.Problems) > 0 || len(o.problems) > 0 {
		t.Fatalf("problems: %v %v", o.res.Problems, o.problems)
	}
	s := o.written
	if got := example(t, s, "getPlanetById", "200"); field(got, "Id") != json.Number("100") || field(got, "Code") != "terra" {
		t.Errorf("planet by id: %v", got)
	}
	if got := param(t, s, "getPlanetById", "id"); got != json.Number("100") {
		t.Errorf("{id}: %v", got)
	}
	if got := field(example(t, s, "listMoonsOfPlanet", "200"), 0, "PlanetId"); got != json.Number("100") {
		t.Errorf("moon refers to planet %v", got)
	}

	conflict := newPipelineFile(t, "lists.yaml", `{"/Planet/id/{id}": 100, "getPlanetById.id": 5}`).run()
	if len(conflict.res.Problems) == 0 || conflict.res.Problems[0].Code != CodeDefault {
		t.Errorf("problems: %v", conflict.res.Problems)
	}
}

// "from" in "$snapshot" can be the request itself. Placeholders left in it
// are filled where a value is known; an unknown optional query parameter
// is dropped, an unknown path parameter stops the run.
func TestRunSnapshotURL(t *testing.T) {
	var asked []string
	fetch := func(_ context.Context, path string) (any, error) {
		asked = append(asked, path)
		dock := map[string]any{"Code": "abc", "Name": "Moon Dock"}
		switch strings.Split(path, "?")[0] {
		case "/DefaultDock/Level/A1":
			return []any{dock}, nil
		case "/Dock/abc":
			return dock, nil
		}
		return nil, errors.New("status 404")
	}
	p := newPipelineFile(t, "levels.yaml", `{"$snapshot": {"Dock": {"from": "/DefaultDock/Level/A1?dockCode={dockCode}&pilotNumber=7", "$comment": "set by hand"}}}`)
	p.fetch = fetch
	o := p.run()
	if len(o.res.Problems) > 0 || len(o.problems) > 0 {
		t.Fatalf("problems: %v %v", o.res.Problems, o.problems)
	}
	if len(asked) == 0 || asked[0] != "/DefaultDock/Level/A1?pilotNumber=7" {
		t.Errorf("requests: %v", asked)
	}
	if got := field(example(t, o.written, "GetDock", "200"), "Name"); got != "Moon Dock" {
		t.Errorf("GetDock: %v", got)
	}

	for from, want := range map[string]string{
		"/DefaultDock/Level/{level}": `{level} in "/DefaultDock/Level/{level}" has no value`,
		"GET /Nope/A1":               "is no GET of Dock",
	} {
		p := newPipelineFile(t, "levels.yaml", `{"$snapshot": {"Dock": {"from": "`+from+`"}}}`)
		p.fetch = fetch
		o := p.run()
		if len(o.res.Problems) == 0 || !strings.Contains(o.res.Problems[0].Message, want) {
			t.Errorf("%s: problems %v", from, o.res.Problems)
		}
	}
}

// Without "$snapshot" a list below an unknown path parameter is no source:
// its value only the user knows.
func TestRunSnapshotUnknownParam(t *testing.T) {
	p := newPipelineFile(t, "levels.yaml", `{}`)
	p.fetch = func(context.Context, string) (any, error) { return nil, errors.New("not asked") }
	o := p.run()
	if len(o.res.Problems) > 0 || !strings.Contains(notes(o.res), `"from": "/path?query"`) {
		t.Errorf("problems %v\n%s", o.res.Problems, notes(o.res))
	}
}

func TestMatchPath(t *testing.T) {
	for _, c := range []struct {
		template, path string
		want           bool
	}{
		{"/DefaultBook/Level/{level}", "/DefaultBook/Level/A1", true},
		{"/DefaultBook/Level/{level}", "/defaultbook/level/{level}", true},
		{"/Book/{Code}", "/Book/abc/Article", false},
		{"/Book/id/{id}", "/Book/x/7", false},
	} {
		if got := MatchPath(c.template, c.path); got != c.want {
			t.Errorf("MatchPath(%q, %q) = %v", c.template, c.path, got)
		}
	}
}

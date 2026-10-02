package review

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fada4773-sketch/apigen-apitest/internal/gen/apply"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/defaults"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/dict"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/yamldoc"
	"github.com/fada4773-sketch/apigen-apitest/internal/spec"
)

func review(t *testing.T, defaultsJSON string) *Result {
	t.Helper()
	const path = "../../../testdata/gen/review.yaml"
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	d, notes, _ := dict.Build(s, nil, dict.Options{Seed: 1})
	defs, err := defaults.Parse([]byte(defaultsJSON))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := yamldoc.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"id"}
	res := apply.Apply(doc, s, d, defs, apply.Options{Seed: 1, GenericIDs: ids})
	return Run(Input{Spec: s, Dict: d, DictNotes: notes, Defaults: defs, GenericIDs: ids, Apply: res})
}

func find(r *Result, action, contains string) *Suggestion {
	for i, s := range r.Suggestions {
		if s.Action == action && strings.Contains(s.Key+" "+s.Where+" "+s.Message+" "+s.Fix, contains) {
			return &r.Suggestions[i]
		}
	}
	return nil
}

func TestReviewProposesFixes(t *testing.T) {
	r := review(t, `{"Dock": {"DockCode": "x"}}`)
	for _, c := range []struct{ action, contains, key string }{
		{ActionDefault, "getDock.dockCode", "getDock.dockCode"},                       // heuristic binding
		{ActionDefault, "getDock.x-apitest-forbidden", "getDock.x-apitest-forbidden"}, // no 403
		{ActionChoose, "listDocks.zone", "listDocks.zone"},                            // NOT_BUILDABLE, pattern unsolved
		{ActionChoose, "/pilots/{id}", "/pilots/{id}"},                                // generic id without producer
		{ActionChoose, "Dock.Sealed", "Dock.Sealed"},                                  // NO_VALUE
		{ActionSpec, `did you mean "getDock"`, ""},                                    // link to an unknown operation
		{ActionSpec, "neither 401 nor 403", ""},                                       // getPilot
		{ActionSpec, "examples.broken", ""},                                           // curated named example
		{ActionApply, "paths./docks/{dockCode}.get.responses.200", ""},                // invalid example
		{ActionEdit, "#/components/schemas/Dock", "Dock"},                             // a DTO name as key
	} {
		s := find(r, c.action, c.contains)
		if s == nil {
			t.Errorf("no %s suggestion with %q", c.action, c.contains)
			continue
		}
		if s.Key != c.key {
			t.Errorf("%s %q: key %q, want %q", c.action, c.contains, s.Key, c.key)
		}
	}
	if b := find(r, ActionDefault, "getDock.dockCode"); b != nil {
		if got, _ := json.Marshal(b.Value); string(got) != `{"bind":"createDock","pointer":"/DockCode"}` {
			t.Errorf("binding: %s", got)
		}
	}
	if n := len(r.Suggestions); n > 11 {
		for _, s := range r.Suggestions {
			t.Logf("%s %s %s: %s", s.Action, s.Key, s.Where, s.Message)
		}
		t.Errorf("%d suggestions, duplicates?", n)
	}
}

// Keys the defaults already have are decided and not proposed again.
func TestReviewSkipsKnownKeys(t *testing.T) {
	r := review(t, `{"getDock.dockCode": {"bind": "createDock", "pointer": "/DockCode"}, "/pilots/{id}": 3}`)
	if s := find(r, ActionDefault, "getDock.dockCode"); s != nil {
		t.Errorf("proposed again: %+v", s)
	}
	if s := find(r, ActionChoose, "/pilots/{id}"); s != nil {
		t.Errorf("proposed again: %+v", s)
	}
}

// The file is a valid defaults file: proposed entries are active, the
// rest are comments the loader ignores.
func TestReviewFile(t *testing.T) {
	r := review(t, `{}`)
	b := r.File()
	defs, err := defaults.Parse(b)
	if err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	keys := defs.Keys()
	if len(keys) != r.Count(ActionDefault) {
		t.Errorf("active keys %v, want %d", keys, r.Count(ActionDefault))
	}
	if !strings.Contains(string(b), `"$choose listDocks.zone"`) || !strings.Contains(string(b), `"$spec": [`) {
		t.Errorf("file:\n%s", b)
	}
}

func TestClosest(t *testing.T) {
	if c := closest("getDok", []string{"getDock", "listDocks"}); c != "getDock" {
		t.Errorf("closest: %q", c)
	}
	if c := closest("xyz", []string{"getDock"}); c != "" {
		t.Errorf("closest: %q", c)
	}
}

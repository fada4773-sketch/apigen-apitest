package scenario

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// seedInstance is a running instance for testdata/gen/seed.yaml. It
// records the list requests in the order they were sent.
type seedInstance struct {
	gardens, moons []any
	docks          map[string][]any // by request
	lists          []string
}

func (s *seedInstance) fetch(_ context.Context, path string) (any, error) {
	switch {
	case path == "/Moon":
		s.lists = append(s.lists, path)
		return s.moons, nil
	case path == "/Garden":
		s.lists = append(s.lists, path)
		return s.gardens, nil
	case strings.HasPrefix(path, "/DefaultDock/"):
		s.lists = append(s.lists, path)
		if d, ok := s.docks[path]; ok {
			return d, nil
		}
		return []any{}, nil
	}
	for _, group := range [][]any{s.moons, s.gardens} {
		for _, it := range group {
			m := it.(map[string]any)
			if path == "/Moon/"+fmt.Sprint(m["Id"]) || path == "/Garden/"+fmt.Sprint(m["Code"]) {
				return m, nil
			}
		}
	}
	for _, list := range s.docks {
		for _, it := range list {
			if m := it.(map[string]any); path == "/Dock/"+fmt.Sprint(m["Code"]) {
				return m, nil
			}
		}
	}
	return nil, errors.New("status 404")
}

func newSeedInstance() *seedInstance {
	return &seedInstance{
		moons: []any{map[string]any{"Id": json.Number("1"), "Code": "moon"}},
		gardens: []any{
			map[string]any{"Code": "a", "Level": "L1"},
			map[string]any{"Code": "b", "Level": nil}, // no level: not seeded
			map[string]any{"Code": "c", "Level": "L2"},
		},
		docks: map[string][]any{
			"/DefaultDock/Level/L1?dockCode=a":    {map[string]any{"Code": "x", "Name": "X1"}},
			"/DefaultDock/Level/L2?dockCode=c":    {map[string]any{"Code": "y", "Name": "Y2"}, map[string]any{"Code": "x", "Name": "X1"}},
			"/DefaultDock/Level/L1?dockCode=moon": {map[string]any{"Code": "m", "Name": "M1"}},
		},
	}
}

func (s *seedInstance) run(t *testing.T, defs string) outcome {
	t.Helper()
	p := newPipelineFile(t, "seed.yaml", defs)
	p.fetch = s.fetch
	return p.run()
}

func recordCodes(o outcome, resource string) string {
	var out []string
	for _, r := range o.res.Records.Records(resource) {
		out = append(out, fmt.Sprint(r["Code"]))
	}
	return strings.Join(out, ",")
}

// The entries of "$snapshot" run in the order of the defaults, not by
// name or by the order of the model.
func TestSnapshotRunsInDefaultsOrder(t *testing.T) {
	for _, c := range []struct {
		defs string
		want string
	}{
		{`{"$snapshot": {"Moon": {"from": "/Moon"}, "Garden": {"from": "/Garden"}}}`, "/Moon,/Garden"},
		{`{"$snapshot": {"Garden": {"from": "/Garden"}, "Moon": {"from": "/Moon"}}}`, "/Garden,/Moon"},
	} {
		in := newSeedInstance()
		o := in.run(t, c.defs)
		var lists []string
		for _, l := range in.lists {
			if l == "/Moon" || l == "/Garden" {
				lists = append(lists, l)
			}
		}
		if got := strings.Join(lists, ","); !strings.HasPrefix(got, c.want) {
			t.Errorf("%s: lists asked in the order %s, want %s first; problems %v", c.defs, got, c.want, o.res.Problems)
		}
	}
}

// "seed" keeps fields of the chosen elements, one set per record. A later
// "from" takes them as placeholders ({code} fills dockCode) and is sent
// once per set; the elements of all answers are searched together. An
// element without a value in a seed field is not chosen. An entry that
// needs a seed of a later entry waits for it.
func TestSnapshotSeed(t *testing.T) {
	in := newSeedInstance()
	o := in.run(t, `{"$snapshot": {
		"Dock": {"from": "/DefaultDock/Level/{level}?dockCode={code}", "count": 2},
		"Garden": {"from": "/Garden", "count": 2, "seed": ["level", "Code"]}}}`)
	if len(o.res.Problems) > 0 {
		t.Fatalf("problems: %v\n%s", o.res.Problems, notes(o.res))
	}
	if got := recordCodes(o, "Garden"); got != "a,c" {
		t.Errorf("gardens %s, want a,c (b has no level)", got)
	}
	var docks []string
	for _, l := range in.lists {
		if strings.HasPrefix(l, "/DefaultDock/") {
			docks = append(docks, l)
		}
	}
	if got := strings.Join(docks, " "); got != "/DefaultDock/Level/L1?dockCode=a /DefaultDock/Level/L2?dockCode=c" {
		t.Errorf("dock requests %s", got)
	}
	// x is in both answers and counts once
	if got := recordCodes(o, "Dock"); got != "x,y" {
		t.Errorf("docks %s, want x,y", got)
	}
	if !hasNote(o.res, CodeSeed, `#1 level="L1" Code="a"; #2 level="L2" Code="c"`) {
		t.Errorf("seed note missing:\n%s", notes(o.res))
	}
}

// {code} takes the seed of the entry that ran last, {Garden.code} the one
// of Garden.
func TestSnapshotSeedLastAndQualified(t *testing.T) {
	const entries = `"Garden": {"from": "/Garden", "seed": ["level", "code"]},
		"Moon": {"from": "/Moon", "seed": ["code"]}`
	for _, c := range []struct {
		from string
		want string
	}{
		{"/DefaultDock/Level/{level}?dockCode={code}", "/DefaultDock/Level/L1?dockCode=moon"},
		{"/DefaultDock/Level/{Garden.level}?dockCode={Garden.code}", "/DefaultDock/Level/L1?dockCode=a"},
	} {
		in := newSeedInstance()
		o := in.run(t, `{"$snapshot": {`+entries+`, "Dock": {"from": "`+c.from+`"}}}`)
		if len(o.res.Problems) > 0 {
			t.Fatalf("%s: problems %v", c.from, o.res.Problems)
		}
		if last := in.lists[len(in.lists)-1]; !strings.HasPrefix(last, "/DefaultDock/") || !containsString(in.lists, c.want) {
			t.Errorf("%s: requests %v, want %s", c.from, in.lists, c.want)
		}
	}
}

// A seed field that is no field of the response stops the run before the
// list is searched; a placeholder no seed and no default fills stops it too.
func TestSnapshotSeedMistakes(t *testing.T) {
	in := newSeedInstance()
	o := in.run(t, `{"$snapshot": {"Garden": {"from": "/Garden", "seed": ["levle"]}}}`)
	if len(o.res.Problems) == 0 || !strings.Contains(o.res.Problems[0].Message, `seed: "levle" matches no field`) {
		t.Errorf("typo: %v", o.res.Problems)
	}
	in = newSeedInstance()
	o = in.run(t, `{"$snapshot": {"Garden": {"from": "/Garden", "seed": ["level"]}, "Dock": {"from": "/DefaultDock/Level/{lvl}"}}}`)
	if len(o.res.Problems) == 0 || !strings.Contains(o.res.Problems[0].Message, `{lvl} in "/DefaultDock/Level/{lvl}" has no value`) {
		t.Errorf("unknown placeholder: %v", o.res.Problems)
	}
}

func containsString(list []string, s string) bool {
	for _, l := range list {
		if l == s {
			return true
		}
	}
	return false
}

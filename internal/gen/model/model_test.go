package model

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/fada4773-sketch/apigen-apitest/internal/gen/defaults"
	"github.com/fada4773-sketch/apigen-apitest/internal/spec"
)

func load(t *testing.T, file string) *spec.Spec {
	t.Helper()
	s, err := spec.Load(context.Background(), "../../../testdata/gen/"+file)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDetect(t *testing.T) {
	m := Detect(load(t, "records.yaml"), nil)
	if len(m.Resources) != 2 {
		t.Fatalf("resources: %v", m.Describe())
	}
	dock, ship := m.Resource("Dock"), m.Resource("ship")
	if dock == nil || ship == nil {
		t.Fatalf("resources: %v", m.Describe())
	}
	if dock.Read() != "DockRead" || !slices.Contains(dock.Schemas, "DockUpdate") {
		t.Errorf("Dock schemas: %v", dock.Schemas)
	}
	if !slices.Equal(dock.Keys, []string{"Id", "Code"}) {
		t.Errorf("Dock keys: %v", dock.Keys)
	}
	if ship.Parent != dock || !slices.Equal(ship.Keys, []string{"Id"}) || !slices.Equal(ship.Relations, []string{"DockId"}) {
		t.Errorf("Ship: parent %v keys %v relations %v", ship.Parent, ship.Keys, ship.Relations)
	}
	roles := map[string]Role{}
	for _, r := range m.Resources {
		for _, o := range r.Ops {
			roles[o.Op.ID] = o.Role
		}
	}
	want := map[string]Role{"GetDocks": RoleList, "GetDockById": RoleRead, "GetDock": RoleRead, "UpdateDockById": RoleUpdate,
		"UpdateDock": RoleUpdate, "GetShips": RoleList, "GetShipById": RoleRead, "UpdateShipById": RoleUpdate}
	for id, role := range want {
		if roles[id] != role {
			t.Errorf("%s: role %q, want %q", id, roles[id], role)
		}
	}
	ships := m.OpByID("GetShips")
	if p := ships.Param("Code"); p == nil || p.Resource != dock || p.Field != "Code" {
		t.Errorf("GetShips {Code}: %+v", p)
	}
	if p := m.OpByID("GetShipById").Param("id"); p == nil || p.Resource != ship || p.Field != "Id" {
		t.Errorf("GetShipById {id}: %+v", p)
	}
	if m.Op(load(t, "records.yaml").Ops[0]) != nil {
		t.Error("an operation of another spec has a model")
	}
	if d := strings.Join(m.Describe(), "\n"); !strings.Contains(d, "Ship: schemas ShipRead, ShipUpdate; keys Id; below Dock; list GetShips; read GetShipById; update UpdateShipById") {
		t.Errorf("describe:\n%s", d)
	}
}

func TestDetectFixes(t *testing.T) {
	fixes := map[string]defaults.ModelFix{
		"Dock":    {Keys: []string{"name", "Nope"}},
		"Planet":  {Keys: []string{"Id"}},
		"ShipSet": {Schemas: []string{"Missing"}},
	}
	m := Detect(load(t, "records.yaml"), fixes)
	if dock := m.Resource("Dock"); !slices.Equal(dock.Keys, []string{"Id", "Code", "Name"}) {
		t.Errorf("Dock keys: %v", dock.Keys)
	}
	var notes []string
	for _, n := range m.Notes {
		notes = append(notes, n.Where+": "+n.Message)
	}
	all := strings.Join(notes, "\n")
	for _, want := range []string{`Dock has no field "Nope"`, "$model.Planet: no GET returns this resource", `schema "Missing" does not exist`} {
		if !strings.Contains(all, want) {
			t.Errorf("notes lack %q:\n%s", want, all)
		}
	}
}

func TestStem(t *testing.T) {
	for in, want := range map[string]string{"BookRead": "Book", "BookUpdateDto": "Book", "Response": "Response", "Book": "Book", "PlanetBase": "Planet"} {
		if got := Stem(in); got != want {
			t.Errorf("Stem(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSegmentBefore(t *testing.T) {
	for _, c := range []struct{ path, param, want string }{
		{"/Book/id/{id}", "id", "Book"},
		{"/Book/{Code}/Article/id/{id}", "id", "Article"},
		{"/Book/{Code}/Article/id/{id}", "Code", "Book"},
		{"/book/class/{class}", "class", "book"},
		{"/{a}/{b}", "b", ""},
	} {
		if got := segmentBefore(c.path, c.param); got != c.want {
			t.Errorf("segmentBefore(%q, %q) = %q, want %q", c.path, c.param, got, c.want)
		}
	}
}

// An object with one list is a page only with a list field like "items" or
// a DTO name like "…Page"; a Pilot with its Ships is a read of the Pilot.
func TestPage(t *testing.T) {
	m := Detect(load(t, "mandatory.yaml"), nil)
	if o := m.OpByID("GetPilot"); o == nil || o.Role != RoleRead || o.Resource.Name != "Pilot" {
		t.Errorf("GetPilot: %+v", o)
	}
	if m.Resource("ShipInfo") != nil {
		t.Errorf("resources: %v", m.Describe())
	}
	for _, c := range []struct {
		dto, field string
		want       bool
	}{{"ShipPage", "ships", true}, {"Ships", "items", true}, {"PilotRead", "Ships", false}} {
		if got := page(c.dto, c.field); got != c.want {
			t.Errorf("page(%q, %q) = %v", c.dto, c.field, got)
		}
	}
}

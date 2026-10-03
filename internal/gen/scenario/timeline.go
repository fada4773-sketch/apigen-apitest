package scenario

import (
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/apigen-apitest/internal/bind"
	"github.com/fada4773-sketch/apigen-apitest/internal/cases"
	"github.com/fada4773-sketch/apigen-apitest/internal/compare"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/defaults"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/dict"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/model"
	"github.com/fada4773-sketch/apigen-apitest/internal/plan"
	"github.com/fada4773-sketch/apigen-apitest/internal/spec"
)

// Order returns the cases in the order apitest runs them with the Config
// of "$apitest", and the bindings apitest uses.
func Order(s *spec.Spec, run defaults.Run) ([]*cases.Case, *bind.Set, error) {
	binds, err := bind.Resolve(s)
	if err != nil {
		return nil, nil, err
	}
	all, err := cases.Build(s, cases.Options{})
	if err != nil {
		return nil, nil, err
	}
	cases.ApplyMethodOrder(all, run.MethodOrder)
	opt := cases.Options{Tags: run.Tags, IncludeOps: run.IncludeOps, ExcludeOps: run.ExcludeOps}
	if err := cases.CheckOptions(s, opt); err != nil {
		return nil, nil, fmt.Errorf("\"$apitest\": %w", err)
	}
	p, err := plan.Build(all, func(c *cases.Case) bool { return cases.Selected(c.Op, opt) }, binds, plan.Options{Tags: run.Tags, DeleteLast: run.DeleteLast})
	if err != nil {
		return nil, nil, err
	}
	return p.Cases(), binds, nil
}

// ref names a record: one of the start records or one a POST created.
type ref struct {
	idx     int
	created *spec.Operation
}

// state is the data at one point of the run.
type state struct {
	records map[string][]Record // resource → records; nil once deleted
	created map[*spec.Operation]Record
	order   []*spec.Operation // creates in the order they ran
	changed map[string]string // record → the case that changed it last
}

func newState(store *Store) *state {
	st := &state{records: map[string][]Record{}, created: map[*spec.Operation]Record{}, changed: map[string]string{}}
	for name, recs := range store.records {
		for _, r := range recs {
			st.records[name] = append(st.records[name], r.clone())
		}
	}
	return st
}

func (st *state) get(r *model.Resource, at ref) Record {
	if at.created != nil {
		return st.created[at.created]
	}
	recs := st.records[strings.ToLower(r.Name)]
	if at.idx < len(recs) {
		return recs[at.idx]
	}
	return nil
}

func (st *state) put(r *model.Resource, at ref, rec Record) {
	if at.created != nil {
		if _, ok := st.created[at.created]; !ok {
			st.order = append(st.order, at.created)
		}
		st.created[at.created] = rec
		return
	}
	recs := st.records[strings.ToLower(r.Name)]
	if at.idx < len(recs) {
		recs[at.idx] = rec
	}
}

func (st *state) list(r *model.Resource) []Record {
	var out []Record
	for _, rec := range st.records[strings.ToLower(r.Name)] {
		if rec != nil {
			out = append(out, rec.clone())
		}
	}
	return out
}

// all returns the records of r a list can return: the start records, then
// the created ones in the order they were created; deleted ones are left out.
func (st *state) all(r *model.Resource, m *model.Model) []Record {
	out := st.list(r)
	for _, op := range st.order {
		rec := st.created[op]
		if o := m.Op(op); o == nil || o.Resource != r || rec == nil {
			continue
		}
		// the same keys are the same record: the POST replaced it
		replaced := false
		for i, x := range out {
			if matching(r, []Record{x}, map[string]any(rec)) != nil {
				out[i], replaced = rec.clone(), true
				break
			}
		}
		if !replaced {
			out = append(out, rec.clone())
		}
	}
	return out
}

// filter keeps the records whose fields have the values of the list's own
// path parameters, taken from rec (/book/class/{class} lists the books of
// one class).
func filter(o *model.Op, recs []Record, rec Record) []Record {
	var own []model.Param
	for _, mp := range o.Params {
		if mp.Resource == o.Resource {
			own = append(own, mp)
		}
	}
	if len(own) == 0 || rec == nil {
		return recs
	}
	var out []Record
	for _, r := range recs {
		keep := true
		for _, mp := range own {
			keep = keep && compare.Equal(spec.Normalize(r[mp.Field]), spec.Normalize(rec[mp.Field]))
		}
		if keep {
			out = append(out, r)
		}
	}
	return out
}

func key(r *model.Resource, at ref) string {
	if at.created != nil {
		return r.Name + " created by " + at.created.ID
	}
	return fmt.Sprintf("%s #%d", r.Name, at.idx+1)
}

// step is one case and the data it meets.
type step struct {
	c      *cases.Case
	o      *model.Op
	at     ref
	before Record // the record before the case; nil for lists and creates
	after  Record // the record after it; nil after a DELETE
	parent Record // the parent record of a list or a create
	list   []Record
	body   any // request body to write for the default example
}

type timeline struct {
	in    Input
	res   *Result
	store *Store
	binds *bind.Set
	st    *state
}

// play runs the cases in apitest's order on the records.
func (t *timeline) play() ([]*step, error) {
	order, binds, err := Order(t.in.Spec, t.in.Defaults.RunConfig())
	if err != nil {
		return nil, err
	}
	t.binds = binds
	t.st = newState(t.store)
	var steps []*step
	for _, c := range order {
		if c.Kind != cases.Positive || c.Skip != "" {
			continue
		}
		o := t.in.Model.Op(c.Op)
		if o == nil {
			continue
		}
		s := &step{c: c, o: o}
		r := o.Resource
		switch o.Role {
		case model.RoleList:
			s.at = t.target(c, o)
			s.before = t.st.get(r, s.at)
			s.list = filter(o, t.st.all(r, t.in.Model), s.before)
			s.parent = t.parent(o)
		case model.RoleCreate:
			s.parent = t.parent(o)
			s.at = ref{created: c.Op}
			s.after, s.body = t.create(c, o, s.parent)
			// apitest binds the value of the first successful case of a
			// producer, so later creates of the same operation do not count
			if t.st.created[c.Op] == nil {
				t.st.put(r, s.at, s.after)
			}
		default:
			s.at = t.target(c, o)
			s.before = t.st.get(r, s.at)
			if s.before == nil {
				if s.at.created != nil || s.at.idx < len(t.store.Records(r.Name)) {
					t.res.note(CodeUpdate, c.Op.Where, "%s runs after %s was deleted; its example is not changed", c.Name, key(r, s.at))
				}
				continue
			}
			s.parent = t.parent(o)
			s.after = s.before
			switch o.Role {
			case model.RoleUpdate:
				if c.Example == cases.DefaultExample {
					s.after, s.body = t.update(c, o, s.before)
				} else if obj, ok := c.Body.(map[string]any); ok {
					s.after = overlay(r, s.before, obj)
				}
				t.st.put(r, s.at, s.after)
				t.st.changed[key(r, s.at)] = c.Name
				t.res.Stats.Updates++
			case model.RoleDelete:
				s.after = nil
				t.st.put(r, s.at, nil)
				t.st.changed[key(r, s.at)] = c.Name
			}
		}
		steps = append(steps, s)
	}
	return steps, nil
}

// target is the record a read, update or delete addresses: the one a POST
// created when apitest binds the key to that POST, else the first record.
func (t *timeline) target(c *cases.Case, o *model.Op) ref {
	for _, mp := range o.Params {
		if mp.Resource != o.Resource {
			continue
		}
		p := specParam(c.Op, mp.Name)
		if p == nil {
			continue
		}
		// a bound key comes from the producer at run time, even after the
		// record it created was deleted
		if b := t.binds.For(c.Op, p); b != nil {
			if po := t.in.Model.Op(b.Producer); po != nil && po.Role == model.RoleCreate && po.Resource == o.Resource {
				if _, created := t.st.created[b.Producer]; created {
					return ref{created: b.Producer}
				}
			}
		}
	}
	return ref{}
}

// parent is the first record of the resource a list or create runs below.
func (t *timeline) parent(o *model.Op) Record {
	for _, mp := range o.Params {
		if mp.Resource != o.Resource {
			if recs := t.st.list(mp.Resource); len(recs) > 0 {
				return recs[0]
			}
		}
	}
	return nil
}

// update builds the body of the default example of an update: every simple
// field gets a new valid value, except keys, fields that refer to the
// parent and fields an operation default sets ("UpdateBook.Name").
func (t *timeline) update(c *cases.Case, o *model.Op, before Record) (Record, any) {
	r := o.Resource
	pl := requestPlace(t.in.Doc, c.Op)
	if pl == nil || pl.named {
		return before, nil
	}
	base, _ := pl.example().(map[string]any)
	after := before.clone()
	props, _ := dict.Properties(pl.schema.Value)
	var changes []string
	for _, k := range sortedKeys(props) {
		ps := props[k].Value
		f := r.Field(k)
		if ps == nil || ps.ReadOnly || f == "" || r.Fixed(f) {
			continue
		}
		if !simple(ps) {
			if _, has := after[f]; !has && base != nil && base[k] != nil {
				after[f] = spec.Normalize(base[k])
			}
			continue
		}
		// the new value depends only on the record, never on the example
		// written last time, so a second run gives the same body
		cur := after[f]
		var v any
		if e := t.in.Defaults.Scoped(c.Op.ID, k); e != nil {
			v = coerce(e.Value, ps)
		} else if nv, ok := (&builder{in: t.in}).different(ps, cur, "update."+c.Op.ID+"."+k, k, r.Read()); ok {
			v = nv
		}
		if v == nil || compare.Equal(v, cur) {
			continue
		}
		after[f] = v
		changes = append(changes, fmt.Sprintf("%s %s → %s", k, text(cur), text(v)))
	}
	if len(changes) > 0 {
		t.res.note(CodeUpdate, c.Op.Where, "%s changes %s: %s", c.Name, key(r, t.target(c, o)), strings.Join(changes, ", "))
	}
	return after, project(pl.schema, base, after, r, spec.ModeRequest)
}

// create builds the record a POST creates: its body, with the fields that
// refer to the parent set to the parent record, and what its response adds.
func (t *timeline) create(c *cases.Case, o *model.Op, parent Record) (Record, any) {
	r := o.Resource
	rec := Record{}
	var body any
	if pl := requestPlace(t.in.Doc, c.Op); pl != nil {
		var obj map[string]any
		if c.Example == cases.DefaultExample && !pl.named {
			obj, _ = pl.example().(map[string]any)
		} else {
			obj, _ = c.Body.(map[string]any)
		}
		for k, v := range obj {
			if f := r.Field(k); f != "" {
				rec[f] = spec.Normalize(v)
			}
		}
		if parent != nil && r.Parent != nil {
			for _, f := range r.Relations {
				for pk, pv := range parent {
					if strings.EqualFold(f, r.Parent.Name+pk) {
						rec[f] = pv
					}
				}
			}
		}
		if c.Example == cases.DefaultExample && !pl.named {
			body = project(pl.schema, obj, rec, r, spec.ModeRequest)
		}
	}
	sent := rec.clone()
	for _, p := range responsePlaces(t.in.Doc, c.Op) {
		if !p.success {
			continue
		}
		if obj, ok := p.example().(map[string]any); ok {
			for k, v := range obj {
				if f := r.Field(k); f != "" && rec[f] == nil {
					rec[f] = spec.Normalize(v)
				}
			}
		}
		break
	}
	for _, k := range r.Keys {
		ref := r.Schema(k)
		if _, has := sent[k]; has || ref == nil || ref.Value == nil || ref.Value.ReadOnly || presenceOnly(ref.Value) {
			continue
		}
		t.res.note(CodeCreatedKey, c.Op.Where, "the server assigns %s.%s when %s runs; the examples of the cases that read the created %s show %s, the server will return another value. Mark %s readOnly in the spec (apitest then only checks that it is there) or add it to IgnoreFields",
			r.Name, k, c.Op.ID, r.Name, text(rec[k]), k)
	}
	return rec, body
}

// overlay lays the fields of a sent body over a record.
func overlay(r *model.Resource, rec Record, body map[string]any) Record {
	out := rec.clone()
	for k, v := range body {
		f := r.Field(k)
		if f == "" {
			f = k
		}
		out[f] = spec.Normalize(v)
	}
	return out
}

// presenceOnly reports the formats apitest only checks for presence.
func presenceOnly(s *openapi3.Schema) bool {
	switch s.Format {
	case "uuid", "date-time", "date":
		return true
	}
	return false
}

func specParam(op *spec.Operation, name string) *openapi3.Parameter {
	for _, p := range op.Params {
		if p.In == openapi3.ParameterInPath && p.Name == name {
			return p
		}
	}
	return nil
}

// project lays the fields of a record over an example of a schema: every
// property the record has gets its value, the others keep theirs.
func project(ref *openapi3.SchemaRef, base any, rec Record, r *model.Resource, mode spec.Mode) any {
	if ref == nil || ref.Value == nil {
		return base
	}
	out := map[string]any{}
	if obj, ok := base.(map[string]any); ok {
		for k, v := range obj {
			out[k] = v
		}
	}
	props, _ := dict.Properties(ref.Value)
	for k, p := range props {
		if p.Value == nil || (mode == spec.ModeRequest && p.Value.ReadOnly) || (mode == spec.ModeResponse && p.Value.WriteOnly) {
			continue
		}
		f := r.Field(k)
		if f == "" {
			continue
		}
		if v, ok := rec[f]; ok && v != nil {
			out[k] = coerce(v, p.Value)
		}
	}
	return out
}

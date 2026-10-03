// Package model detects the resources of a spec: which DTOs describe the
// same thing (BookRead, BookUpdate → Book), which fields identify a record
// (the path parameters /Book/id/{id} and /Book/{Code} → Id and Code), which
// operations list, read, create, update and delete it, and which resource
// another one belongs to (/Book/{Code}/Article → Article below Book).
//
// The generator uses the model to give all examples of a resource the
// values of one record, so a GET after an update expects what the update
// sent. Nothing of the model is written into the spec.
package model

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/apigen-apitest/internal/gen/defaults"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/dict"
	"github.com/fada4773-sketch/apigen-apitest/internal/spec"
)

// Role is what an operation does with its resource.
type Role string

// Roles.
const (
	RoleList   Role = "list"
	RoleRead   Role = "read"
	RoleCreate Role = "create"
	RoleUpdate Role = "update"
	RoleDelete Role = "delete"
)

// Resource is one kind of record.
type Resource struct {
	Name string
	// Schemas are the DTOs of the resource, the one its GETs return first.
	Schemas []string
	// Keys identify a record; updates never change them.
	Keys []string
	// Parent is the resource whose record a list of this one is fetched
	// below, e.g. Book for /Book/{Code}/Article.
	Parent *Resource
	// Relations are fields that refer to the parent record (BookCode in an
	// Article); updates do not change them either.
	Relations []string
	Ops       []*Op

	fields openapi3.Schemas
}

// Op is an operation on a resource.
type Op struct {
	Op       *spec.Operation
	Resource *Resource
	Role     Role
	// Items is the JSON pointer of the list in the response of a list
	// operation, "" for a top-level array.
	Items string
	// Params are the path parameters that identify a record.
	Params []Param
}

// Param is a path parameter that holds a key of a record.
type Param struct {
	Name     string
	Resource *Resource
	Field    string
}

// Note is something the detection could not decide.
type Note struct {
	Where, Message string
}

// Model is the detected model of a spec.
type Model struct {
	Resources []*Resource
	Notes     []Note
	ops       map[*spec.Operation]*Op
}

// Op returns the model of an operation, or nil if it belongs to no
// resource.
func (m *Model) Op(op *spec.Operation) *Op {
	if m == nil {
		return nil
	}
	return m.ops[op]
}

// OpByID returns the model of an operation by its operationId.
func (m *Model) OpByID(id string) *Op {
	if m == nil {
		return nil
	}
	for op, o := range m.ops {
		if op.ID == id {
			return o
		}
	}
	return nil
}

// Resource returns a resource by name, ignoring case.
func (m *Model) Resource(name string) *Resource {
	if m == nil {
		return nil
	}
	for _, r := range m.Resources {
		if strings.EqualFold(r.Name, name) {
			return r
		}
	}
	return nil
}

// Param returns the parameter of o with the name, or nil.
func (o *Op) Param(name string) *Param {
	for i := range o.Params {
		if o.Params[i].Name == name {
			return &o.Params[i]
		}
	}
	return nil
}

// Field returns the name of the field of r that matches name, ignoring
// case, or "".
func (r *Resource) Field(name string) string {
	if r.fields[name] != nil {
		return name
	}
	for _, k := range sortedKeys(r.fields) {
		if strings.EqualFold(k, name) {
			return k
		}
	}
	return ""
}

// FieldNames returns the fields of all DTOs of r, sorted.
func (r *Resource) FieldNames() []string { return sortedKeys(r.fields) }

// Schema returns the schema of a field of r.
func (r *Resource) Schema(field string) *openapi3.SchemaRef {
	if f := r.Field(field); f != "" {
		return r.fields[f]
	}
	return nil
}

// Fixed reports whether a field identifies the record or refers to its
// parent; updates leave such fields alone.
func (r *Resource) Fixed(field string) bool {
	match := func(k string) bool { return strings.EqualFold(k, field) }
	return slices.ContainsFunc(r.Keys, match) || slices.ContainsFunc(r.Relations, match)
}

// OpsWith returns the operations of r with the role, in spec order.
func (r *Resource) OpsWith(role Role) []*Op {
	var out []*Op
	for _, o := range r.Ops {
		if o.Role == role {
			out = append(out, o)
		}
	}
	return out
}

// Read is the DTO the GETs of r return.
func (r *Resource) Read() string { return r.Schemas[0] }

// suffixes are removed from DTO names to find the resource they describe.
var suffixes = []string{"Read", "Update", "Create", "Write", "Patch", "Put", "Post", "Dto", "DTO",
	"Request", "Response", "Details", "Detail", "View", "Model", "Input", "Output", "Summary",
	"Base", "Data", "Body", "Payload", "Item", "Entity"}

// Stem returns the resource name of a DTO name: BookRead → Book.
func Stem(name string) string {
	for changed := true; changed; {
		changed = false
		for _, s := range suffixes {
			if strings.HasSuffix(name, s) && len(name) > len(s) {
				name, changed = strings.TrimSuffix(name, s), true
			}
		}
	}
	return name
}

// Detect builds the model of s. fixes are the "$model" entries.
func Detect(s *spec.Spec, fixes map[string]defaults.ModelFix) *Model {
	m := &Model{ops: map[*spec.Operation]*Op{}}
	d := &detector{s: s, m: m, family: map[string]string{}, fixes: fixes}
	d.families()
	d.resources()
	d.roles()
	d.params()
	d.keys()
	sort.SliceStable(m.Resources, func(i, j int) bool { return m.Resources[i].Name < m.Resources[j].Name })
	return m
}

type detector struct {
	s      *spec.Spec
	m      *Model
	fixes  map[string]defaults.ModelFix
	family map[string]string // DTO → resource name
}

func (d *detector) schemas() openapi3.Schemas {
	if d.s.Doc.Components == nil {
		return nil
	}
	return d.s.Doc.Components.Schemas
}

// families groups the DTOs by stem; "$model" schemas override it.
func (d *detector) families() {
	for name := range d.schemas() {
		d.family[name] = Stem(name)
	}
	for res, f := range d.fixes {
		for _, name := range f.Schemas {
			if d.schemas()[name] == nil {
				d.m.Notes = append(d.m.Notes, Note{"$model." + res, fmt.Sprintf("schema %q does not exist", name)})
				continue
			}
			d.family[name] = res
		}
	}
}

// resources creates a resource for every family a GET returns.
func (d *detector) resources() {
	for _, op := range d.s.Ops {
		if op.Method != http.MethodGet {
			continue
		}
		dto, _, _ := listOrItem(op)
		if dto == "" {
			continue
		}
		name := d.family[dto]
		r := d.m.Resource(name)
		if r == nil {
			r = &Resource{Name: name}
			d.m.Resources = append(d.m.Resources, r)
		}
		if !slices.Contains(r.Schemas, dto) {
			r.Schemas = append(r.Schemas, dto)
		}
	}
	for _, r := range d.m.Resources {
		if f, ok := d.fix(r.Name); ok && len(f.Schemas) > 0 {
			r.Schemas = append([]string(nil), f.Schemas...)
		}
		for _, name := range sortedKeys(d.schemas()) {
			if d.family[name] == r.Name && !slices.Contains(r.Schemas, name) {
				r.Schemas = append(r.Schemas, name)
			}
		}
		r.fields = openapi3.Schemas{}
		for _, name := range r.Schemas { // the read DTO first: its fields win
			if ref := d.schemas()[name]; ref != nil && ref.Value != nil {
				props, _ := dict.Properties(ref.Value)
				for k, v := range props {
					if r.Field(k) == "" {
						r.fields[k] = v
					}
				}
			}
		}
	}
	for res := range d.fixes {
		if d.m.Resource(res) == nil {
			d.m.Notes = append(d.m.Notes, Note{"$model." + res, "no GET returns this resource; the entry has no effect"})
		}
	}
}

func (d *detector) fix(name string) (defaults.ModelFix, bool) {
	for k, f := range d.fixes {
		if strings.EqualFold(k, name) {
			return f, true
		}
	}
	return defaults.ModelFix{}, false
}

// roles assigns every operation to a resource; the GETs first, because a
// DELETE takes the resource read at its path.
func (d *detector) roles() {
	ops := slices.Clone(d.s.Ops)
	sort.SliceStable(ops, func(i, j int) bool {
		return ops[i].Method == http.MethodGet && ops[j].Method != http.MethodGet
	})
	for _, op := range ops {
		var o *Op
		switch op.Method {
		case http.MethodGet:
			if dto, items, list := listOrItem(op); dto != "" {
				o = &Op{Op: op, Resource: d.m.Resource(d.family[dto]), Role: RoleRead, Items: items}
				if list {
					o.Role = RoleList
				}
			}
		case http.MethodPut, http.MethodPatch, http.MethodPost:
			role := RoleUpdate
			if op.Method == http.MethodPost {
				role = RoleCreate
			}
			if r := d.m.Resource(d.family[requestDTO(op)]); r != nil {
				o = &Op{Op: op, Resource: r, Role: role}
			}
		case http.MethodDelete:
			if r := d.deleted(op); r != nil {
				o = &Op{Op: op, Resource: r, Role: RoleDelete}
			}
		}
		if o != nil && o.Resource != nil {
			d.m.ops[op] = o
		}
	}
	for _, op := range d.s.Ops { // spec order
		if o := d.m.ops[op]; o != nil {
			o.Resource.Ops = append(o.Resource.Ops, o)
		}
	}
}

// deleted finds the resource a DELETE removes: the one read at the same
// path, or the one named in front of the last path parameter.
func (d *detector) deleted(op *spec.Operation) *Resource {
	for _, other := range d.s.Ops {
		if other.Path == op.Path && other.Method == http.MethodGet {
			if o := d.m.ops[other]; o != nil && o.Role == RoleRead {
				return o.Resource
			}
		}
	}
	if last := lastParam(op.Path); last != "" {
		return d.byName(segmentBefore(op.Path, last))
	}
	return nil
}

func (d *detector) byName(seg string) *Resource {
	if seg == "" {
		return nil
	}
	for _, r := range d.m.Resources {
		if strings.EqualFold(r.Name, seg) || strings.EqualFold(r.Name, singular(seg)) {
			return r
		}
	}
	return nil
}

// params maps the path parameters of each operation to record keys.
func (d *detector) params() {
	for _, op := range d.s.Ops {
		o := d.m.ops[op]
		if o == nil {
			continue
		}
		last := lastParam(op.Path)
		for _, p := range op.Params {
			if p.In != openapi3.ParameterInPath {
				continue
			}
			r := d.byName(segmentBefore(op.Path, p.Name))
			if r == nil && p.Name == last && o.Role != RoleList && o.Role != RoleCreate {
				r = o.Resource
			}
			if r == nil {
				d.m.Notes = append(d.m.Notes, Note{fmt.Sprintf("%s.parameters[%s]", op.Where, p.Name),
					fmt.Sprintf("no resource found for {%s}; its example is not taken from a record", p.Name)})
				continue
			}
			field := keyField(r, p.Name)
			if field == "" {
				d.m.Notes = append(d.m.Notes, Note{fmt.Sprintf("%s.parameters[%s]", op.Where, p.Name),
					fmt.Sprintf("%s has no field for {%s}; set it with \"$model\": {%q: {\"keys\": [...]}}", r.Name, p.Name, r.Name)})
				continue
			}
			o.Params = append(o.Params, Param{Name: p.Name, Resource: r, Field: field})
			if r != o.Resource && o.Resource.Parent == nil && (o.Role == RoleList || o.Role == RoleRead || o.Role == RoleCreate) {
				o.Resource.Parent = r
			}
		}
	}
}

// keys collects the key fields of every resource and the fields that refer
// to the parent.
func (d *detector) keys() {
	for _, r := range d.m.Resources {
		for _, o := range r.Ops {
			for _, p := range o.Params {
				if p.Resource == r && !slices.Contains(r.Keys, p.Field) {
					r.Keys = append(r.Keys, p.Field)
				}
			}
		}
		if f, ok := d.fix(r.Name); ok {
			for _, k := range f.Keys {
				if field := r.Field(k); field != "" && !slices.Contains(r.Keys, field) {
					r.Keys = append(r.Keys, field)
				} else if field == "" {
					d.m.Notes = append(d.m.Notes, Note{"$model." + r.Name, fmt.Sprintf("%s has no field %q", r.Name, k)})
				}
			}
		}
	}
	for _, r := range d.m.Resources {
		if r.Parent == nil {
			continue
		}
		for _, k := range append(append([]string(nil), r.Parent.Keys...), "Id") {
			if f := r.Field(r.Parent.Name + k); f != "" && !slices.Contains(r.Relations, f) {
				r.Relations = append(r.Relations, f)
			}
		}
	}
}

// keyField finds the field of r a path parameter holds: the same name, or
// Id for an id-like name.
func keyField(r *Resource, param string) string {
	if f := r.Field(param); f != "" {
		return f
	}
	lower := strings.ToLower(param)
	if lower == "id" || lower == "uuid" || lower == "key" || strings.HasSuffix(lower, "id") {
		if f := r.Field("id"); f != "" {
			return f
		}
	}
	return r.Field(r.Name + param)
}

// listOrItem returns the DTO of the lowest 2xx JSON response of op, the
// pointer of the list inside it and whether the response is a list.
func listOrItem(op *spec.Operation) (dto, items string, list bool) {
	ref := successSchema(op)
	if ref == nil || ref.Value == nil {
		return "", "", false
	}
	if ref.Value.Items != nil && isArray(ref.Value) {
		return dict.DTORef(ref.Value.Items), "", true
	}
	name := dict.DTORef(ref)
	if name == "" {
		return "", "", false
	}
	// a page: an object with one list of DTOs, e.g. {items: [...], total: 3}
	props, _ := dict.Properties(ref.Value)
	var lists []string
	for _, k := range sortedKeys(props) {
		if p := props[k]; p.Value != nil && isArray(p.Value) && p.Value.Items != nil && dict.DTORef(p.Value.Items) != "" {
			lists = append(lists, k)
		}
	}
	if len(lists) == 1 && len(props) <= 4 {
		return dict.DTORef(props[lists[0]].Value.Items), "/" + lists[0], true
	}
	return name, "", false
}

func isArray(s *openapi3.Schema) bool {
	return s.Type != nil && s.Type.Is("array")
}

// successSchema is the schema of the lowest 2xx JSON response.
func successSchema(op *spec.Operation) *openapi3.SchemaRef {
	if op.Op.Responses == nil {
		return nil
	}
	codes := sortedKeys(op.Op.Responses.Map())
	for _, code := range codes {
		if len(code) != 3 || code[0] != '2' {
			continue
		}
		r := op.Op.Responses.Map()[code].Value
		if r == nil {
			continue
		}
		for _, mt := range sortedKeys(r.Content) {
			if spec.IsJSON(mt) && r.Content[mt].Schema != nil {
				return r.Content[mt].Schema
			}
		}
	}
	return nil
}

// requestDTO is the DTO of the JSON request body of op.
func requestDTO(op *spec.Operation) string {
	rb := op.Op.RequestBody
	if rb == nil || rb.Value == nil {
		return ""
	}
	for _, mt := range sortedKeys(rb.Value.Content) {
		if m := rb.Value.Content[mt]; spec.IsJSON(mt) && m.Schema != nil {
			return dict.DTORef(m.Schema)
		}
	}
	return ""
}

// segmentBefore returns the literal path segment that names the resource of
// a parameter: the one in front of it, skipping words like "id" or "code"
// and a segment equal to the parameter name (/Book/id/{id}, /Book/code/{code}).
func segmentBefore(path, param string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	i := slices.Index(segs, "{"+param+"}")
	for j := i - 1; j >= 0; j-- {
		seg := segs[j]
		switch {
		case strings.HasPrefix(seg, "{"):
			return ""
		case slices.Contains([]string{"id", "ids", "code", "key", "uuid", "by-id", "byid", "name"}, strings.ToLower(seg)),
			strings.EqualFold(seg, param):
			continue
		}
		return seg
	}
	return ""
}

func lastParam(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		if strings.HasPrefix(segs[i], "{") && strings.HasSuffix(segs[i], "}") {
			return segs[i][1 : len(segs[i])-1]
		}
	}
	return ""
}

func singular(w string) string {
	switch {
	case strings.HasSuffix(w, "ies"):
		return strings.TrimSuffix(w, "ies") + "y"
	case strings.HasSuffix(w, "sses"), strings.HasSuffix(w, "xes"):
		return strings.TrimSuffix(w, "es")
	case strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		return strings.TrimSuffix(w, "s")
	}
	return w
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Describe returns one line per resource, for the review.
func (m *Model) Describe() []string {
	var out []string
	for _, r := range m.Resources {
		parts := []string{"schemas " + strings.Join(r.Schemas, ", ")}
		if len(r.Keys) > 0 {
			parts = append(parts, "keys "+strings.Join(r.Keys, ", "))
		}
		if r.Parent != nil {
			parts = append(parts, "below "+r.Parent.Name)
		}
		for _, role := range []Role{RoleList, RoleRead, RoleCreate, RoleUpdate, RoleDelete} {
			var ids []string
			for _, o := range r.OpsWith(role) {
				ids = append(ids, o.Op.ID)
			}
			if len(ids) > 0 {
				parts = append(parts, string(role)+" "+strings.Join(ids, ", "))
			}
		}
		out = append(out, fmt.Sprintf("%s: %s", r.Name, strings.Join(parts, "; ")))
	}
	return out
}

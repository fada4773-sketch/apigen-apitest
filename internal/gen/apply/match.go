package apply

import (
	"reflect"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/apigen-apitest/internal/gen/dict"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/value"
	"github.com/fada4773-sketch/apigen-apitest/internal/spec"
)

// matchPath makes a response example agree with the path of its
// operation: GET /Book/id/{id} with id 100 returns a BookRead with Id 100,
// GET /Book/id/{id}/Article returns Articles with BookId 100. A value that
// came from the defaults is also kept in the dictionary field.
func (a *applier) matchPath(op *spec.Operation, schema *openapi3.SchemaRef, v any) (any, bool) {
	if len(a.pathVals) == 0 || schema == nil || schema.Value == nil {
		return v, false
	}
	item, list := schema, false
	if value.Type(schema.Value) == "array" && schema.Value.Items != nil && schema.Value.Items.Value != nil {
		item, list = schema.Value.Items, true
	}
	dto := dict.DTORef(item)
	props, _ := dict.Properties(item.Value)
	if len(props) == 0 {
		return v, false
	}
	changed := false
	for _, name := range sortedKeys(a.pathVals) {
		pv := a.pathVals[name]
		resource := pathResource(op.Path, name)
		if resource == "" {
			continue
		}
		field := matchField(props, dto, name, resource, a.isGenericName(name))
		if field == "" || props[field].Value == nil {
			continue
		}
		fv := coerce(pv.v, props[field].Value)
		if !a.valid(props[field].Value, fv, spec.ModePlain) {
			continue
		}
		set := func(obj any) any {
			m, ok := obj.(map[string]any)
			if !ok || reflect.DeepEqual(spec.Normalize(m[field]), spec.Normalize(fv)) {
				return obj
			}
			out := make(map[string]any, len(m)+1)
			for k, x := range m {
				out[k] = x
			}
			out[field] = fv
			changed = true
			return out
		}
		if list {
			if xs, ok := v.([]any); ok {
				out := make([]any, len(xs))
				for i, x := range xs {
					out[i] = set(x)
				}
				v = out
			}
		} else {
			v = set(v)
		}
		if pv.fromDefault && dto != "" {
			a.keep(a.dictField(dto, field), fv, "schemas."+dto+"."+field, 0)
		}
	}
	return v, changed
}

// matchField is the field of a response DTO that a path parameter stands
// for. For the resource itself (Book… for /Book/{Code}) it is the field of
// the same name, or Id for a generic id; for another resource (Article
// under /Book/id/{id}/Article) it is <Resource><Name>, e.g. BookId.
func matchField(props openapi3.Schemas, dto, param, resource string, generic bool) string {
	find := func(names ...string) string {
		for _, n := range names {
			for k := range props {
				if strings.EqualFold(k, n) {
					return k
				}
			}
		}
		return ""
	}
	own := dto == "" || strings.HasPrefix(strings.ToLower(dto), strings.ToLower(singularOf(resource)))
	if own {
		names := []string{param}
		if generic {
			names = append(names, "id")
		}
		return find(names...)
	}
	names := []string{resource + param}
	if generic {
		names = append(names, singularOf(resource)+"id")
	}
	return find(names...)
}

// pathResource is the resource a path parameter identifies: the literal
// segment in front of it, skipping a literal "id": /Book/id/{id} → Book.
func pathResource(path, param string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	i := slices.Index(segs, "{"+param+"}")
	j := i - 1
	if j >= 0 && strings.EqualFold(segs[j], "id") {
		j--
	}
	if i < 0 || j < 0 || strings.HasPrefix(segs[j], "{") {
		return ""
	}
	return segs[j]
}

func singularOf(w string) string {
	if s := singulars(w); len(s) > 0 {
		return s[0]
	}
	return w
}

func (a *applier) isGenericName(name string) bool {
	return slices.ContainsFunc(a.opt.GenericIDs, func(g string) bool { return strings.EqualFold(g, name) })
}

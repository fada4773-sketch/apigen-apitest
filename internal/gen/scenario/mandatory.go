package scenario

import (
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/apigen-apitest/internal/gen/dict"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/model"
)

// A mandatory field is a dotted path into a fetched element:
// "author", "BookDetail.Author" or "ReadDTO.BookDetail.Author". A segment
// is a field name (case-insensitive) or the name of a DTO: the path then
// continues at the object of that DTO, the element itself or the first one
// found below it. Inside a list one element with a value is enough.

// mandatory returns the non-empty paths of a "mandatoryfields" list.
func mandatory(list []string) [][]string {
	var out [][]string
	for _, m := range list {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, strings.Split(m, "."))
		}
	}
	return out
}

// hasAll reports whether every path has a value in item: not missing, not
// null, not "" and not an empty list.
func hasAll(item any, ref *openapi3.SchemaRef, paths [][]string) bool {
	for _, p := range paths {
		if !present(item, ref, p, 0) {
			return false
		}
	}
	return true
}

func present(v any, ref *openapi3.SchemaRef, segs []string, depth int) bool {
	if depth > 30 {
		return false
	}
	if list, ok := v.([]any); ok {
		var items *openapi3.SchemaRef
		if ref != nil && ref.Value != nil {
			items = ref.Value.Items
		}
		for _, el := range list {
			if present(el, items, segs, depth+1) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		switch x := v.(type) {
		case nil:
			return false
		case string:
			return x != ""
		}
		return true
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return false
	}
	seg := segs[0]
	for k, child := range obj {
		if strings.EqualFold(k, seg) {
			return present(child, property(ref, k), segs[1:], depth+1)
		}
	}
	if isDTO(ref, seg) {
		return present(v, ref, segs[1:], depth+1)
	}
	// the DTO further down: every field that holds it or contains it
	for k, child := range obj {
		c := property(ref, k)
		if c != nil && contains(c, seg, 0) && present(child, c, segs, depth+1) {
			return true
		}
	}
	return false
}

// resolvable reports whether a path can exist in a schema at all, to catch
// typos before any element is rejected.
func resolvable(ref *openapi3.SchemaRef, segs []string, depth int) bool {
	if len(segs) == 0 {
		return true
	}
	if ref == nil || ref.Value == nil || depth > 30 {
		return false
	}
	if ref.Value.Items != nil {
		return resolvable(ref.Value.Items, segs, depth+1)
	}
	props, _ := dict.Properties(ref.Value)
	for k, c := range props {
		if strings.EqualFold(k, segs[0]) {
			return resolvable(c, segs[1:], depth+1)
		}
	}
	if isDTO(ref, segs[0]) {
		return resolvable(ref, segs[1:], depth+1)
	}
	for _, c := range props {
		if contains(c, segs[0], 0) && resolvable(c, segs, depth+1) {
			return true
		}
	}
	return false
}

// isDTO reports whether ref is the DTO name, directly or as a part of its
// allOf (BookRead = allOf [BookUpdate, …] is a BookUpdate too).
func isDTO(ref *openapi3.SchemaRef, name string) bool {
	if ref == nil {
		return false
	}
	if strings.EqualFold(dict.DTORef(ref), name) || strings.EqualFold(strings.TrimPrefix(ref.Ref, "#/components/schemas/"), name) {
		return true
	}
	if ref.Value != nil {
		for _, part := range ref.Value.AllOf {
			if isDTO(part, name) {
				return true
			}
		}
	}
	return false
}

// contains reports whether the DTO name is ref itself or somewhere below it.
func contains(ref *openapi3.SchemaRef, name string, depth int) bool {
	if ref == nil || ref.Value == nil || depth > 8 {
		return false
	}
	if isDTO(ref, name) {
		return true
	}
	if ref.Value.Items != nil {
		return contains(ref.Value.Items, name, depth+1)
	}
	props, _ := dict.Properties(ref.Value)
	for _, c := range props {
		if contains(c, name, depth+1) {
			return true
		}
	}
	return false
}

// property returns the schema of field k of ref, ignoring case.
func property(ref *openapi3.SchemaRef, k string) *openapi3.SchemaRef {
	if ref == nil || ref.Value == nil {
		return nil
	}
	props, _ := dict.Properties(ref.Value)
	if c := props[k]; c != nil {
		return c
	}
	for name, c := range props {
		if strings.EqualFold(name, k) {
			return c
		}
	}
	return nil
}

// itemRef is the schema of one element of o's response, with its $ref.
func itemRef(o *model.Op) *openapi3.SchemaRef {
	ref := successSchema(o.Op)
	if ref == nil || ref.Value == nil {
		return nil
	}
	if o.Role != model.RoleList {
		return ref
	}
	if o.Items != "" {
		p := property(ref, strings.TrimPrefix(o.Items, "/"))
		if p == nil || p.Value == nil {
			return nil
		}
		return p.Value.Items
	}
	return ref.Value.Items
}

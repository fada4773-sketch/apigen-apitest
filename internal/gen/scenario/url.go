package scenario

import (
	"net/url"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/apigen-apitest/internal/gen/model"
	"github.com/fada4773-sketch/apigen-apitest/internal/params"
)

// Template is the request of o with every parameter as a placeholder:
// "/DefaultBook/Level/{level}?bookCode={bookCode}&isbn={isbn}".
func Template(o *model.Op) string {
	var query []string
	for _, p := range o.Op.Params {
		if p.In == openapi3.ParameterInQuery {
			query = append(query, p.Name+"={"+p.Name+"}")
		}
	}
	if len(query) == 0 {
		return o.Op.Path
	}
	return o.Op.Path + "?" + strings.Join(query, "&")
}

// Fill builds the request of o with the values value returns. A path or
// required query parameter without value stays a placeholder ({level}) and
// is listed in missing; an optional query parameter without value is left
// out.
func Fill(o *model.Op, value func(*openapi3.Parameter) any) (target string, missing []string) {
	path := o.Op.Path
	var query []string
	for _, p := range o.Op.Params {
		v := value(p)
		switch p.In {
		case openapi3.ParameterInPath:
			s, err := params.Path(p, v)
			if v == nil || err != nil {
				missing = append(missing, p.Name)
				continue
			}
			path = strings.ReplaceAll(path, "{"+p.Name+"}", s)
		case openapi3.ParameterInQuery:
			switch {
			case v != nil:
				query = append(query, url.QueryEscape(p.Name)+"="+url.QueryEscape(params.Scalar(v)))
			case p.Required:
				query = append(query, p.Name+"={"+p.Name+"}")
				missing = append(missing, p.Name)
			}
		}
	}
	if len(query) > 0 {
		path += "?" + strings.Join(query, "&")
	}
	return path, missing
}

// MatchPath reports whether a request path (without query) fits a path
// template: "{x}" in the template matches any segment.
func MatchPath(template, path string) bool {
	t := strings.Split(strings.Trim(template, "/"), "/")
	p := strings.Split(strings.Trim(path, "/"), "/")
	if len(t) != len(p) {
		return false
	}
	for i := range t {
		if strings.HasPrefix(t[i], "{") && strings.HasSuffix(t[i], "}") {
			continue
		}
		if !strings.EqualFold(t[i], p[i]) {
			return false
		}
	}
	return true
}

// IsURL reports whether the "from" of "$snapshot" is a request instead of
// an operationId.
func IsURL(from string) bool {
	from = strings.TrimSpace(from)
	return strings.HasPrefix(from, "/") || strings.HasPrefix(from, "GET ")
}

// fillURL completes a request written in "$snapshot": placeholders that are
// left are filled like the parameters of o (keys of records, defaults).
// missing names the first placeholder without value; an optional query
// parameter without value is dropped.
func (b *builder) fillURL(o *model.Op, raw string, rec Record) (string, string) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "GET "))
	path, query, _ := strings.Cut(raw, "?")
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		name, ok := placeholder(seg)
		if !ok {
			continue
		}
		p := specParam(o.Op, name)
		v := b.paramValue(o, p, rec, false)
		if p == nil || v == nil {
			return "", "{" + name + "}"
		}
		s, err := params.Path(p, v)
		if err != nil {
			return "", "{" + name + "}"
		}
		segs[i] = s
	}
	out := strings.Join(segs, "/")
	if query == "" {
		return out, ""
	}
	var pairs []string
	for _, pair := range strings.Split(query, "&") {
		k, val, _ := strings.Cut(pair, "=")
		name, ok := placeholder(val)
		if !ok {
			pairs = append(pairs, pair)
			continue
		}
		p := queryParam(o, name)
		v := b.paramValue(o, p, rec, false)
		switch {
		case v != nil:
			pairs = append(pairs, k+"="+url.QueryEscape(params.Scalar(v)))
		case p != nil && !p.Required:
			// optional and unknown: left out
		default:
			return "", "{" + name + "}"
		}
	}
	if len(pairs) > 0 {
		out += "?" + strings.Join(pairs, "&")
	}
	return out, ""
}

// paramValue is the value of a parameter of o: the key of rec for an own
// key, the key of the first record of another resource, a key default, and
// with examples the example of the parameter.
func (b *builder) paramValue(o *model.Op, p *openapi3.Parameter, rec Record, examples bool) any {
	if p == nil {
		return nil
	}
	if mp := o.Param(p.Name); mp != nil && p.In == openapi3.ParameterInPath {
		if mp.Resource == o.Resource {
			if rec != nil {
				return rec[mp.Field]
			}
			return nil
		}
		// a generated key does not exist in the instance
		if recs := b.store.Records(mp.Resource.Name); len(recs) > 0 && (b.in.Fetch == nil || b.store.Fetched(mp.Resource.Name)) {
			return recs[0][mp.Field]
		}
		return nil
	}
	if o.Op.HasOperationID {
		if e := b.in.Defaults.Scoped(o.Op.ID, p.Name); e != nil {
			return e.Value
		}
	}
	if e := b.in.Defaults.Plain(p.Name); e != nil {
		return e.Value
	}
	if examples {
		return paramExample(b.in.Doc, o.Op, p)
	}
	return nil
}

func placeholder(s string) (string, bool) {
	if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") && len(s) > 2 {
		return s[1 : len(s)-1], true
	}
	return "", false
}

func queryParam(o *model.Op, name string) *openapi3.Parameter {
	for _, p := range o.Op.Params {
		if p.In == openapi3.ParameterInQuery && p.Name == name {
			return p
		}
	}
	return nil
}

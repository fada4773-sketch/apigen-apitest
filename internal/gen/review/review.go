// Package review evaluates what apitest would report about a spec, the
// spec findings, cases it could not send and values the generator could
// not create, and proposes a fix for each: a defaults.json entry where one
// can solve it, otherwise what to do. The proposals are added to
// defaults.json for the user to review; "apply" takes them into the spec.
package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/apigen-apitest/internal/bind"
	"github.com/fada4773-sketch/apigen-apitest/internal/cases"
	"github.com/fada4773-sketch/apigen-apitest/internal/exec"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/apply"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/defaults"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/dict"
	"github.com/fada4773-sketch/apigen-apitest/internal/params"
	"github.com/fada4773-sketch/apigen-apitest/internal/spec"
)

// Actions: what solves a finding.
const (
	ActionDefault = "DEFAULT" // a defaults.json entry, proposed with its value
	ActionChoose  = "CHOOSE"  // a defaults.json entry whose value only the user knows
	ActionApply   = "APPLY"   // "apitest-gen apply" fixes it
	ActionSpec    = "SPEC"    // the spec has to be changed by hand
	ActionEdit    = "EDIT"    // an entry of the existing defaults is wrong
)

// Suggestion is the proposed fix for one finding.
type Suggestion struct {
	Action  string
	Finding string // the finding kind, e.g. "heuristic" or "NOT_BUILDABLE"
	Where   string // location in the spec or the dictionary
	Message string // the finding as apitest reports it
	Key     string // defaults key (DEFAULT, CHOOSE)
	Value   any    // proposed value (DEFAULT) or the current one (CHOOSE)
	Fix     string // what to do and why
}

// Result of a review.
type Result struct {
	Suggestions []Suggestion
}

// Count returns the number of suggestions with the action.
func (r *Result) Count(action string) int {
	n := 0
	for _, s := range r.Suggestions {
		if s.Action == action {
			n++
		}
	}
	return n
}

// Input of a review.
type Input struct {
	Spec       *spec.Spec
	Dict       *dict.Dict
	DictNotes  []dict.Note // from dict.Build
	Defaults   *defaults.Defaults
	GenericIDs []string
	// Apply is the result of "apply" run in memory on the same input; its
	// problems are reviewed too.
	Apply *apply.Result
}

type reviewer struct {
	in    Input
	res   *Result
	known map[string]bool // lower-case keys of the defaults with a value
	todo  map[string]bool // lower-case keys of the defaults with null
	seen  map[string]bool
	// chosen are parameters ("query.zone") that already have a CHOOSE
	// entry from a case that cannot be sent
	chosen map[string]bool
	// listBound are the bindings proposed from list GETs, by
	// "<operationId>.<param>", with their producer
	listBound map[string]*spec.Operation
	set       *bind.Set // the bindings in the spec, nil if they are invalid
}

// Run reviews a spec.
func Run(in Input) *Result {
	r := &reviewer{in: in, res: &Result{}, known: map[string]bool{}, todo: map[string]bool{}, seen: map[string]bool{}, chosen: map[string]bool{}, listBound: map[string]*spec.Operation{}}
	for _, k := range in.Defaults.Keys() {
		if in.Defaults.IsTodo(k) {
			r.todo[strings.ToLower(k)] = true
			continue
		}
		r.known[strings.ToLower(k)] = true
	}
	for _, k := range in.Defaults.Rejected {
		r.known[strings.ToLower(k)] = true // decided: not proposed again
	}
	r.specFindings()
	set, err := bind.Resolve(in.Spec)
	if err != nil {
		r.add(Suggestion{Action: ActionSpec, Finding: spec.FindingBinding, Where: "bindings", Message: err.Error(),
			Fix: "an x-apitest-bind in the spec is invalid; apitest stops before the first request. Fix or remove it in the spec"})
		set = nil
	} else {
		r.set = set
		r.bindings(set)
		r.listBindings(set)
	}
	r.auth()
	if set != nil {
		r.notBuildable(set)
		r.genericIDs(set)
	}
	r.dictNotes()
	r.applyProblems()
	return r.res
}

// add records a suggestion once. Keys the defaults already have, or that
// were rejected, are decided and dropped; a key with null is still open
// and only listed as a value to choose.
func (r *reviewer) add(s Suggestion) {
	k := strings.ToLower(s.Key)
	if s.Key != "" && (r.known[k] || (r.todo[k] && s.Action != ActionChoose)) {
		return
	}
	id := s.Action + "|" + s.Key + "|" + s.Where + "|" + s.Message
	if s.Key != "" {
		id = s.Action + "|" + s.Key
	}
	if r.seen[id] {
		return
	}
	r.seen[id] = true
	r.res.Suggestions = append(r.res.Suggestions, s)
}

func (r *reviewer) specFindings() {
	for _, f := range r.in.Spec.Findings {
		switch {
		case f.Kind == spec.FindingExampleSchema && strings.Contains(f.Where, ".examples."):
			r.add(Suggestion{Action: ActionSpec, Finding: f.Kind, Where: f.Where, Message: f.Message,
				Fix: "a curated named example violates its schema; apitest-gen never changes named examples. Fix the value in the spec, or remove the example"})
		case f.Kind == spec.FindingExampleSchema:
			r.add(Suggestion{Action: ActionApply, Finding: f.Kind, Where: f.Where, Message: f.Message,
				Fix: "run apitest-gen apply: it replaces examples that violate their schema"})
		case f.Kind == spec.FindingValidation && f.Where == "swagger":
			// information only: the conversion needs no fix
		case f.Kind == spec.FindingValidation:
			r.add(Suggestion{Action: ActionSpec, Finding: f.Kind, Where: f.Where, Message: f.Message,
				Fix: "rename one of the paths or merge the operations; apitest tests both, but a server can only route one"})
		}
	}
	if scheme, ok := r.in.Spec.AssumeBearer(); ok {
		r.add(Suggestion{Action: ActionSpec, Finding: spec.FindingAuth, Where: "security",
			Message: fmt.Sprintf("the spec declares no security; with Config.Token apitest sends it as a bearer token (scheme %q) to every operation", scheme),
			Fix: "declare it in the spec, so the authentication cases are generated:\n" +
				"components:\n  securitySchemes:\n    bearerAuth: { type: http, scheme: bearer }\nsecurity:\n  - bearerAuth: []"})
	}
}

var quoted = regexp.MustCompile(`"([^"]*)"`)

func (r *reviewer) bindings(set *bind.Set) {
	s := r.in.Spec
	for _, op := range s.Ops {
		for _, p := range op.Params {
			b := set.For(op, p)
			if b == nil || b.Kind != bind.Heuristic {
				continue
			}
			where := fmt.Sprintf("%s.parameters[%s]", op.Where, p.Name)
			msg := fmt.Sprintf("parameter %q is resolved heuristically from %s (%s)", p.Name, b.Producer.ID, b.Source)
			if !op.HasOperationID || !b.Producer.HasOperationID {
				r.add(Suggestion{Action: ActionSpec, Finding: spec.FindingHeuristic, Where: where, Message: msg,
					Fix: "give both operations an operationId; then apitest-gen review can propose the binding"})
				continue
			}
			v := map[string]any{"bind": b.Producer.ID}
			switch {
			case b.Source.HasConst:
				r.add(Suggestion{Action: ActionDefault, Finding: spec.FindingHeuristic, Where: where, Message: msg,
					Key: op.ID + "." + p.Name, Value: b.Source.Const, Fix: "the link sets a constant; keep it as a fixed value"})
				continue
			case b.Source.Header != "":
				v["header"] = b.Source.Header
			default:
				v["pointer"] = b.Source.Pointer
				if b.Source.FromRequest {
					v["request"] = true
				}
			}
			r.add(Suggestion{Action: ActionDefault, Finding: spec.FindingHeuristic, Where: where, Message: msg,
				Key: op.ID + "." + p.Name, Value: v,
				Fix: fmt.Sprintf("guessed by apitest from the names: %s returns %s. Keep it if that is the %s %s needs", b.Producer.ID, b.Source, p.Name, op.ID)})
		}
	}
	for _, f := range set.Findings {
		if f.Kind != spec.FindingBinding {
			continue
		}
		fix := "fix the link in the spec"
		if m := quoted.FindStringSubmatch(f.Message); m != nil {
			switch {
			case strings.Contains(f.Message, "unknown operation"):
				if c := closest(m[1], opIDs(s)); c != "" {
					fix = fmt.Sprintf("the link names an operation that does not exist; did you mean %q?", c)
				}
			case strings.Contains(f.Message, "does not exist in"):
				fix = "the link sets a parameter the target operation does not have; fix the name or remove it"
			}
		}
		r.add(Suggestion{Action: ActionSpec, Finding: f.Kind, Where: f.Where, Message: f.Message, Fix: fix})
	}
}

// listBindings proposes bindings from list GETs for path parameters that
// have no binding: /Book/id/{id} takes the Id of the first element of
// GET /Book, /Book/{Code}/Article/id/{id} the Id of the first Article of a
// list of Articles. This is how test data are found in an environment
// that already has them, and the only way where no POST creates them.
func (r *reviewer) listBindings(set *bind.Set) {
	s := r.in.Spec
	for _, op := range s.Ops {
		if !op.HasOperationID {
			continue
		}
		for _, p := range op.Params {
			where := fmt.Sprintf("%s.parameters[%s]", op.Where, p.Name)
			if p.In != openapi3.ParameterInPath || set.For(op, p) != nil || (r.decided(op, p) && !r.sharedConflict(where)) {
				continue
			}
			resource := resourceOf(op.Path, p.Name)
			if resource == "" {
				continue
			}
			producer, field := r.listProducer(op, p, resource, set)
			if producer == nil {
				continue
			}
			key := op.ID + "." + p.Name
			r.listBound[key] = producer
			r.add(Suggestion{Action: ActionDefault, Finding: "LIST_BINDING", Where: where,
				Message: fmt.Sprintf("parameter %q has no producer; %s lists %s with %s", p.Name, producer.ID, resource, field),
				Key:     key, Value: map[string]any{"bind": producer.ID, "pointer": "/0/" + field},
				Fix: fmt.Sprintf("takes the %s of the first element %s returns at run time, so the test uses data that exist", field, producer.ID)})
		}
	}
}

// sharedConflict reports whether apply could not write the default of a
// shared parameter at where; such a default has no effect.
func (r *reviewer) sharedConflict(where string) bool {
	if r.in.Apply == nil {
		return false
	}
	for _, n := range r.in.Apply.Notes {
		if n.Code == apply.CodeSharedParam && n.Where == where {
			return true
		}
	}
	return false
}

// decided reports whether the defaults already set a value for p of op.
func (r *reviewer) decided(op *spec.Operation, p *openapi3.Parameter) bool {
	keys := []string{op.ID + "." + p.Name, p.Name}
	if r.generic(p.Name) {
		keys = apply.GenericIDKeys(op, p)
	}
	return slices.ContainsFunc(keys, func(k string) bool { return r.known[strings.ToLower(k)] })
}

// listProducer finds a GET that returns a list of resource whose elements
// have the field p stands for. The one with the fewest path parameters
// wins; a producer that depends on op is never used.
func (r *reviewer) listProducer(op *spec.Operation, p *openapi3.Parameter, resource string, set *bind.Set) (*spec.Operation, string) {
	var best *spec.Operation
	bestField := ""
	for _, cand := range r.in.Spec.Ops {
		if cand == op || cand.Method != "GET" || !cand.HasOperationID {
			continue
		}
		last := lastLiteral(cand.Path)
		if last == "" || !sameResource(last, resource) {
			continue
		}
		props := listItemProps(cand)
		field := fieldFor(props, p.Name, resource, r.generic(p.Name))
		if field == "" || r.dependsOn(cand, op, set, 0) {
			continue
		}
		if best == nil || countParams(cand) < countParams(best) || (countParams(cand) == countParams(best) && cand.Path < best.Path) {
			best, bestField = cand, field
		}
	}
	return best, bestField
}

// dependsOn reports whether a takes a parameter from b, directly or through
// other bindings, existing or proposed.
func (r *reviewer) dependsOn(a, b *spec.Operation, set *bind.Set, depth int) bool {
	if a == b {
		return true
	}
	if depth > 20 {
		return false
	}
	for _, p := range a.Params {
		var producer *spec.Operation
		if bd := set.For(a, p); bd != nil {
			producer = bd.Producer
		} else {
			producer = r.listBound[a.ID+"."+p.Name]
		}
		if producer != nil && r.dependsOn(producer, b, set, depth+1) {
			return true
		}
	}
	return false
}

// resourceOf is the resource a path parameter identifies: the literal
// segment in front of it, skipping a literal "id": /Book/id/{id} → Book.
func resourceOf(path, param string) string {
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

func lastLiteral(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if last := segs[len(segs)-1]; last != "" && !strings.HasPrefix(last, "{") {
		return last
	}
	return ""
}

func sameResource(a, b string) bool { return strings.EqualFold(singular(a), singular(b)) }

func singular(w string) string {
	l := strings.ToLower(w)
	switch {
	case strings.HasSuffix(l, "ies"):
		return l[:len(l)-3] + "y"
	case strings.HasSuffix(l, "ses"), strings.HasSuffix(l, "xes"):
		return l[:len(l)-2]
	case strings.HasSuffix(l, "s") && !strings.HasSuffix(l, "ss"):
		return l[:len(l)-1]
	}
	return l
}

// listItemProps returns the properties of the elements of the lowest 2xx
// JSON response, if it is a list.
func listItemProps(op *spec.Operation) openapi3.Schemas {
	if op.Op.Responses == nil {
		return nil
	}
	var codes []string
	for code := range op.Op.Responses.Map() {
		if len(code) == 3 && code[0] == '2' {
			codes = append(codes, code)
		}
	}
	slices.Sort(codes)
	for _, code := range codes {
		resp := op.Op.Responses.Map()[code].Value
		if resp == nil {
			continue
		}
		for mt, m := range resp.Content {
			if !spec.IsJSON(mt) || m.Schema == nil || m.Schema.Value == nil {
				continue
			}
			if s := m.Schema.Value; s.Items != nil && s.Items.Value != nil {
				props, _ := dict.Properties(s.Items.Value)
				return props
			}
		}
		return nil
	}
	return nil
}

// fieldFor finds the field of a list element that a path parameter stands
// for: the same name, the name without the resource ("bookId" → "Id"), or
// "Id" for a generic id.
func fieldFor(props openapi3.Schemas, param, resource string, generic bool) string {
	names := []string{param}
	if generic {
		names = append(names, "id")
	}
	if len(param) > len(resource) && strings.EqualFold(param[:len(resource)], resource) {
		names = append(names, param[len(resource):])
	}
	for _, n := range names {
		for k := range props {
			if strings.EqualFold(k, n) {
				return k
			}
		}
	}
	return ""
}

func countParams(op *spec.Operation) int { return strings.Count(op.Path, "{") }

func (r *reviewer) auth() {
	for _, f := range cases.AuthFindings(r.in.Spec) {
		op := r.opAt(f.Where)
		if strings.HasSuffix(f.Where, ".x-apitest-forbidden") && op != nil && op.HasOperationID {
			r.add(Suggestion{Action: ActionDefault, Finding: f.Kind, Where: f.Where, Message: f.Message,
				Key: op.ID + ".x-apitest-forbidden", Value: false,
				Fix: "turns the forbidden case off; or document a 403 response in the spec to keep it"})
			continue
		}
		r.add(Suggestion{Action: ActionSpec, Finding: f.Kind, Where: f.Where, Message: f.Message,
			Fix: "document the response the API sends without a valid token, e.g.\n\"401\":\n  description: Unauthorized\n" +
				"or leave these cases out with Config.SkipAuthCases"})
	}
}

func (r *reviewer) opAt(where string) *spec.Operation {
	for _, op := range r.in.Spec.Ops {
		if strings.HasPrefix(where, op.Where+".") {
			return op
		}
	}
	return nil
}

// notBuildable proposes values for the cases apitest could not send.
func (r *reviewer) notBuildable(set *bind.Set) {
	s := r.in.Spec
	all, err := cases.Build(s, cases.Options{})
	if err != nil {
		r.add(Suggestion{Action: ActionSpec, Finding: "CASES", Where: "cases", Message: err.Error(), Fix: "fix the spec; apitest cannot build any case"})
		return
	}
	var schemes openapi3.SecuritySchemes
	if s.Doc.Components != nil {
		schemes = s.Doc.Components.SecuritySchemes
	}
	fixed := r.in.Defaults.Params()
	for _, c := range all {
		if c.Skip != "" {
			continue
		}
		in := params.Inputs{Fixed: fixed, OpID: c.Op.ID, CaseName: c.ParamSource(),
			Binding: func(p *openapi3.Parameter) (any, bool) {
				return "bound", set.For(c.Op, p) != nil || r.listBound[c.Op.ID+"."+p.Name] != nil
			}}
		_, err := exec.Prepare(c, exec.Input{Base: "http://localhost", Params: in,
			Auth: exec.ResolveAuth(c.Op.Security, schemes), Token: "token"})
		nb := (*exec.NotBuildableError)(nil)
		if !errors.As(err, &nb) {
			continue
		}
		missing := false
		for _, p := range c.Op.Params {
			if _, ok := params.Resolve(p, in); ok || (!p.Required && p.In != openapi3.ParameterInPath) {
				continue
			}
			missing = true
			if p.In == openapi3.ParameterInPath && r.generic(p.Name) {
				continue // proposed by genericIDs
			}
			r.missingParam(c.Op, p)
		}
		if missing {
			continue
		}
		action, fix, where := ActionSpec, "fix the spec: "+nb.Reason, c.Name
		if strings.HasPrefix(nb.Reason, "required body without example") {
			// one entry per operation, not per case
			action, where = ActionApply, c.Op.Where+".requestBody"
			fix = "run apitest-gen apply: it writes the body example; a field without value is listed as NO_VALUE or PATTERN_PENDING"
		}
		r.add(Suggestion{Action: action, Finding: "NOT_BUILDABLE", Where: where, Message: nb.Reason, Fix: fix})
	}
}

func (r *reviewer) missingParam(op *spec.Operation, p *openapi3.Parameter) {
	where := fmt.Sprintf("%s.parameters[%s]", op.Where, p.Name)
	msg := fmt.Sprintf("no value for required parameter %q (%s)", p.Name, p.In)
	if n := r.in.Dict.Parameters[p.In+"."+p.Name]; n != nil && n.Value != nil {
		r.add(Suggestion{Action: ActionApply, Finding: "NOT_BUILDABLE", Where: where, Message: msg,
			Fix: fmt.Sprintf("run apitest-gen apply: it writes the dictionary value %s as example", compact(n.Value))})
		return
	}
	r.chosen[p.In+"."+p.Name] = true
	key := p.Name
	if op.HasOperationID {
		key = op.ID + "." + p.Name
	}
	r.add(Suggestion{Action: ActionChoose, Finding: "NOT_BUILDABLE", Where: where, Message: msg, Key: key,
		Fix: "set a value that exists in the test environment" + constraints(p.Schema)})
}

// genericIDs proposes real ids for generic path parameters that have no
// binding and no default; apply would invent one.
func (r *reviewer) genericIDs(set *bind.Set) {
	for _, op := range r.in.Spec.Ops {
		for _, p := range op.Params {
			if p.In != openapi3.ParameterInPath || !r.generic(p.Name) {
				continue
			}
			if set.For(op, p) != nil || r.listBound[op.ID+"."+p.Name] != nil {
				continue // apitest takes the id from a producer at run time
			}
			keys := apply.GenericIDKeys(op, p)
			if slices.ContainsFunc(keys, func(k string) bool { return r.known[strings.ToLower(k)] }) {
				continue
			}
			current := r.in.Dict.Paths[op.Path]
			fix := "no producer creates this resource during the test; set the id of one that exists in the test environment"
			if current != nil {
				fix += fmt.Sprintf(" (generated so far: %s)", compact(current))
			}
			r.add(Suggestion{Action: ActionChoose, Finding: "GENERIC_ID", Where: fmt.Sprintf("%s.parameters[%s]", op.Where, p.Name),
				Message: fmt.Sprintf("{%s} has no binding and no default", p.Name), Key: op.Path, Value: current, Fix: fix})
		}
	}
}

func (r *reviewer) generic(name string) bool {
	return slices.ContainsFunc(r.in.GenericIDs, func(g string) bool { return strings.EqualFold(g, name) })
}

// dictNotes proposes defaults for values the generator could not create.
func (r *reviewer) dictNotes() {
	for _, n := range r.in.DictNotes {
		switch n.Code {
		case dict.CodeNoValue, dict.CodePatternPending, dict.CodeTypeConflict:
			if r.chosen[strings.TrimPrefix(n.Where, "parameters.")] {
				continue // proposed with the operation already
			}
			r.add(Suggestion{Action: ActionChoose, Finding: n.Code, Where: n.Where, Message: n.Message, Key: dictKey(n.Where),
				Fix: "the generator cannot create a value that fits; set one by hand"})
		case dict.CodeValueInvalid:
			r.add(Suggestion{Action: ActionApply, Finding: n.Code, Where: n.Where, Message: n.Message,
				Fix: "run apitest-gen apply -repair to regenerate it, or correct the value in the dictionary"})
		}
	}
}

var noValue = regexp.MustCompile(`(\S+) has no value`)

// applyProblems proposes fixes for what apply reports.
func (r *reviewer) applyProblems() {
	if r.in.Apply == nil {
		return
	}
	for _, f := range r.in.Apply.Fatal {
		key := ""
		if m := quoted.FindStringSubmatch(f); m != nil {
			key = m[1]
		}
		r.edit(Suggestion{Action: ActionEdit, Finding: strings.Fields(f)[0], Where: "defaults", Message: f, Key: key,
			Fix: "correct or remove this entry; apply writes nothing while it is wrong"})
	}
	for _, n := range r.in.Apply.Notes {
		switch n.Code {
		case apply.CodeDefaultUnused:
			key := ""
			if m := quoted.FindStringSubmatch(n.Message); m != nil {
				key = m[1]
			}
			r.edit(Suggestion{Action: ActionEdit, Finding: n.Code, Where: n.Where, Message: n.Message, Key: key,
				Fix: "fix the spelling, use the key named in the message, or remove the entry"})
		case apply.CodeIncomplete:
			if strings.Contains(n.Where, ".parameters[") {
				continue // a case that cannot be sent, proposed with the operation
			}
			if m := noValue.FindStringSubmatch(n.Message); m != nil && !strings.Contains(m[1], "(") {
				r.add(Suggestion{Action: ActionChoose, Finding: n.Code, Where: n.Where, Message: n.Message, Key: m[1],
					Fix: "no value fits this field; set one"})
				continue
			}
			r.add(Suggestion{Action: ActionSpec, Finding: n.Code, Where: n.Where, Message: n.Message,
				Fix: "the schema allows no example (a cycle or a contradiction); simplify it in the spec"})
		case apply.CodeSharedParam:
			if r.boundAt(n.Where) {
				continue // a binding from a list GET is proposed for it
			}
			r.add(Suggestion{Action: ActionSpec, Finding: n.Code, Where: n.Where, Message: n.Message,
				Fix: "define the parameter in the operation instead of a shared component, then the operation can have its own value"})
		case apply.CodeBindSkipped, apply.CodeExternalRef:
			r.add(Suggestion{Action: ActionSpec, Finding: n.Code, Where: n.Where, Message: n.Message,
				Fix: "change the spec as the message says"})
		}
	}
}

// boundAt reports whether the parameter at where
// ("paths./x/{id}.get.parameters[id]") is bound, in the spec or by a
// proposed list binding.
func (r *reviewer) boundAt(where string) bool {
	for _, op := range r.in.Spec.Ops {
		for _, p := range op.Params {
			if fmt.Sprintf("%s.parameters[%s]", op.Where, p.Name) != where {
				continue
			}
			if r.listBound[op.ID+"."+p.Name] != nil || (r.set != nil && r.set.For(op, p) != nil) {
				return true
			}
		}
	}
	return false
}

// edit records a fix for an existing defaults entry, which add would drop.
func (r *reviewer) edit(s Suggestion) {
	id := "edit|" + s.Key + "|" + s.Message
	if r.seen[id] {
		return
	}
	r.seen[id] = true
	r.res.Suggestions = append(r.res.Suggestions, s)
}

// dictKey turns a dictionary path into a defaults key:
// schemas.Garden.Sealed → Garden.Sealed, parameters.path.code → code.
func dictKey(where string) string {
	parts := strings.Split(strings.ReplaceAll(where, "[]", ""), ".")
	switch {
	case len(parts) >= 3 && parts[0] == "schemas":
		return parts[1] + "." + parts[len(parts)-1]
	case len(parts) == 2 && parts[0] == "schemas":
		return apply.DTOKey(parts[1])
	case len(parts) >= 3 && parts[0] == "parameters":
		return parts[len(parts)-1]
	}
	return where
}

func constraints(ref *openapi3.SchemaRef) string {
	if ref == nil || ref.Value == nil {
		return ""
	}
	s := ref.Value
	var c []string
	if t := s.Type; t != nil && len(*t) > 0 {
		c = append(c, "type "+strings.Join(*t, "|"))
	}
	if s.Format != "" {
		c = append(c, "format "+s.Format)
	}
	if s.Pattern != "" {
		c = append(c, "pattern "+s.Pattern)
	}
	if len(s.Enum) > 0 {
		c = append(c, "one of "+compact(s.Enum))
	}
	if len(c) == 0 {
		return ""
	}
	return " (" + strings.Join(c, ", ") + ")"
}

func opIDs(s *spec.Spec) []string {
	var out []string
	for _, op := range s.Ops {
		if op.HasOperationID {
			out = append(out, op.ID)
		}
	}
	return out
}

// closest returns the candidate nearest to name: equal ignoring case, or
// at most a third of its letters different.
func closest(name string, candidates []string) string {
	best, bestD := "", len(name)/3+1
	for _, c := range candidates {
		if strings.EqualFold(c, name) {
			return c
		}
		if d := distance(strings.ToLower(name), strings.ToLower(c)); d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

func compact(v any) string { return marshal(v, "") }

// marshal renders JSON without HTML escaping, indented after prefix.
func marshal(v any, prefix string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if prefix != "" {
		enc.SetIndent(prefix, "  ")
	}
	_ = enc.Encode(v)
	return strings.TrimRight(b.String(), "\n")
}

// Changes returns the entries review adds to defaults.json: proposed
// values and bindings, and for values only the user knows the value used
// so far, to be checked. Nothing else is written; the reasons are printed.
func (r *Result) Changes() []defaults.Pair {
	var add []defaults.Pair
	for _, s := range r.Suggestions {
		if s.Key == "" || s.Value == nil {
			continue
		}
		if s.Action == ActionDefault || s.Action == ActionChoose {
			add = append(add, defaults.Pair{Key: s.Key, Value: s.Value})
		}
	}
	return add
}

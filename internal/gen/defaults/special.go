package defaults

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Keys of the special entries, which configure the generator instead of
// setting a value.
const (
	SnapshotKey = "$snapshot" // where the records of a resource are fetched
	ModelKey    = "$model"    // corrections of the resource model
	ApitestKey  = "$apitest"  // the apitest Config that orders the cases
)

// Snapshot tells where the records of a resource come from: the operation
// whose response provides them and how many elements of a list are taken.
type Snapshot struct {
	// From is the request, "/DefaultBook/Level/A1?isbn=978", or the
	// operationId of a GET of the resource.
	From  string `json:"from"`
	Count int    `json:"count"` // records taken from the list; 0 means 1
	// Comment is free text; review writes the path template there.
	Comment string `json:"$comment,omitempty"`
}

// Records is the number of records, at least 1.
func (s Snapshot) Records() int { return max(s.Count, 1) }

// ModelFix corrects the detected model of one resource.
type ModelFix struct {
	// Schemas are the DTOs of the resource, the one its GETs return first.
	Schemas []string `json:"schemas,omitempty"`
	// Keys are fields that identify a record, in addition to the ones the
	// paths use; updates never change them.
	Keys []string `json:"keys,omitempty"`
}

// Run holds the fields of apitest's Config that decide which cases run and
// in which order. They must be the same as in the test, because the
// examples follow the state of the data at the position of each case.
type Run struct {
	MethodOrder  []string `json:"MethodOrder,omitempty"`
	DeleteLast   bool     `json:"DeleteLast,omitempty"`
	Tags         []string `json:"Tags,omitempty"`
	IncludeOps   []string `json:"IncludeOps,omitempty"`
	ExcludeOps   []string `json:"ExcludeOps,omitempty"`
	IgnoreFields []string `json:"IgnoreFields,omitempty"`
}

// special parses the special keys; handled reports whether key is one.
func (d *Defaults) special(key string, raw json.RawMessage) (handled bool, err error) {
	switch key {
	case SnapshotKey:
		m := map[string]Snapshot{}
		if err := strict(raw, &m); err != nil {
			return true, fmt.Errorf(`%q must map a resource to {"from": "<operationId>", "count": 1}: %w`, key, err)
		}
		for name, s := range m {
			if s.Count < 0 {
				return true, fmt.Errorf("%q: count of %q must be 1 or more", key, name)
			}
		}
		d.Snapshot = m
	case ModelKey:
		m := map[string]ModelFix{}
		if err := strict(raw, &m); err != nil {
			return true, fmt.Errorf(`%q must map a resource to {"schemas": [...], "keys": [...]}: %w`, key, err)
		}
		d.Model = m
	case ApitestKey:
		r := &Run{}
		if err := strict(raw, r); err != nil {
			return true, fmt.Errorf(`%q takes the apitest Config fields MethodOrder, DeleteLast, Tags, IncludeOps, ExcludeOps and IgnoreFields: %w`, key, err)
		}
		if err := checkMethodOrder(r.MethodOrder); err != nil {
			return true, fmt.Errorf("%q: %w", key, err)
		}
		d.Run = r
	default:
		return false, nil
	}
	return true, nil
}

// strict decodes raw into v and rejects unknown fields, so a typo does not
// silently change the order of the cases.
func strict(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// checkMethodOrder applies the rules of apitest's Config.MethodOrder.
func checkMethodOrder(order []string) error {
	seen := map[string]bool{}
	for i, m := range order {
		up := strings.ToUpper(strings.TrimSpace(m))
		switch {
		case seen[up]:
			return fmt.Errorf("MethodOrder lists %s twice", up)
		case up == "DELETE" && i != len(order)-1:
			return fmt.Errorf("MethodOrder: DELETE must be the last entry")
		case !slices.Contains([]string{"POST", "GET", "PUT", "PATCH", "DELETE"}, up):
			return fmt.Errorf("MethodOrder: unknown method %q (valid: POST, GET, PUT, PATCH, DELETE last)", m)
		}
		seen[up] = true
	}
	return nil
}

// SnapshotFor returns the "$snapshot" entry of a resource, ignoring case.
func (d *Defaults) SnapshotFor(resource string) (Snapshot, bool) {
	for name, s := range d.Snapshot {
		if strings.EqualFold(name, resource) {
			return s, true
		}
	}
	return Snapshot{}, false
}

// RunConfig returns "$apitest", or the apitest defaults if there is none.
func (d *Defaults) RunConfig() Run {
	if d.Run == nil {
		return Run{}
	}
	return *d.Run
}

// mergeSpecial takes the special entries of a later file: a resource
// replaces the one with the same name, "$apitest" replaces the whole entry.
func (d *Defaults) mergeSpecial(other *Defaults) {
	for name, s := range other.Snapshot {
		if d.Snapshot == nil {
			d.Snapshot = map[string]Snapshot{}
		}
		d.Snapshot[name] = s
	}
	for name, f := range other.Model {
		if d.Model == nil {
			d.Model = map[string]ModelFix{}
		}
		d.Model[name] = f
	}
	if other.Run != nil {
		d.Run = other.Run
	}
}

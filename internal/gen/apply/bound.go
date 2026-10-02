package apply

import (
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/apigen-apitest/internal/bind"
	"github.com/fada4773-sketch/apigen-apitest/internal/gen/yamldoc"
	"github.com/fada4773-sketch/apigen-apitest/internal/spec"
)

// boundValue returns the value a parameter bound in defaults.json gets
// from its producer's example: for {"bind": "GetBooks", "pointer": "/0/Id"}
// the Id of the first element of the GetBooks response example. It is nil
// for a header binding or if the producer has no such value.
func (a *applier) boundValue(op *spec.Operation, p *openapi3.Parameter) any {
	if !op.HasOperationID {
		return nil
	}
	e := a.defs.Binding(op.ID, p.Name)
	if e == nil || e.Bind.Header != "" {
		return nil
	}
	producer := a.s.Op(e.Bind.From)
	if producer == nil {
		return nil
	}
	ex := a.producerExample(producer, e.Bind.Request)
	if ex == nil {
		return nil
	}
	v, ok := bind.Pointer(ex, e.Bind.Pointer)
	if !ok {
		return nil
	}
	return v
}

// fromProducer is the example of a bound parameter: the producer's value,
// adjusted to the parameter's type; nil if there is none or it does not
// fit. A fixed value in defaults.json is looked up before and wins.
func (a *applier) fromProducer(op *spec.Operation, p *openapi3.Parameter) any {
	v := a.boundValue(op, p)
	if v == nil {
		return nil
	}
	v = coerce(v, p.Schema.Value)
	if !a.valid(p.Schema.Value, v, spec.ModePlain) {
		return nil
	}
	return v
}

// producerExample is the example of the producer's request body or lowest
// 2xx JSON response: as it is in the spec, or else as apply would build it.
func (a *applier) producerExample(producer *spec.Operation, request bool) any {
	var contentY *yaml.Node
	var content openapi3.Content
	if request {
		if rb := producer.Op.RequestBody; rb != nil && rb.Value != nil {
			content = rb.Value.Content
			if rbY := yamldoc.Path(a.doc.Root, "paths", producer.Path, strings.ToLower(producer.Method), "requestBody"); rbY != nil {
				if t, err := a.doc.Resolve(rbY); err == nil {
					contentY = yamldoc.Get(t, "content")
				}
			}
		}
	} else if respY, code := a.successResponse(producer); respY != nil {
		if t, err := a.doc.Resolve(respY); err == nil {
			contentY = yamldoc.Get(t, "content")
		}
		if r := producer.Op.Responses.Value(code); r != nil && r.Value != nil {
			content = r.Value.Content
		}
	}
	for mt, m := range content {
		if !spec.IsJSON(mt) || m.Schema == nil {
			continue
		}
		if exY := yamldoc.Get(yamldoc.Get(contentY, mt), "example"); exY != nil {
			if v, err := yamldoc.Decode(exY); err == nil && a.valid(m.Schema.Value, v, spec.ModePlain) {
				return v
			}
		}
		mode := spec.ModeResponse
		if request {
			mode = spec.ModeRequest
		}
		if v, ok, _ := a.build(m.Schema, "", nil, "", opName(producer), mode, 0, nil); ok {
			return v
		}
	}
	return nil
}

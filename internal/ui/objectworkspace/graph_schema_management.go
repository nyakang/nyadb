package objectworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphschema"
)

type GraphSchemaForm struct{ Action, Name, Type, EntityType, LabelsJSON, PropertiesJSON, OptionsJSON string }
type graphSchemaDocument struct {
	lease                                                                                                     contract.GraphSchemaManagementLease
	generation, revision                                                                                      uint64
	form, baseline, preparedForm                                                                              GraphSchemaForm
	observation                                                                                               contract.GraphSchemaObservation
	catalog                                                                                                   contract.GraphSchemaCatalog
	plan                                                                                                      contract.GraphSchemaPreparation
	preview                                                                                                   contract.GraphSchemaPreview
	last                                                                                                      contract.GraphSchemaManagementResult
	results                                                                                                   <-chan graphSchemaReply
	tab                                                                                                       int
	confirmation                                                                                              string
	showingPreview, reviewed, acceptDrop, pending, lost, abandoned, guard, leaveAcknowledged, cancelRequested bool
	next                                                                                                      func(ui.Services)
}
type graphSchemaReply struct {
	request, generation uint64
	operation           string
	observation         contract.GraphSchemaObservation
	catalog             contract.GraphSchemaCatalog
	result              contract.GraphSchemaManagementResult
	err                 error
}
type GraphSchemaEvent struct {
	Object     contract.ObjectRef
	Generation uint64
	Result     contract.GraphSchemaManagementResult
}

func graphSchemaObject(o contract.ObjectRef) bool {
	return o.ProfileID > 0 && o.Catalog != "" && o.Schema == "" && o.Kind == "graph_schema" && o.Name == "graph_schema"
}
func (s *State) GraphSchemaManagementSupported(o contract.ObjectRef) bool {
	if !graphSchemaObject(o) || s.deps.GraphSchemaManagementFor == nil {
		return false
	}
	p, e := s.deps.GraphSchemaManagementFor(o.ProfileID)
	return e == nil && p != nil && p.SupportsGraphSchemaManagement(o)
}
func (s *State) OpenGraphSchemaManagement(id uint64, o contract.ObjectRef) *Document {
	if id == 0 || !s.GraphSchemaManagementSupported(o) {
		return nil
	}
	if old := s.documents[id]; old != nil {
		s.Close(old)
	}
	f := GraphSchemaForm{Action: "create_index", Type: "RANGE", EntityType: "NODE", LabelsJSON: "[]", PropertiesJSON: "[]"}
	database := o.Catalog
	if s.deps.NamespaceLabel != nil {
		database = s.deps.NamespaceLabel(o.Namespace())
	}
	d := &Document{id: id, profileID: o.ProfileID, object: o, database: database, table: o.Name, objectKind: "图索引与约束管理", schemaManagement: &graphSchemaDocument{form: f, baseline: f}}
	s.documents[id] = d
	return d
}
func (d *Document) GraphSchemaManagementMode() bool {
	return d != nil && !d.closed && d.schemaManagement != nil
}
func (x *graphSchemaDocument) dirty() bool { return x != nil && !x.abandoned && x.form != x.baseline }
func (s *State) schemaReady(d *Document) bool {
	if !d.GraphSchemaManagementMode() || d.loading || d.schemaManagement.lost || d.schemaManagement.abandoned || s.session().ProfileID != d.profileID {
		return false
	}
	if s.deps.DocumentActive != nil && !s.deps.DocumentActive(d.id) {
		return false
	}
	w := s.sessionFor(d.profileID)
	return w.Connected && w.ProfileID == d.profileID && (d.schemaManagement.lease == nil || w.Generation == d.schemaManagement.generation)
}
func schemaValidateJSON(raw string) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var read func(int) error
	read = func(depth int) error {
		if depth > 16 {
			return graphschema.ErrBounds
		}
		token, e := dec.Token()
		if e != nil {
			return e
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		seen := map[string]bool{}
		count := 0
		for dec.More() {
			count++
			if count > 1000 {
				return graphschema.ErrBounds
			}
			if delim == '{' {
				key, e := dec.Token()
				if e != nil {
					return e
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return graphschema.ErrInvalid
				}
				seen[name] = true
			}
			if e := read(depth + 1); e != nil {
				return e
			}
		}
		_, e = dec.Token()
		return e
	}
	if e := read(0); e != nil {
		return e
	}
	if _, e := dec.Token(); e != io.EOF {
		return graphschema.ErrInvalid
	}
	return nil
}
func schemaDecode(raw string, target any) error {
	if e := schemaValidateJSON(raw); e != nil {
		return e
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if e := dec.Decode(target); e != nil {
		return e
	}
	var extra any
	if e := dec.Decode(&extra); e != io.EOF {
		return graphschema.ErrInvalid
	}
	return nil
}
func schemaJSONValue(v any, depth int) (contract.GraphValue, error) {
	if depth > 16 {
		return contract.GraphValue{}, graphschema.ErrBounds
	}
	switch v := v.(type) {
	case nil:
		return contract.GraphValue{Kind: "null"}, nil
	case string:
		return contract.GraphValue{Kind: "string", Text: v}, nil
	case bool:
		return contract.GraphValue{Kind: "boolean", Text: strconv.FormatBool(v)}, nil
	case json.Number:
		if _, e := v.Int64(); e != nil {
			return contract.GraphValue{}, graphschema.ErrInvalid
		}
		return contract.GraphValue{Kind: "integer", Text: v.String()}, nil
	case map[string]any:
		if len(v) > 1000 {
			return contract.GraphValue{}, graphschema.ErrBounds
		}
		out := contract.GraphValue{Kind: "map"}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child, e := schemaJSONValue(v[k], depth+1)
			if e != nil {
				return out, e
			}
			out.Properties = append(out.Properties, contract.GraphProperty{Name: k, Value: child})
		}
		return out, nil
	case []any:
		if len(v) > 1000 {
			return contract.GraphValue{}, graphschema.ErrBounds
		}
		out := contract.GraphValue{Kind: "list"}
		for _, item := range v {
			child, e := schemaJSONValue(item, depth+1)
			if e != nil {
				return out, e
			}
			out.Values = append(out.Values, child)
		}
		return out, nil
	}
	return contract.GraphValue{}, graphschema.ErrInvalid
}
func graphSchemaDraft(f GraphSchemaForm) (contract.GraphSchemaDraft, error) {
	d := contract.GraphSchemaDraft{Action: f.Action, Name: f.Name}
	if len(f.Name)+len(f.LabelsJSON)+len(f.PropertiesJSON)+len(f.OptionsJSON) > 1<<20 {
		return d, graphschema.ErrBounds
	}
	if f.Action == "drop_index" || f.Action == "drop_constraint" {
		return d, graphschema.ValidateDraftBounds(d)
	}
	d.Type, d.EntityType = f.Type, f.EntityType
	if e := schemaDecode(f.LabelsJSON, &d.LabelsOrTypes); e != nil {
		return d, e
	}
	if e := schemaDecode(f.PropertiesJSON, &d.Properties); e != nil {
		return d, e
	}
	if strings.TrimSpace(f.OptionsJSON) != "" {
		var value any
		if e := schemaDecode(f.OptionsJSON, &value); e != nil {
			return d, e
		}
		var e error
		d.Options, e = schemaJSONValue(value, 0)
		if e != nil {
			return d, e
		}
	}
	return d, graphschema.ValidateDraftBounds(d)
}
func (s *State) GraphSchemaForm(d *Document) GraphSchemaForm {
	if !d.GraphSchemaManagementMode() {
		return GraphSchemaForm{}
	}
	return d.schemaManagement.form
}
func (s *State) SetGraphSchemaForm(d *Document, f GraphSchemaForm) bool {
	if !s.schemaReady(d) || d.schemaManagement.pending || d.schemaManagement.showingPreview {
		return false
	}
	x := d.schemaManagement
	if x.form != f {
		x.form = f
		x.plan = nil
		x.revision++
		x.leaveAcknowledged = false
		x.confirmation = ""
		x.reviewed, x.acceptDrop = false, false
	}
	return true
}
func (s *State) ReadGraphSchema(d *Document, invalidate func()) {
	if !s.schemaReady(d) || d.schemaManagement.pending || d.schemaManagement.showingPreview {
		return
	}
	x := d.schemaManagement
	if x.lease == nil {
		p, e := s.deps.GraphSchemaManagementFor(d.profileID)
		if e != nil {
			d.err = e.Error()
			return
		}
		l, e := p.ReserveGraphSchemaManagement(d.object, d.id)
		if e != nil {
			d.err = e.Error()
			return
		}
		if l.Object() != d.object || l.Generation() != s.sessionFor(d.profileID).Generation {
			l.Close()
			d.err = contract.ErrRevoked.Error()
			return
		}
		x.lease, x.generation = l, l.Generation()
	}
	x.observation, x.plan = nil, nil
	x.revision++
	s.runGraphSchema(d, invalidate, "read", func(ctx context.Context, l contract.GraphSchemaManagementLease) graphSchemaReply {
		o, e := l.Read(ctx)
		var c contract.GraphSchemaCatalog
		if o != nil {
			c = o.Snapshot().Clone()
		}
		return graphSchemaReply{observation: o, catalog: c, err: e}
	})
}
func (s *State) PrepareGraphSchema(d *Document) error {
	if !s.schemaReady(d) || d.schemaManagement.pending {
		return contract.ErrUnsupported
	}
	x := d.schemaManagement
	if x.observation == nil {
		return contract.ErrGraphSchemaChanged
	}
	draft, e := graphSchemaDraft(x.form)
	if e == nil {
		x.plan, e = x.lease.Prepare(x.observation, draft.Clone())
	}
	if e != nil {
		x.plan = nil
		d.err = e.Error()
		return e
	}
	x.preview = x.plan.Preview().Clone()
	x.preparedForm = x.form
	x.showingPreview = true
	x.confirmation = ""
	x.reviewed, x.acceptDrop, x.leaveAcknowledged = false, false, false
	x.revision++
	d.err = ""
	return nil
}
func (s *State) CanSubmitGraphSchema(d *Document) bool {
	if !s.schemaReady(d) {
		return false
	}
	x := d.schemaManagement
	_, truncated := taskPreview(graphSchemaJSON(x.preview))
	return !x.pending && x.showingPreview && x.plan != nil && x.form == x.preparedForm && (!truncated || x.reviewed) && (!x.preview.NonAtomicDrop || x.acceptDrop && x.confirmation == x.preview.Draft.Name)
}
func (s *State) SubmitGraphSchema(d *Document, invalidate func()) {
	if !s.CanSubmitGraphSchema(d) {
		return
	}
	x := d.schemaManagement
	plan := x.plan
	confirmation := contract.GraphSchemaConfirmation{Name: x.confirmation, AcceptNonAtomicDrop: x.acceptDrop}
	x.pending = true
	x.showingPreview = false
	x.plan, x.observation = nil, nil
	s.runGraphSchema(d, invalidate, "submit", func(ctx context.Context, l contract.GraphSchemaManagementLease) graphSchemaReply {
		r, e := l.Submit(ctx, plan, confirmation)
		return graphSchemaReply{result: r.Clone(), err: e}
	})
}
func (s *State) VerifyGraphSchema(d *Document, invalidate func()) {
	if !s.schemaReady(d) || !d.schemaManagement.pending || d.schemaManagement.lease == nil {
		return
	}
	s.runGraphSchema(d, invalidate, "verify", func(ctx context.Context, l contract.GraphSchemaManagementLease) graphSchemaReply {
		r, e := l.Verify(ctx)
		return graphSchemaReply{result: r.Clone(), err: e}
	})
}
func (s *State) runGraphSchema(d *Document, invalidate func(), operation string, run func(context.Context, contract.GraphSchemaManagementLease) graphSchemaReply) {
	x := d.schemaManagement
	l, generation := x.lease, x.generation
	d.request++
	request := d.request
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel, d.loading, d.err = cancel, true, ""
	x.cancelRequested, x.leaveAcknowledged = false, false
	out := make(chan graphSchemaReply, 1)
	x.results = out
	go func() {
		r := run(ctx, l)
		r.request, r.generation, r.operation = request, generation, operation
		out <- r
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) CancelGraphSchema(d *Document) {
	if !d.GraphSchemaManagementMode() || !d.loading || d.schemaManagement.cancelRequested {
		return
	}
	d.schemaManagement.cancelRequested = true
	if d.cancel != nil {
		d.cancel()
	}
}
func (s *State) consumeGraphSchema(services ui.Services) {
	active := s.session()
	for _, d := range s.documents {
		if !d.GraphSchemaManagementMode() {
			continue
		}
		x := d.schemaManagement
		orphaned := active.ProfileID == 0 && s.deps.DocumentActive != nil && s.deps.DocumentActive(d.id)
		if active.ProfileID != d.profileID && !orphaned {
			continue
		}
		current := s.sessionFor(d.profileID)
		if x.lease != nil && (!current.Connected || current.Generation != x.generation) {
			x.lost = true
			if !d.loading {
				d.err = "原图结构管理会话已失效，草稿与回执保留"
			}
		}
		if x.results == nil {
			continue
		}
		select {
		case r := <-x.results:
			if r.request != d.request {
				continue
			}
			x.results = nil
			d.loading = false
			x.cancelRequested = false
			if d.cancel != nil {
				d.cancel()
				d.cancel = nil
			}
			if r.operation == "submit" || r.operation == "verify" {
				if r.operation == "submit" || r.result.Preview.Request.Query != "" {
					x.last = r.result.Clone()
				}
				x.tab = 2
				x.pending = x.last.State != "not_submitted" && x.last.State != "verified"
			}
			if !current.Connected || r.generation != current.Generation {
				x.lost = true
				d.err = "原图结构管理会话已失效，草稿与回执保留"
				continue
			}
			if errors.Is(r.err, contract.ErrRevoked) {
				x.lost = true
			}
			d.err = ""
			if r.err != nil {
				d.err = r.err.Error()
			}
			if r.operation == "verify" && r.err == nil && r.result.HasVerification {
				x.catalog = r.result.Verification.Clone()
			}
			if r.operation == "read" && r.err == nil {
				x.catalog = r.catalog.Clone()
				x.observation = r.observation
				x.tab = 1
			}
			if (r.operation == "submit" || r.operation == "verify") && x.last.Receipt.Attempted {
				if s.deps.GraphSchemaChanged != nil {
					s.deps.GraphSchemaChanged(services, GraphSchemaEvent{Object: d.object, Generation: r.generation, Result: x.last.Clone()})
				}
				if r.err == nil && !x.lost && x.last.State == "verified" {
					x.baseline = x.preparedForm
				}
			}
		default:
		}
	}
}
func (s *State) GraphSchemaResult(d *Document) contract.GraphSchemaManagementResult {
	if !d.GraphSchemaManagementMode() {
		return contract.GraphSchemaManagementResult{}
	}
	return d.schemaManagement.last.Clone()
}
func graphSchemaJSON(v any) string { raw, _ := json.MarshalIndent(v, "", "  "); return string(raw) }
func graphSchemaEvidence(x *graphSchemaDocument) string {
	return graphSchemaJSON(struct {
		Catalog contract.GraphSchemaCatalog
		Preview contract.GraphSchemaPreview
		Result  contract.GraphSchemaManagementResult
	}{x.catalog.Clone(), x.preview.Clone(), x.last.Clone()})
}
func (s *State) guardGraphSchemaLeave(d *Document, next func(ui.Services)) bool {
	if !d.GraphSchemaManagementMode() {
		return false
	}
	x := d.schemaManagement
	if !d.loading && (x.leaveAcknowledged || !x.dirty() && !x.pending && !x.showingPreview) {
		return false
	}
	if s.deps.ActivateDesign != nil {
		s.deps.ActivateDesign(d)
	}
	x.guard, x.next = true, next
	return true
}
func (s *State) abandonGraphSchema(d *Document) {
	if !d.GraphSchemaManagementMode() || d.loading {
		return
	}
	x := d.schemaManagement
	if x.lease != nil {
		x.lease.Close()
		x.lease = nil
	}
	x.plan, x.observation = nil, nil
	x.showingPreview, x.abandoned, x.leaveAcknowledged = false, true, true
	x.revision++
}
func (s *State) closeGraphSchema(d *Document) {
	if d.schemaManagement == nil {
		return
	}
	x := d.schemaManagement
	d.request++
	if d.cancel != nil {
		d.cancel()
	}
	if x.lease != nil {
		x.lease.Close()
	}
	*x = graphSchemaDocument{}
	d.schemaManagement = nil
}

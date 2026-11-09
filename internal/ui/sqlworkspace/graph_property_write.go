package sqlworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphresult"
)

const graphPropertyRecordBudget = 8 << 20

type graphPropertyDraft struct {
	request               contract.GraphPropertyWriteRequest
	text, error           string
	notice                string
	preview               *contract.GraphPropertyWriteRequest
	owner                 *graphVisualState
	lease                 Lease
	attempted, unresolved bool
}
type graphPropertyRecord struct {
	Request      contract.GraphPropertyWriteRequest
	Time         time.Time
	Receipt      contract.GraphPropertyWriteReceipt
	Verification bool
	Error        string
}

func (r graphPropertyRecord) clone() graphPropertyRecord {
	r.Request = r.Request.Clone()
	r.Receipt = r.Receipt.Clone()
	return r
}

type graphPropertyOutcome struct {
	receipt contract.GraphPropertyWriteReceipt
	err     error
}
type graphPropertyPending struct {
	request          contract.GraphPropertyWriteRequest
	owner            *graphVisualState
	lease            Lease
	draft            *graphPropertyDraft
	verify, canceled bool
	started          time.Time
	cancel           context.CancelFunc
	results          <-chan graphPropertyOutcome
}

func graphPropertyValue(kind, text string) (contract.GraphValue, error) {
	v := contract.GraphValue{Kind: kind, Text: text}
	valid := utf8.ValidString(text) && len(text) <= 1<<20
	switch kind {
	case "string":
	case "boolean":
		valid = valid && (text == "true" || text == "false")
	case "integer":
		n, e := strconv.ParseInt(text, 10, 64)
		valid = valid && e == nil && strconv.FormatInt(n, 10) == text
	case "float":
		n, e := strconv.ParseFloat(text, 64)
		valid = valid && e == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
	default:
		valid = false
	}
	if !valid {
		return v, errors.New("图属性值类型或范围不受支持")
	}
	return v, nil
}
func graphPropertySize(v any) int { raw, _ := json.Marshal(v); return len(raw) + 128 }
func (s *State) graphPropertyLeaseReady(d *Document) bool {
	if d == nil || d.closed || d.preview || d.snapshotOwner != nil || !d.cypher() || d.Loading() || d.recoveryNeeded || d.prepareResults != nil || !s.connected(d.profileID) || d.graphPropertyBytes >= graphPropertyRecordBudget-(4<<20) || len(d.graphPropertyRecords) >= 1000 {
		return false
	}
	status, ok := d.lease.(contract.QueryLeaseStatus)
	return ok && status.CheckQueryLease() == nil
}
func (s *State) canEditGraphProperty(d *Document, g *graphVisualState, p contract.GraphProperty) bool {
	if !s.graphPropertyLeaseReady(d) || g == nil || g.closed || d.graphPropertyDraft != nil || d.result == nil || d.result.Graph == nil || !d.result.Graph.Completed || d.result.Graph.Uncertain {
		return false
	}
	if _, ok := d.lease.(contract.GraphPropertyWriteLease); !ok {
		return false
	}
	if capability, ok := d.lease.(interface{ SupportsGraphPropertyWrites() bool }); ok && !capability.SupportsGraphPropertyWrites() {
		return false
	}
	if _, e := graphPropertyValue(p.Value.Kind, p.Value.Text); e != nil {
		return false
	}
	if node := g.node(g.selected); node != nil {
		return !node.Missing && !node.Conflicting && node.Value.ElementID != ""
	}
	if edge := g.edge(g.selected); edge != nil {
		return !edge.Conflicting && edge.Value.ElementID != "" && edge.Value.StartElementID != "" && edge.Value.EndElementID != ""
	}
	return false
}
func (s *State) openGraphPropertyDraft(d *Document, g *graphVisualState, p contract.GraphProperty) {
	if !s.canEditGraphProperty(d, g, p) {
		return
	}
	target := contract.GraphPropertyTarget{}
	if node := g.node(g.selected); node != nil {
		target = contract.GraphPropertyTarget{Kind: "node", ElementID: node.Value.ElementID, Labels: append([]string(nil), node.Value.Labels...)}
	}
	if edge := g.edge(g.selected); edge != nil {
		target = contract.GraphPropertyTarget{Kind: "relationship", ElementID: edge.Value.ElementID, SourceID: edge.Value.StartElementID, TargetID: edge.Value.EndElementID, RelationshipType: edge.Value.Type}
	}
	r := contract.GraphPropertyWriteRequest{Namespace: d.Namespace(), Target: target, Property: p.Name, Before: p.Value.Clone(), After: p.Value.Clone()}
	d.graphPropertyDraft = &graphPropertyDraft{request: r, text: p.Value.Text, owner: g, lease: d.lease}
	d.graphPropertyOpen = true
	d.graphPropertyRecord = -1
}
func (s *State) previewGraphProperty(d *Document) {
	p := d.graphPropertyDraft
	if p == nil || p.attempted {
		return
	}
	v, e := graphPropertyValue(p.request.Before.Kind, p.text)
	p.error = ""
	p.notice = ""
	p.preview = nil
	if e != nil {
		p.error = e.Error()
		return
	}
	r := p.request.Clone()
	r.After = v
	if r.Before.Kind == r.After.Kind && r.Before.Text == r.After.Text {
		p.error = "属性值未更改"
		return
	}
	// The native parameter capture has a shared 1 MiB budget for old/new values.
	if graphPropertySize(r) > 1<<20 {
		p.error = "原值和新值的参数总量超过 1 MiB"
		return
	}
	p.preview = &r
}
func (s *State) saveGraphProperty(d *Document) {
	p := d.graphPropertyDraft
	if p == nil || p.preview == nil || p.attempted || !s.graphPropertyLeaseReady(d) {
		return
	}
	if d.graphVisual != p.owner || d.lease != p.lease || d.Namespace() != p.request.Namespace {
		p.error = "属性草稿属于此前会话或结果；不会自动提交"
		return
	}
	request := p.preview.Clone()
	p.attempted = true
	p.error = ""
	s.startGraphProperty(d, request, p, false)
}
func (s *State) startGraphProperty(d *Document, request contract.GraphPropertyWriteRequest, draft *graphPropertyDraft, verify bool) {
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan graphPropertyOutcome, 1)
	n := &graphPropertyPending{request: request.Clone(), owner: d.graphVisual, lease: d.lease, draft: draft, verify: verify, started: time.Now(), cancel: cancel, results: results}
	d.graphPropertyPending = n
	lease, invalidate := d.lease, s.Invalidate
	go func() {
		var receipt contract.GraphPropertyWriteReceipt
		var err error
		if verify {
			query := "MATCH (e) WHERE elementId(e)=$id RETURN e AS entity"
			if request.Target.Kind == "relationship" {
				query = "MATCH ()-[e]->() WHERE elementId(e)=$id RETURN e AS entity"
			}
			receipt.Query = query
			receipt.Result, err = lease.(graphLease).ExecuteGraph(ctx, contract.GraphQueryRequest{Query: query, Database: request.Namespace.Catalog, Mode: "read", Parameters: map[string]any{"id": request.Target.ElementID}, MaxRows: 2, MaxBytes: 2 << 20, FetchSize: 2, TimeoutSeconds: 30})
		} else {
			receipt, err = lease.(contract.GraphPropertyWriteLease).WriteGraphProperty(ctx, request)
		}
		results <- graphPropertyOutcome{receipt, err}
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) verifyGraphProperty(d *Document, index int) {
	if !s.graphPropertyLeaseReady(d) || index < 0 || index >= len(d.graphPropertyRecords) {
		return
	}
	r := d.graphPropertyRecords[index].Request.Clone()
	if r.Namespace != d.Namespace() {
		return
	}
	if _, ok := d.lease.(graphLease); !ok {
		return
	}
	s.startGraphProperty(d, r, nil, true)
}
func (s *State) cancelGraphProperty(d *Document) bool {
	if d == nil || d.graphPropertyPending == nil {
		return false
	}
	n := d.graphPropertyPending
	if !n.canceled {
		n.canceled = true
		n.cancel()
	}
	return true
}
func (s *State) consumeGraphProperty(d *Document) {
	n := d.graphPropertyPending
	if n == nil || !s.active(d) {
		return
	}
	select {
	case result := <-n.results:
		d.graphPropertyPending = nil
		n.cancel()
		status := n.lease.(contract.QueryLeaseStatus).CheckQueryLease()
		err := errors.Join(result.err, status)
		lost := status != nil || errors.Is(err, contract.ErrSQLSessionLost) || errors.Is(err, contract.ErrRevoked) || result.receipt.Result.Uncertain || result.receipt.Result.Attempted && !result.receipt.Result.Completed
		if lost {
			d.recoveryNeeded = true
			d.recoveryProfile, d.recoverySchema = n.request.Namespace.ProfileID, n.request.Namespace.Catalog
			d.recoveryCatalog, d.recoveryNamespaceSchema = n.request.Namespace.Catalog, n.request.Namespace.Schema
		}
		message := ""
		if err != nil {
			message = err.Error()
		}
		notice := ""
		if result.receipt.Conflict {
			notice = "属性或图元已变化；未替换目标值，请显式核对当前图元"
		}
		if lost {
			notice = "图属性操作缺少当前原会话确认；保留草稿和回执，不会重放"
		}
		if n.draft != nil {
			n.draft.error = message
			n.draft.notice = notice
			n.draft.unresolved = lost || result.receipt.Result.Attempted && !result.receipt.Applied && !result.receipt.Conflict
			if !lost && !result.receipt.Result.Attempted && !result.receipt.Result.Uncertain {
				n.draft.attempted = false
				n.draft.preview = nil
			}
			if !lost && err == nil && result.receipt.Applied && d.graphPropertyDraft == n.draft {
				d.graphPropertyDraft = nil
			}
		}
		record := graphPropertyRecord{Request: n.request.Clone(), Time: n.started, Receipt: result.receipt.Clone(), Verification: n.verify, Error: message}
		d.graphPropertyRecords = append(d.graphPropertyRecords, record)
		d.graphPropertyBytes += graphPropertySize(record)
		if err == nil && !lost && d.lease == n.lease && d.Namespace() == n.request.Namespace && d.graphVisual == n.owner && n.owner != nil && !n.owner.closed {
			entity := contract.GraphValue{}
			if n.verify && result.receipt.Result.Completed && !result.receipt.Result.Truncated && len(result.receipt.Result.Rows) == 1 && len(result.receipt.Result.Rows[0]) == 1 {
				entity = result.receipt.Result.Rows[0][0]
			}
			if !n.verify && result.receipt.Applied && len(result.receipt.Result.Rows) == 1 && len(result.receipt.Result.Rows[0]) == 4 {
				entity = result.receipt.Result.Rows[0][3]
			}
			n.owner.applyGraphPropertyEntity(n.request.Target, entity)
		}
		d.graphPropertyRecord = len(d.graphPropertyRecords) - 1
		if lost {
			d.err = notice
		}
	default:
	}
}
func (g *graphVisualState) applyGraphPropertyEntity(target contract.GraphPropertyTarget, entity contract.GraphValue) {
	key := ""
	if entity.Kind == "node" && entity.Node != nil && target.Kind == "node" && entity.Node.ElementID == target.ElementID && g.node("n:"+target.ElementID) != nil {
		key = "n:" + target.ElementID
	}
	if entity.Kind == "relationship" && entity.Relationship != nil && target.Kind == "relationship" && entity.Relationship.ElementID == target.ElementID && entity.Relationship.StartElementID == target.SourceID && entity.Relationship.EndElementID == target.TargetID && g.edge("r:"+target.ElementID) != nil {
		key = "r:" + target.ElementID
	}
	if key == "" {
		return
	}
	if g.propertyEntities == nil {
		g.propertyEntities = map[string]graphresult.EntityObservation{}
	}
	g.propertyEntities[key] = graphresult.EntityObservation{AfterObservations: len(g.observations), Value: entity.Clone()}
	g.rebuildObservations()
	g.fieldJSON, g.detail = "", ""
	g.matchQuery, g.matchKeys = "", nil
}
func (s *State) exportGraphPropertyRecords(d *Document) {
	if d == nil || d.closed || len(d.graphPropertyRecords) == 0 {
		return
	}
	f := s.persistence.files
	if f.SavePath == nil || f.WriteStream == nil {
		return
	}
	raw, err := json.MarshalIndent(d.graphPropertyRecords, "", "  ")
	if err != nil {
		d.err = err.Error()
		return
	}
	name := d.Name() + "-graph-properties.json"
	s.startIO(d, nil, func() ioResult {
		path, e := f.SavePath(name)
		if e != nil || path == "" {
			return ioResult{kind: "export", err: e}
		}
		e = f.WriteStream(path, func(w io.Writer) error { _, e := w.Write(raw); return e })
		return ioResult{kind: "export", err: e}
	})
}

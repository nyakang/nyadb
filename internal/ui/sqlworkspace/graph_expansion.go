package sqlworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphresult"
	"github.com/nyakang/nyadb/locales"
)

const graphExpansionBytes = 8 << 20
const graphExpansionRows = 200
const graphExpansionQuery = "MATCH (n) WHERE elementId(n) = $node OPTIONAL MATCH (n)-[r]-(m) RETURN n, r, m ORDER BY elementId(r) LIMIT $limit"

// An observation owns its native request and receipt. Its rows never enter the
// original result table. A rejected payload still retains the native receipt.
type graphObservation struct {
	Node, Query, Error                 string
	Namespace                          contract.NamespaceRef
	Time                               time.Time
	Result                             contract.GraphResult
	Accepted, Limited, PayloadRetained bool
}
type graphExpansionResult struct {
	result contract.GraphResult
	err    error
}
type graphExpansionRequest struct {
	owner     *graphVisualState
	lease     Lease
	namespace contract.NamespaceRef
	database  string
	node      string
	started   time.Time
	cancel    context.CancelFunc
	canceled  bool
	results   <-chan graphExpansionResult
}

func (s *State) canExpandGraph(d *Document) bool {
	if d == nil || d.closed || d.preview || d.snapshotOwner != nil || !d.cypher() || d.Loading() || d.recoveryNeeded || d.prepareResults != nil || d.profileID <= 0 || !s.connected(d.profileID) || d.result == nil || d.result.Graph == nil || !d.result.Graph.Completed || d.result.Graph.Uncertain {
		return false
	}
	g := d.graphVisualState()
	if g == nil || g.topology.Bounded || g.node(g.selected) == nil || g.node(g.selected).Value.ElementID == "" || len(g.topology.Nodes) >= graphresult.TopologyMaxNodes || len(g.topology.Edges) >= graphresult.TopologyMaxEdges || g.observationBytes >= graphExpansionBytes-(32<<10) || len(g.observations) >= graphresult.TopologyMaxNodes {
		return false
	}
	_, graph := d.lease.(graphLease)
	status, bound := d.lease.(contract.QueryLeaseStatus)
	if !graph || !bound || status.CheckQueryLease() != nil {
		return false
	}
	if support, ok := d.lease.(interface{ SupportsGraph() bool }); ok && !support.SupportsGraph() {
		return false
	}
	return true
}
func (s *State) expandGraph(d *Document) {
	if !s.canExpandGraph(d) {
		return
	}
	g, lease := d.graphVisualState(), d.lease
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan graphExpansionResult, 1)
	n := &graphExpansionRequest{owner: g, lease: lease, namespace: d.Namespace(), database: d.database, node: g.node(g.selected).Value.ElementID, started: time.Now(), cancel: cancel, results: results}
	d.graphExpansion = n
	g.expansionError, d.err = "", ""
	query := contract.GraphQueryRequest{Query: graphExpansionQuery, Database: n.database, Mode: "read", Parameters: map[string]any{"node": n.node, "limit": int64(graphExpansionRows)}, MaxRows: graphExpansionRows, MaxBytes: min(4<<20, graphExpansionBytes-g.observationBytes-(16<<10)), FetchSize: graphExpansionRows, TimeoutSeconds: 30}
	execute, invalidate := lease.(graphLease).ExecuteGraph, s.Invalidate
	go func() {
		r, err := execute(ctx, query)
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		results <- graphExpansionResult{r, err}
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (d *Document) stopGraphExpansion() {
	if d.graphExpansion != nil {
		d.graphExpansion.cancel()
		d.graphExpansion = nil
	}
}
func (d *Document) clearGraphVisual() {
	d.stopGraphExpansion()
	d.graphJSON = ""
	if d.graphVisual != nil {
		d.graphVisual.release()
	}
	d.graphVisual, d.graphVisualOpen = nil, false
}
func (s *State) cancelGraphExpansion(d *Document) bool {
	if d == nil || d.graphExpansion == nil {
		return false
	}
	n := d.graphExpansion
	if !n.canceled {
		n.canceled = true
		n.cancel()
		n.owner.expansionError = locales.Text("已请求取消邻居扩展；等待原执行器确认")
	}
	return true
}
func (s *State) consumeGraphExpansion(d *Document) {
	n := d.graphExpansion
	if n == nil || !s.active(d) {
		return
	}
	select {
	case result := <-n.results:
		d.graphExpansion = nil
		n.cancel()
		if d.graphVisual != n.owner || n.owner.closed || d.lease != n.lease || d.Namespace() != n.namespace || d.database != n.database {
			return
		}
		status := n.lease.(contract.QueryLeaseStatus).CheckQueryLease()
		err := errors.Join(result.err, status)
		lost := status != nil || errors.Is(err, contract.ErrSQLSessionLost) || errors.Is(err, contract.ErrRevoked) || result.result.Uncertain || result.result.Attempted && !result.result.Completed
		if lost {
			d.recoveryNeeded = true
			d.recoveryProfile, d.recoverySchema = d.profileID, d.database
			d.recoveryCatalog, d.recoveryNamespaceSchema = d.catalog, d.namespaceSchema
		}
		accepted := err == nil && !n.canceled && result.result.Completed && !result.result.Uncertain
		message := ""
		if n.canceled || errors.Is(err, context.Canceled) {
			message = locales.Text("邻居扩展已停止；保留此前图形，不会自动重试")
		} else if err != nil {
			message = err.Error()
		} else if !accepted {
			message = locales.Text("邻居扩展未获得原生完成确认；保留此前图形")
		}
		if lost {
			message += " · " + locales.Text("原请求会话已失效；请先核对服务器端结果，再建立新会话。不会自动执行现有请求")
		}
		o := graphObservation{Node: n.node, Query: graphExpansionQuery, Namespace: n.namespace, Time: n.started, Result: result.result, Accepted: accepted, Limited: result.result.Truncated || len(result.result.Rows) >= graphExpansionRows, PayloadRetained: true, Error: message}
		n.owner.addObservation(o)
		n.owner.expansionError = message
		if lost {
			d.err = message
		}
	default:
	}
}
func graphObservationSize(o graphObservation) int {
	raw, _ := json.Marshal(o)
	return len(raw) + 128
}
func (g *graphVisualState) addObservation(o graphObservation) {
	size := graphObservationSize(o)
	if g.observationBytes+size > graphExpansionBytes {
		o.Result.Rows = nil
		o.Accepted, o.PayloadRetained = false, false
		o.Error = locales.Text("扩展数据超过保留预算；已保留原生回执和此前图形")
		size = graphObservationSize(o)
	}
	o.Result = o.Result.Clone()
	g.observations = append(g.observations, o)
	g.observationBytes += size
	g.fieldJSON, g.detail = "", ""
	g.matchQuery, g.matchKeys = "", nil
	if o.Accepted {
		g.rebuildObservations()
	}
}
func (g *graphVisualState) rebuildObservations() {
	if g.base == nil {
		return
	}
	sources := make([]contract.GraphResult, len(g.observations))
	for index, o := range g.observations {
		if o.Accepted {
			sources[index] = o.Result
		}
	}
	g.topology = graphresult.ExtractTopologyObservations(*g.base, sources)
	fixed := make(map[string]bool, len(g.positions))
	for key := range g.positions {
		fixed[key] = true
	}
	newNodes := 0
	for index, node := range g.topology.Nodes {
		if _, exists := g.positions[node.Key]; exists {
			continue
		}
		center := ui.Point{}
		for _, edge := range g.topology.Edges {
			other := ""
			if edge.Source == node.Key {
				other = edge.Target
			} else if edge.Target == node.Key {
				other = edge.Source
			}
			if p, ok := g.positions[other]; ok {
				center = p
				break
			}
		}
		angle := float64(index) * 2.399963229728653
		newNodes++
		radius := float32(140 + 24*math.Sqrt(float64(newNodes)))
		g.positions[node.Key] = ui.Point{X: center.X + float32(math.Cos(angle))*radius, Y: center.Y + float32(math.Sin(angle))*radius}
	}
	if newNodes > 0 {
		g.relax(fixed)
	}
}
func (g *graphVisualState) observationRefs() []graphresult.ObservationRef {
	if n := g.node(g.selected); n != nil {
		return n.Observations
	}
	if e := g.edge(g.selected); e != nil {
		return e.Observations
	}
	return nil
}
func (g *graphVisualState) observationDetail() string {
	if g.observationIndex < 0 || g.observationIndex >= len(g.observations) {
		return ""
	}
	if g.fieldJSON == "" {
		g.fieldJSON = boundedGraphJSON(g.observations[g.observationIndex])
	}
	return g.fieldJSON
}
func (s *State) exportGraphObservations(d *Document) {
	if d == nil || d.closed || d.graphVisual == nil || len(d.graphVisual.observations) == 0 {
		return
	}
	f := s.persistence.files
	if f.SavePath == nil || f.WriteStream == nil {
		return
	}
	raw, err := json.MarshalIndent(d.graphVisual.observations, "", "  ")
	if err != nil {
		d.err = err.Error()
		return
	}
	name := d.Name() + "-neighbors.json"
	s.startIO(d, nil, func() ioResult {
		path, err := f.SavePath(name)
		if err != nil || path == "" {
			return ioResult{kind: "export", err: err}
		}
		err = f.WriteStream(path, func(w io.Writer) error { _, err := w.Write(raw); return err })
		return ioResult{kind: "export", err: err}
	})
}

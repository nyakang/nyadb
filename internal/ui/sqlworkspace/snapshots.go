package sqlworkspace

import (
	"encoding/json"
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
	"time"
)

const snapshotMaxBytes = 16 << 20
const snapshotMaxCount = 3

type resultSnapshot struct {
	Document  *Document
	SQL       string
	Namespace contract.NamespaceRef
	Time      time.Time
	Bytes     int64
}

func resultSnapshotSize(r *contract.SQLResult) int64 {
	var n int64
	if r.Graph != nil {
		raw, _ := json.Marshal(r.Graph)
		n += int64(len(raw))
	}
	for _, step := range r.Steps {
		n += int64(len(step.SQL) + len(step.State) + len(step.Error) + 64)
	}
	for _, set := range r.ResultSets() {
		n += int64(128 + len(set.LayoutKey) + (len(set.DefaultHiddenColumns)+len(set.OriginColumns))*8)
		for _, c := range set.Columns {
			n += int64(len(c.Name) + len(c.DatabaseType) + 64)
		}
		for _, reason := range set.Truncation {
			n += int64(len(reason) + 16)
		}
		for _, row := range set.Rows {
			n += 24
			for _, cell := range row {
				n += int64(len(cell.Text) + len(cell.Bytes) + 64)
			}
		}
	}
	return n
}

func (s *State) pinResult(d *Document) bool {
	if d == nil || d.closed || d.Loading() || d.result == nil {
		return false
	}
	n := resultSnapshotSize(d.result) + int64(len(d.executedSQL))
	planBytes, _ := json.Marshal(d.plan)
	n += int64(len(planBytes))
	n += int64(d.graphPropertyBytes)
	if d.graphVisual != nil {
		n += int64(d.graphVisual.observationBytes)
	}
	for _, snap := range d.snapshots {
		n += snap.Bytes
	}
	if len(d.snapshots) >= snapshotMaxCount || n > snapshotMaxBytes {
		d.err = locales.Text("每个查询最多保留 3 个结果快照，总计 16 MiB；请先移除旧快照")
		return false
	}
	r := &contract.SQLResult{Steps: append([]contract.StatementOutcome(nil), d.result.Steps...), Duration: d.result.Duration, AffectedRows: d.result.AffectedRows, Truncated: d.result.Truncated}
	if d.result.Graph != nil {
		graph := d.result.Graph.Clone()
		r.Graph = &graph
	}
	for _, set := range d.result.ResultSets() {
		r.Sets = append(r.Sets, cloneCachedResultSet(set))
	}
	x := &Document{commandDialect: d.commandDialect, snapshotOwner: d, id: d.id, name: d.Name(), profileID: d.profileID, database: d.database, catalog: d.catalog, namespaceSchema: d.namespaceSchema}
	x.setResult(r)
	x.plan, x.planError, x.planVisible = d.plan.Clone(), d.planError, d.planVisible
	x.graphPlanNative = d.graphPlanNative
	if d.graphVisual != nil {
		x.graphVisualState().cloneReading(d.graphVisual)
	}
	x.graphVisualOpen = d.graphVisualOpen
	for _, record := range d.graphPropertyRecords {
		x.graphPropertyRecords = append(x.graphPropertyRecords, record.clone())
	}
	x.graphPropertyBytes = d.graphPropertyBytes
	d.nextSnapshot++
	x.name = fmt.Sprintf("%s-%d", d.Name(), d.nextSnapshot)
	bytes := resultSnapshotSize(r) + int64(len(d.executedSQL))
	bytes += int64(len(planBytes))
	bytes += int64(x.graphPropertyBytes)
	if x.graphVisual != nil {
		bytes += int64(x.graphVisual.observationBytes)
	}
	d.snapshots = append(d.snapshots, resultSnapshot{Document: x, SQL: d.executedSQL, Namespace: d.Namespace(), Time: time.Now(), Bytes: bytes})
	return true
}
func (d *Document) removeSnapshot(index int) {
	if index < 0 || index >= len(d.snapshots) {
		return
	}
	x := d.snapshots[index].Document
	x.cachedExport = cachedExportDraft{}
	if x.graphVisual != nil {
		x.graphVisual.release()
	}
	x.graphVisual, x.graphVisualOpen = nil, false
	x.graphJSON = ""
	x.graphPropertyRecords = nil
	x.graphPropertyBytes = 0
	x.graphPropertyOpen = false
	x.closed = true
	x.snapshotOwner = nil
	x.selection.Clear()
	x.hiddenResultColumns = nil
	x.result = nil
	x.plan, x.graphPlanJSON = nil, ""
	x.resultViews = nil
	x.grid = ui.ListState{}
	d.snapshots = append(d.snapshots[:index], d.snapshots[index+1:]...)
	d.snapshotIndex = 0
}

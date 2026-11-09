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
		n += 128
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
	if d == nil || d.closed || d.loading || d.result == nil {
		return false
	}
	n := resultSnapshotSize(d.result) + int64(len(d.executedSQL))
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
	d.nextSnapshot++
	x.name = fmt.Sprintf("%s-%d", d.Name(), d.nextSnapshot)
	d.snapshots = append(d.snapshots, resultSnapshot{Document: x, SQL: d.executedSQL, Namespace: d.Namespace(), Time: time.Now(), Bytes: resultSnapshotSize(r) + int64(len(d.executedSQL))})
	return true
}
func (d *Document) removeSnapshot(index int) {
	if index < 0 || index >= len(d.snapshots) {
		return
	}
	x := d.snapshots[index].Document
	x.cachedExport = cachedExportDraft{}
	x.closed = true
	x.snapshotOwner = nil
	x.selection.Clear()
	x.hiddenResultColumns = nil
	x.result = nil
	x.resultViews = nil
	x.grid = ui.ListState{}
	d.snapshots = append(d.snapshots[:index], d.snapshots[index+1:]...)
	d.snapshotIndex = 0
}

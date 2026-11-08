package objectworkspace

import (
	"encoding/json"
	"github.com/egoist/mygo/ui"
)

type taskPresentation struct {
	Version                                                                                   int
	Columns                                                                                   ui.TableLayout
	UID, Indexes, Status, Type, Limit, Order, Batch, CanceledBy                               string
	BeforeEnqueued, AfterEnqueued, BeforeStarted, AfterStarted, BeforeFinished, AfterFinished string
}

func snapshotTasks(d *Document) json.RawMessage {
	x := d.tasks
	raw, _ := json.Marshal(taskPresentation{Version: 1, Columns: d.columns.Columns, UID: x.uids, Indexes: x.indexes, Status: x.status, Type: x.kind, Limit: x.limit, Order: x.order, Batch: x.batch, CanceledBy: x.canceledBy, BeforeEnqueued: x.beforeEnqueued, AfterEnqueued: x.afterEnqueued, BeforeStarted: x.beforeStarted, AfterStarted: x.afterStarted, BeforeFinished: x.beforeFinished, AfterFinished: x.afterFinished})
	return raw
}
func restoreTasks(d *Document, raw json.RawMessage) {
	if len(raw) == 0 || len(raw) > 65536 {
		return
	}
	var p taskPresentation
	if json.Unmarshal(raw, &p) != nil || p.Version != 1 {
		return
	}
	x := d.tasks
	x.uids, x.indexes, x.status, x.kind, x.limit, x.order, x.batch, x.canceledBy = p.UID, p.Indexes, p.Status, p.Type, p.Limit, p.Order, p.Batch, p.CanceledBy
	x.beforeEnqueued, x.afterEnqueued, x.beforeStarted, x.afterStarted, x.beforeFinished, x.afterFinished = p.BeforeEnqueued, p.AfterEnqueued, p.BeforeStarted, p.AfterStarted, p.BeforeFinished, p.AfterFinished
	d.columns.Columns = p.Columns
	// Opaque cursors, physical leases, raw tasks and results are never restored.
	// Draft filters remain inert until the user explicitly applies them.
	x.fresh = false
	d.err = "任务筛选已恢复；请显式应用筛选"
}

package dataworkspace

import (
	"encoding/json"
	"github.com/nyakang/nyadb/internal/db/contract"
	"reflect"
)

type draftSnapshot struct {
	Drafts    map[string]*mysqlRowDraft
	Rows      [][]contract.DataCell
	Refs      []*contract.RowRef
	Originals []*contract.RowSnapshot
	Row       int
}
type draftChange struct {
	Before, After draftSnapshot
	Page          *contract.DataPage
	Bytes         int64
}

func captureDraft(t *Document) draftSnapshot {
	x := draftSnapshot{Drafts: map[string]*mysqlRowDraft{}, Row: t.selectedRow}
	for k, d := range t.edit.Drafts {
		if d.Kind == "update" && len(d.Values) == 0 {
			continue
		}
		c := *d
		c.GraphEndpoints = contract.CloneDataGraphEndpoints(d.GraphEndpoints)
		c.Original = cloneSnapshot(d.Original)
		c.Before = clonePreviews(d.Before)
		c.Values = cloneValues(d.Values)
		x.Drafts[k] = &c
	}
	for _, r := range t.page.Rows {
		x.Rows = append(x.Rows, clonePreviews(r))
	}
	for _, r := range t.page.RowRefs {
		if r == nil {
			x.Refs = append(x.Refs, nil)
		} else {
			c := cloneRowRef(*r)
			x.Refs = append(x.Refs, &c)
		}
	}
	for _, r := range t.page.RowSnapshots {
		x.Originals = append(x.Originals, cloneSnapshot(r))
	}
	return x
}
func draftSnapshotBytes(x draftSnapshot) int64 {
	// Include every identity, fingerprint and full edit value. A conservative
	// serialized estimate bounds retained content as well as map/slice overhead.
	b, _ := json.Marshal(x)
	return int64(len(b))*2 + int64(len(x.Rows)+len(x.Refs)+len(x.Originals)+len(x.Drafts))*256
}
func pushDraftChange(t *Document, before draftSnapshot) {
	if t.page == nil || t.edit.Busy || t.edit.Uncertain != nil {
		return
	}
	after := captureDraft(t)
	if reflect.DeepEqual(before, after) {
		return
	}
	t.edit.Redo = nil
	change := draftChange{Before: before, After: after, Page: t.page, Bytes: draftSnapshotBytes(before) + draftSnapshotBytes(after)}
	if change.Bytes > 8<<20 {
		t.edit.Undo = nil
		return
	}
	t.edit.Undo = append(t.edit.Undo, change)
	var n int64
	for _, x := range t.edit.Undo {
		n += x.Bytes
	}
	for len(t.edit.Undo) > 32 || n > 8<<20 {
		n -= t.edit.Undo[0].Bytes
		t.edit.Undo[0] = draftChange{}
		t.edit.Undo = t.edit.Undo[1:]
	}
}
func beginDraftChange(t *Document) func() {
	if t == nil || t.page == nil || t.edit.historyGroup > 0 {
		return func() {}
	}
	before := captureDraft(t)
	t.edit.historyGroup++
	return func() { t.edit.historyGroup--; pushDraftChange(t, before) }
}
func clearDraftHistory(t *Document) { t.edit.Undo = nil; t.edit.Redo = nil }
func (s *State) canDraftUndo(t *Document, redo bool) bool {
	if t == nil || t.closed || t.page == nil || t.loading || t.edit.Busy || t.edit.Uncertain != nil || t.transfer.Busy || t.transfer.Uncertain || dataTransactionUnavailable(t) || !s.canUseSession(t) {
		return false
	}
	xs := t.edit.Undo
	if redo {
		xs = t.edit.Redo
	}
	return len(xs) > 0 && xs[len(xs)-1].Page == t.page
}
func (s *State) undoDraft(t *Document, redo bool) bool {
	if !s.canDraftUndo(t, redo) {
		return false
	}
	s.finishMySQLCellEdit(t, true)
	if t.edit.Editor != nil {
		return false
	}
	source, dest := &t.edit.Undo, &t.edit.Redo
	if redo {
		source, dest = dest, source
	}
	x := (*source)[len(*source)-1]
	*source = (*source)[:len(*source)-1]
	*dest = append(*dest, x)
	snapshot := x.Before
	if redo {
		snapshot = x.After
	}
	// Histories remain independent of previews and the restored mutable draft map.
	temp := &Document{page: &contract.DataPage{Rows: snapshot.Rows, RowRefs: snapshot.Refs, RowSnapshots: snapshot.Originals}, selectedRow: snapshot.Row, edit: mysqlDataEditState{Drafts: snapshot.Drafts}}
	restored := captureDraft(temp)
	t.edit.Drafts = restored.Drafts
	t.page.Rows = restored.Rows
	t.page.RowRefs = restored.Refs
	t.page.RowSnapshots = restored.Originals
	t.selectedRow = min(restored.Row, len(t.page.Rows)-1)
	t.selection.Clear()
	if t.selectedRow >= 0 {
		t.selection.Add(t.selectedRow)
	}
	t.edit.Err = ""
	t.edit.AfterSave = nil
	mysqlDataClearColumnSelection(t)
	return true
}

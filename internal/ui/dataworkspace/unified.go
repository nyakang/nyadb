package dataworkspace

import (
	"context"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/locales"
	"io"
	"slices"
	"strings"
)

func (s *State) OpenObject(id uint64, o contract.ObjectRef, c contract.Capabilities) *Document {
	namespace := o.Schema
	if namespace == "" {
		namespace = o.Catalog
	}
	if s.deps.NamespaceLabel != nil {
		namespace = s.deps.NamespaceLabel(o.Namespace())
	}
	kind := "表"
	if o.Kind == "keyspace" {
		kind = "键"
	}
	if o.Kind == "collection" {
		kind = "集合"
	}
	if o.Kind == "view" {
		kind = "视图"
	}
	switch o.Kind {
	case "label":
		kind = "图标签"
	case "relationship":
		kind = "关系类型"
	case "points":
		kind = "向量点"
	case "documents":
		kind = "文档"
	case "core":
		kind = "Core"
	case "index":
		kind = "索引"
	case "alias":
		kind = "别名"
	case "data_stream":
		kind = "数据流"
	}
	var d *Document
	if o.Kind == contract.GraphNamespaceKind {
		if id == 0 || o.ProfileID <= 0 || o.Catalog == "" || o.Schema != "" || o.Name != "" || !c.GraphExchange {
			return nil
		}
		if old := s.documents[id]; old != nil {
			s.Close(old)
		}
		d = &Document{id: id, profileID: o.ProfileID, database: namespace, objectKind: "图交换", graphTransferOnly: true, selectedRow: -1, hiddenColumns: map[string]bool{}}
		s.documents[id] = d
	} else {
		d = s.Open(id, o.ProfileID, namespace, o.Name, kind)
	}
	if d == nil {
		return nil
	}
	d.object = o
	d.capabilities = c
	parent := s.runtimeFor(d)
	if parent.ReserveDocument == nil && d.graphTransferOnly {
		d.err = contract.ErrUnsupported.Error()
		d.capabilities = contract.Capabilities{}
		d.boundRuntime = &Runtime{}
	} else if parent.ReserveDocument != nil {
		lease, err := parent.ReserveDocument(o)
		if err == nil && lease == nil {
			err = contract.ErrUnsupported
		}
		if err != nil {
			if lease != nil {
				_ = lease.Close()
			}
			d.err = err.Error()
			d.capabilities = contract.Capabilities{}
			d.boundRuntime = &Runtime{}
		} else {
			d.runtimeLease = lease
			bound := lease.Runtime()
			bound.ReserveDocument = nil
			bound.DocumentImportModes = slices.Clone(bound.DocumentImportModes)
			bound.EditKinds = slices.Clone(bound.EditKinds)
			bound.PreviewEditKinds = slices.Clone(bound.PreviewEditKinds)
			bound.EditValueKinds = slices.Clone(bound.EditValueKinds)
			bound.Capabilities = contract.BoundDataCapabilities(bound)
			d.capabilities = contract.IntersectCapabilities(c, bound.Capabilities)
			bound.Capabilities = d.capabilities
			d.boundRuntime = &bound
			if d.graphTransferOnly && !d.capabilities.GraphExchange {
				d.err = contract.ErrUnsupported.Error()
			}
		}
	}
	d.exportFormats = append([]string(nil), s.runtimeFor(d).ExportFormats...)
	if runtime := s.runtimeFor(d); runtime.AsyncWrites || len(runtime.PreviewEditKinds) > 0 {
		d.edit.Batch = true
	}
	d.generation = s.sessionFor(d).Generation
	d.filterModes = append([]string(nil), s.runtimeFor(d).FilterModes...)
	if len(d.filterModes) > 0 {
		d.filterModeDraft = mysqlDataFilterModeLabel(d.filterModes[0])
	}
	d.useKeyset = d.capabilities.Keyset
	return d
}
func (d *Document) Object() contract.ObjectRef { return d.object }
func (s *State) SetFiles(pick func(string) (string, error), write func(string, func(io.Writer) error) error) {
	s.savePath = pick
	s.writeStream = write
}
func (s *State) EditCell(services ui.Services, d *Document, row int, field string, v contract.Cell) {
	if d == nil || d.page == nil {
		return
	}
	for i, n := range d.page.Columns {
		if n == field {
			s.setMySQLDataCell(services, d, row, i, contract.DataValue{Kind: v.Kind, Text: v.Text, Bytes: append([]byte(nil), v.Bytes...), Null: v.Kind == contract.Null, Binary: v.Kind == contract.Binary})
			return
		}
	}
}
func (s *State) Save(services ui.Services, d *Document) { s.saveMySQLData(services, d) }
func (s *State) CanSave(d *Document) bool {
	return d != nil && !d.closed && !d.loading && !d.Busy() && d.edit.Uncertain == nil && !d.transfer.Uncertain && !dataTransactionUnavailable(d) && s.canUseSession(d) && d.profileID == s.session().ProfileID && d.HasDraft()
}
func (s *State) Begin(services ui.Services, d *Document)    { s.beginDataTransaction(services, d) }
func (s *State) Commit(services ui.Services, d *Document)   { s.commitDataTransaction(services, d) }
func (s *State) Rollback(services ui.Services, d *Document) { s.rollbackDataTransaction(services, d) }
func (d *Document) Busy() bool {
	return d.loading || d.edit.Busy || d.transaction.busy || d.transfer.Busy || d.export.Busy
}
func (d *Document) HasTransaction() bool { return dataTransactionActive(d) }
func (s *State) ExportJSONL(services ui.Services, d *Document) {
	if d == nil || d.closed || d.readRetired || d.Busy() || d.HasDraft() || d.HasTransaction() || s.savePath == nil || s.writeStream == nil {
		return
	}
	export := s.runtimeFor(d).JSONL
	if export == nil {
		return
	}
	path, e := s.savePath(d.table + ".jsonl")
	if e != nil || path == "" {
		if e != nil {
			d.err = e.Error()
		}
		return
	}
	write, o := s.writeStream, d.object
	ctx, cancel := context.WithCancel(context.Background())
	d.export.Cancel = cancel
	// Reuse the existing export request channel and consume lifecycle below.
	s.startJSONL(services, d, ctx, func() error { return write(path, func(w io.Writer) error { return export(ctx, o, w) }) })
}

func (s *State) startJSONL(services ui.Services, d *Document, ctx context.Context, work func() error) {
	ch := make(chan mysqlDataExportResult, 1)
	d.export.Busy = true
	d.export.Results = ch
	go func() {
		e := work()
		if e == nil {
			e = ctx.Err()
		}
		ch <- mysqlDataExportResult{err: e}
		services.Invalidate()
	}()
}

func (s *State) sessionFor(d *Document) Session {
	if d != nil && s.deps.SessionFor != nil {
		return s.deps.SessionFor(d.profileID)
	}
	return s.session()
}
func (s *State) runtimeFor(d *Document) Runtime {
	if d != nil && d.boundRuntime != nil {
		return *d.boundRuntime
	}
	if d != nil && s.deps.RuntimeFor != nil {
		return s.deps.RuntimeFor(d.profileID)
	}
	return s.runtime()
}
func (s *State) consumeFor(d *Document) bool {
	return d.profileID == s.session().ProfileID || s.guardDoc == d
}
func (d *Document) portable() bool { return d.object.Schema != "" }

func (d *Document) supportsExportFormat(format string) bool {
	if len(d.exportFormats) == 0 {
		return true
	}
	for _, allowed := range d.exportFormats {
		if strings.EqualFold(format, allowed) {
			return true
		}
	}
	return false
}
func (d *Document) keysetEnabled() bool { return d.object.ProfileID == 0 || d.capabilities.Keyset }
func (d *Document) editable() bool {
	return !d.readRetired && d.objectKind != "视图" && d.page != nil && (d.page.Editable == nil || *d.page.Editable) && (d.object.ProfileID == 0 || d.capabilities.EditableTables)
}

func (d *Document) allowsDataEdit(kind string) bool {
	if d.boundRuntime == nil || d.boundRuntime.EditKinds == nil {
		return kind == "insert" || kind == "update" || kind == "delete"
	}
	return slices.Contains(d.boundRuntime.EditKinds, kind)
}
func (d *Document) filterChoices() []string {
	if len(d.filterModes) == 0 {
		return mysqlFilterModeChoices
	}
	out := make([]string, 0, len(d.filterModes))
	for _, mode := range d.filterModes {
		out = append(out, mysqlDataFilterModeLabel(mode))
	}
	return out
}
func (d *Document) defaultFilter() string { return d.filterChoices()[0] }
func (d *Document) supportsFilter(mode string) bool {
	if len(d.filterModes) == 0 {
		return true
	}
	for _, m := range d.filterModes {
		if mode == m {
			return true
		}
	}
	return false
}
func (s *State) VerifySave(services ui.Services, d *Document) { s.verifyMySQLDataSave(services, d) }

func (s *State) canUseSession(d *Document) bool {
	if d == nil || d.closed {
		return false
	}
	snapshot := s.sessionFor(d)
	return snapshot.Connected && snapshot.ProfileID == d.profileID && (d.generation == 0 || d.generation == snapshot.Generation)
}

func typedKindLabel(kind contract.CellKind) string {
	switch kind {
	case contract.Null:
		return "NULL"
	case contract.Integer:
		return "整数"
	case contract.Decimal:
		return "精确小数"
	case contract.Floating:
		return "浮点"
	case contract.Boolean:
		return "布尔"
	case contract.Binary:
		return "二进制"
	case contract.JSON:
		return "JSON"
	case contract.DateTime:
		return "日期时间"
	}
	return "文本"
}
func typedKind(label string) contract.CellKind {
	for _, kind := range []contract.CellKind{contract.Null, contract.Text, contract.Integer, contract.Decimal, contract.Floating, contract.Boolean, contract.Binary, contract.JSON, contract.DateTime} {
		if typedKindLabel(kind) == label {
			return kind
		}
	}
	return contract.Text
}
func (s *State) typedEditorChoice(c *ui.Context, d *Document, e *mysqlCellEditor) {
	kinds := s.runtimeFor(d).EditValueKinds
	if !d.portable() && kinds == nil {
		return
	}
	choices := []string{"NULL", "文本", "整数", "精确小数", "浮点", "布尔", "二进制", "JSON", "日期时间"}
	if kinds != nil {
		choices = make([]string, 0, len(kinds))
		for _, kind := range kinds {
			choices = append(choices, typedKindLabel(kind))
		}
	}
	mysqlDataRuleSelect(c, &e.Kind, choices, locales.Text).Label(locales.Text("值类型")).Width(150)
}

func (s *State) filterColumns(d *Document, columns []string) []string {
	allowed := s.runtimeFor(d).FilterColumns
	if allowed == nil {
		return columns
	}
	out := []string{}
	for _, name := range columns {
		if slices.Contains(allowed, name) {
			out = append(out, name)
		}
	}
	return out
}
func (s *State) sortable(d *Document, name string) bool {
	allowed := s.runtimeFor(d).SortColumns
	return allowed == nil || slices.Contains(allowed, name)
}

// Reopen requests a fresh document only after the original page executor has
// acknowledged retirement and while the original public session is still active.
func (s *State) CanReopen(d *Document) bool {
	return d != nil && d.readRetired && !d.Busy() && !d.HasDraft() && !d.HasTransaction() &&
		s.canUseSession(d) && d.profileID == s.session().ProfileID && s.deps.ReopenDocument != nil
}
func (s *State) Reopen(services ui.Services, d *Document) {
	if s.CanReopen(d) {
		s.deps.ReopenDocument(services, d)
	}
}
func (d *Document) Generation() uint64 { return d.generation }

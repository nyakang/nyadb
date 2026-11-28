package manticore

import (
	"context"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type dataLease struct {
	session                        *Session
	object                         contract.ObjectRef
	primary                        *queryLease
	ctx                            context.Context
	cancel                         context.CancelFunc
	mu                             sync.Mutex
	active                         sync.WaitGroup
	closed, planning, writeBlocked bool
	plan                           *dataWritePlan
	grants                         map[string]*dataGrant
	pageSequence                   uint64
	done                           chan struct{}
}

func (s *Session) ReserveDataDocument(object contract.ObjectRef) (contract.DataDocumentLease, error) {
	if e := s.checkNamespace(object.Namespace()); e != nil {
		return nil, e
	}
	if object.Kind != "TABLE" || !validName(object.Name) {
		return nil, contract.ErrUnsupported
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, contract.ErrRevoked
	}
	if len(s.dataLeases) >= 32 || s.nextOwner == ^uint64(0) {
		return nil, contract.ErrSQLSessionLimit
	}
	s.nextOwner++
	ctx, cancel := context.WithCancel(s.ctx)
	d := &dataLease{session: s, object: object, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	d.primary = s.ownerLocked(ctx, s.nextOwner, s.dataSlots)
	s.dataLeases[d] = true
	return d, nil
}
func (s *Session) DataDocumentCapabilities() contract.Capabilities {
	return contract.Capabilities{Metadata: true, Count: true, Export: true, JSONL: true, EditableTables: true}
}
func (d *dataLease) Runtime() contract.DataRuntime {
	return contract.DataRuntime{Capabilities: d.session.DataDocumentCapabilities(), PageFailureRequiresReopen: true,
		Loader: d.load, AdvancedLoader: d.page, Count: d.count, TableMetadata: d.metadata, TableInfo: d.info,
		Exporter: d.export, JSONL: d.jsonl, ExportFormats: []string{"TXT", "CSV", "TSV", "JSON", "XML", "HTML"},
		RowReader: d.row, PreviewEdits: d.previewEdits, SubmitEdits: d.submitEdits,
		EditKinds: []string{"insert", "update", "delete"}, PreviewEditKinds: []string{"insert", "update", "delete"},
		EditValueKinds: []contract.CellKind{contract.Text, contract.Integer, contract.Floating, contract.Boolean, contract.JSON},
		NativeReceipts: true, SequentialWrites: true, InsertOmittedValues: true,
		MutationPresentation: &contract.DataMutationPresentation{Title: "确认 Manticore 数据写入", Hint: "各语句独立执行，不提供批次事务或整行并发隔离。提交前检查原始行；标量属性参与条件，全文、JSON 和集合仅作提交前检查。省略字段使用原生默认值；未知或不完整回执保留草稿并禁止重放。", PreviewLabel: "Manticore 原生写入完整预览", HistoryLabel: "已确认的 Manticore 数据写入", CreateLabel: "新增记录", UpdateLabel: "更新记录", DeleteLabel: "删除记录"},
		ReceiptPresentation:  &contract.NativeWriteReceiptPresentation{ExportFilename: "manticore-write-receipt.json"},
		FilterModes:          []string{"equal", "not_equal", "less", "greater"},
		ReadScopeHint:        "Manticore 分页和导出读取当前可见数据；跨次读取不提供一致快照。全文字段不支持普通属性筛选。",
	}
}
func (d *dataLease) identity(id int64, database, table string) error {
	if id != d.object.ProfileID || database != d.object.Catalog || table != d.object.Name {
		return connections.ErrInvalid
	}
	return d.session.checkNamespace(d.object.Namespace())
}
func (d *dataLease) request(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, connections.ErrInvalid
	}
	if e := ctx.Err(); e != nil {
		return nil, nil, e
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, nil, contract.ErrRevoked
	}
	d.active.Add(1)
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(d.ctx, cancel)
	return child, func() { stop(); cancel(); d.active.Done() }, nil
}
func (d *dataLease) auxiliary() (*queryLease, error) {
	s := d.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || d.ctx.Err() != nil {
		return nil, contract.ErrRevoked
	}
	if s.nextOwner == ^uint64(0) {
		return nil, contract.ErrSQLSessionLimit
	}
	s.nextOwner++
	return s.ownerLocked(d.ctx, s.nextOwner, s.metadataSlots), nil
}
func (d *dataLease) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		<-d.done
		return nil
	}
	d.closed = true
	d.cancel()
	d.mu.Unlock()
	d.primary.Release()
	d.active.Wait()
	d.mu.Lock()
	if d.plan != nil {
		d.plan.releaseLocked()
		d.plan = nil
	}
	d.grants = nil
	d.mu.Unlock()
	d.session.mu.Lock()
	delete(d.session.dataLeases, d)
	d.session.mu.Unlock()
	close(d.done)
	return nil
}
func (d *dataLease) metadata(ctx context.Context, id int64, database, table string) (*contract.TableStructure, error) {
	if e := d.identity(id, database, table); e != nil {
		return nil, e
	}
	child, done, e := d.request(ctx)
	if e != nil {
		return nil, e
	}
	defer done()
	return d.table(child)
}
func (d *dataLease) table(ctx context.Context) (*contract.TableStructure, error) {
	m, e := d.session.tableMetadata(ctx, d.object, false)
	if e != nil {
		return nil, e
	}
	// RT rows have the observed native id/attribute semantics. Percolate and
	// distributed tables expose different data schemas and need their own path.
	if m.Engine != "rt" || len(m.Columns) == 0 || len(m.Columns) > 512 {
		return nil, contract.ErrUnsupported
	}
	return m, nil
}

var _ contract.DataDocumentsProvider = (*Session)(nil)
var _ contract.DataDocumentFeaturesProvider = (*Session)(nil)

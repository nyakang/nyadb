package databend

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// A reservation is pure. All operation sessions, signed original observations
// and one-attempt write plans belong to this captured document and process.
type dataLease struct {
	mu                             sync.Mutex
	session                        *Session
	object                         contract.ObjectRef
	ctx                            context.Context
	cancel                         context.CancelFunc
	closed, planning, writeBlocked bool
	done                           chan struct{}
	work                           sync.WaitGroup
	active                         int
	key                            [32]byte
	plan                           *dataWritePlan
}

func (s *Session) ReserveDataDocument(o contract.ObjectRef) (contract.DataDocumentLease, error) {
	if err := s.checkObject(o); err != nil {
		return nil, err
	}
	if o.Kind != "table" && o.Kind != "view" {
		return nil, contract.ErrUnsupported
	}
	l := &dataLease{session: s, object: o, done: make(chan struct{})}
	if _, err := rand.Read(l.key[:]); err != nil {
		return nil, err
	}
	l.ctx, l.cancel = context.WithCancel(s.ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		l.cancel()
		return nil, contract.ErrRevoked
	}
	if len(s.dataLeases) >= 32 {
		l.cancel()
		return nil, contract.ErrSQLSessionLimit
	}
	if s.dataLeases == nil {
		s.dataLeases = map[*dataLease]bool{}
	}
	s.dataLeases[l] = true
	return l, nil
}

func (s *Session) DataDocumentCapabilities() contract.Capabilities {
	c := s.Capabilities()
	c.Count, c.FieldChunks, c.Export, c.JSONL, c.EditableTables = true, true, true, true, true
	return c
}

func (l *dataLease) Runtime() contract.DataRuntime {
	return contract.DataRuntime{
		Capabilities: l.session.DataDocumentCapabilities(), PageCancellationAcknowledged: true,
		NativeReceipts: true, SequentialWrites: true, InsertOmittedValues: true,
		EditKinds: []string{"insert", "update", "delete"}, PreviewEditKinds: []string{"insert", "update", "delete"},
		EditValueKinds: []contract.CellKind{contract.Null, contract.Text, contract.Integer, contract.Decimal, contract.Floating, contract.Boolean, contract.Binary, contract.DateTime, contract.JSON},
		PreviewEdits:   l.previewEdits, SubmitEdits: l.submitEdits,
		AdvancedLoader: l.page, RowReader: l.row, Chunk: l.chunk, Count: l.count,
		TableMetadata: l.metadata, TableInfo: l.info, Exporter: l.export, JSONL: l.jsonl,
		ExportFormats:        []string{"TXT", "CSV", "TSV", "JSON", "XML", "HTML"},
		FilterModes:          []string{"equal", "not_equal", "less", "greater", "like", "is_null", "is_not_null"},
		ReadScopeHint:        "Databend 分页读取当前可见数据，跨页不提供一致快照。",
		FieldReadScopeHint:   "显式读取原始记录的字段片段并检查变化，每次最多 1024 字符或字节。",
		MutationPresentation: &contract.DataMutationPresentation{Title: "确认 Databend 数据更改", Hint: "各语句独立执行，不提供批次事务。更改已有行会匹配原始字段并检查唯一匹配；检查不能保证跨语句并发隔离。不完整或未知回执保留草稿并禁止重放。", PreviewLabel: "Databend 原生写入完整预览", HistoryLabel: "已确认的 Databend 数据更改", CreateLabel: "新增记录", UpdateLabel: "更改原始记录", DeleteLabel: "删除原始记录"},
		ReceiptPresentation:  &contract.NativeWriteReceiptPresentation{ExportFilename: "databend-write-receipt.json"},
	}
}

func (l *dataLease) identity(id int64, db, table string) error {
	if id != l.object.ProfileID || db != l.object.Schema || table != l.object.Name {
		return connections.ErrInvalid
	}
	return l.session.checkObject(l.object)
}

func (l *dataLease) request(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, connections.ErrInvalid
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, nil, contract.ErrRevoked
	}
	if err := ctx.Err(); err != nil {
		l.mu.Unlock()
		return nil, nil, err
	}
	if l.active >= 8 {
		l.mu.Unlock()
		return nil, nil, contract.ErrSQLSessionLimit
	}
	l.active++
	l.work.Add(1)
	l.mu.Unlock()
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	return child, func() {
		stop()
		cancel()
		l.mu.Lock()
		l.active--
		l.mu.Unlock()
		l.work.Done()
	}, nil
}

func (l *dataLease) withOriginal(ctx context.Context, metadata bool, fn func(context.Context, *ManagedSession) error) (err error) {
	child, done, err := l.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	original, receipts, err := l.session.process.OpenSession(child, metadata)
	for _, r := range receipts {
		l.session.observed("data-open", r)
	}
	if err != nil {
		return err
	}
	defer func() {
		r, closeErr := original.Close()
		l.session.observed("data-close", r)
		err = errors.Join(err, closeErr)
	}()
	return fn(child, original)
}

func (l *dataLease) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		<-l.done
		return nil
	}
	l.closed = true
	l.cancel()
	l.mu.Unlock()
	l.work.Wait()
	l.mu.Lock()
	if l.plan != nil {
		l.plan.releaseLocked()
		l.plan = nil
	}
	clear(l.key[:])
	l.mu.Unlock()
	l.session.mu.Lock()
	delete(l.session.dataLeases, l)
	l.session.mu.Unlock()
	close(l.done)
	return nil
}

func (l *dataLease) metadata(ctx context.Context, id int64, db, table string) (*contract.TableStructure, error) {
	if err := l.identity(id, db, table); err != nil {
		return nil, err
	}
	child, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	m, err := l.session.TableMetadata(child, l.object)
	if err == nil {
		m.Editable = dataHasRowID(m)
	}
	return m, err
}

func (l *dataLease) info(ctx context.Context, id int64, db, table string) (*contract.TableInfo, error) {
	m, err := l.metadata(ctx, id, db, table)
	if err != nil {
		return nil, err
	}
	return &contract.TableInfo{Database: db, Name: table, Kind: m.Kind, Engine: m.Engine, Comment: m.Comment}, nil
}

func (l *dataLease) jsonl(ctx context.Context, o contract.ObjectRef, w io.Writer) error {
	if o != l.object {
		return connections.ErrInvalid
	}
	if w == nil {
		return connections.ErrInvalid
	}
	return l.exportJSONL(ctx, w)
}

func qualified(o contract.ObjectRef) string { return quoteName(o.Schema) + "." + quoteName(o.Name) }

var _ contract.DataDocumentsProvider = (*Session)(nil)
var _ contract.DataDocumentFeaturesProvider = (*Session)(nil)

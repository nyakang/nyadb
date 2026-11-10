package etcd

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var errDataRangeLost = errors.New("original etcd read range lost; explicitly reopen the data document")

// A data document owns separate native executors for page, count, fields,
// writes and export. Cancellation never selects a replacement session.
type dataLease struct {
	pageStarted                               bool
	access                                    *keyAccess
	accessFingerprint                         string
	accessScopes                              [5]bool
	session                                   *Session
	object                                    contract.ObjectRef
	ctx                                       context.Context
	cancel                                    context.CancelFunc
	mu, snapshotMu, pageMu, writeMu, exportMu sync.Mutex
	work                                      sync.WaitGroup
	active                                    int
	closed, rangeLost, uncertain              bool
	revision                                  int64
	epoch                                     uint64
	key                                       [32]byte
	scopes                                    [5]*kvLease
	done                                      chan struct{}
	closeErr                                  error
	plan                                      *dataPlan
	previewEpoch                              uint64
}

const (
	dataPageScope = iota
	dataCountScope
	dataFieldScope
	dataWriteScope
	dataExportScope
)

func dataCapabilities() contract.Capabilities {
	return contract.Capabilities{KeyValues: true, Metadata: true, Keyset: true, Count: true, FieldChunks: true, EditableTables: true, Export: true}
}
func (s *Session) DataDocumentCapabilities() contract.Capabilities { return dataCapabilities() }
func (s *Session) ReserveDataDocument(o contract.ObjectRef) (contract.DataDocumentLease, error) {
	if o != s.KVScope().Namespace.Object("keyspace", "keys") {
		return nil, contract.ErrUnsupported
	}
	l := &dataLease{session: s, object: o, done: make(chan struct{}), epoch: 1}
	if _, err := rand.Read(l.key[:]); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked || !s.client.Alive() {
		return nil, contract.ErrRevoked
	}
	if len(s.leases)+len(s.dataDocuments) >= 32 {
		return nil, contract.ErrKVLeaseLimit
	}
	l.ctx, l.cancel = context.WithCancel(s.ctx)
	for i := range l.scopes {
		ctx, cancel := context.WithCancel(l.ctx)
		l.scopes[i] = &kvLease{s: s, ctx: ctx, cancel: cancel}
	}
	if s.dataDocuments == nil {
		s.dataDocuments = map[*dataLease]bool{}
	}
	s.dataDocuments[l] = true
	return l, nil
}
func (l *dataLease) Runtime() contract.DataRuntime {
	return contract.DataRuntime{Capabilities: dataCapabilities(), PageFailureRequiresReopen: true,
		AdvancedLoader: l.page, Count: l.count, RowReader: l.row, Chunk: l.chunk, TableMetadata: l.metadata, TableInfo: l.info,
		Saver: l.save, PreviewEdits: l.preview, SubmitEdits: l.submit, Exporter: l.export,
		NativeReceipts: true, InsertOmittedValues: true, EditKinds: []string{"insert", "update", "delete"},
		PreviewEditKinds: []string{"insert", "update", "delete"}, EditValueKinds: []contract.CellKind{contract.Binary, contract.Integer},
		MutationPresentation: &contract.DataMutationPresentation{Title: "确认键更改", Hint: "请检查原始键、目标键、值和租约；确认后将原子提交此批次更改。", PreviewLabel: "键更改完整预览", HistoryLabel: "已确认的原生键更改", CreateLabel: "创建键", UpdateLabel: "更新键", DeleteLabel: "删除键"},
		ExportFormats:        []string{"csv", "json"}, WriteDescription: "/etcdserverpb.KV/Txn (one original atomic compare-and-swap)",
		FilterColumns: []string{}, SortColumns: []string{}, FilterModes: []string{},
		ReadScopeHint: "仅显示当前前缀内可读取的键；行数统计和导出使用相同授权范围。",
		ReadDescription: func(contract.DataOptions) string {
			return "/etcdserverpb.KV/Range (fixed original revision, byte key order)"
		},
	}
}
func (l *dataLease) check(id int64, catalog, name string) error {
	if id != l.object.ProfileID || catalog != l.object.Catalog || name != l.object.Name {
		return ErrInvalid
	}
	l.mu.Lock()
	closed, lost := l.closed, l.rangeLost
	l.mu.Unlock()
	if closed || !l.session.Alive() {
		return contract.ErrRevoked
	}
	if lost {
		return errDataRangeLost
	}
	return nil
}
func (l *dataLease) native(ctx context.Context, scope int, message proto.Message) (contract.KVResult, error) {
	if err := l.check(l.object.ProfileID, l.object.Catalog, l.object.Name); err != nil {
		return contract.KVResult{}, err
	}
	if _, err := l.captureAccess(ctx, scope); err != nil {
		return contract.KVResult{}, err
	}
	raw, err := protojson.Marshal(message)
	if err != nil {
		return contract.KVResult{}, err
	}
	lease := l.scopes[scope]
	// The method derives from the same validation as the original protocol.
	method, _, err := validateRequest(message)
	if err != nil {
		return contract.KVResult{}, err
	}
	plan, err := lease.Prepare(contract.KVRequest{Method: method, JSON: string(raw)})
	if err != nil {
		return contract.KVResult{}, err
	}
	return lease.Execute(ctx, plan)
}
func (l *dataLease) begin(ctx context.Context) (context.Context, func(), error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, nil, contract.ErrRevoked
	}
	if l.rangeLost {
		return nil, nil, errDataRangeLost
	}
	if l.active >= 8 {
		return nil, nil, contract.ErrKVBusy
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	l.work.Add(1)
	l.active++
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
func (l *dataLease) sign(raw []byte) string {
	l.mu.Lock()
	key := l.key
	l.mu.Unlock()
	h := hmac.New(sha256.New, key[:])
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

type dataEnvelope struct {
	Body json.RawMessage `json:"body"`
	MAC  string          `json:"mac"`
}

func (l *dataLease) encode(v any) []byte {
	body, _ := json.Marshal(v)
	raw, _ := json.Marshal(dataEnvelope{Body: body, MAC: l.sign(body)})
	return raw
}
func (l *dataLease) decode(raw []byte, v any) error {
	var envelope dataEnvelope
	if err := jsondocument.Decode(raw, &envelope, 64<<10); err != nil {
		return ErrInvalid
	}
	if !hmac.Equal([]byte(envelope.MAC), []byte(l.sign(envelope.Body))) {
		return ErrInvalid
	}
	if err := jsondocument.Decode(envelope.Body, v, 64<<10); err != nil {
		return ErrInvalid
	}
	return nil
}
func (l *dataLease) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		<-l.done
		return l.closeErr
	}
	l.closed = true
	if l.plan != nil {
		l.plan.releaseLocked()
	}
	l.cancel()
	l.mu.Unlock()
	var err error
	for _, scope := range l.scopes {
		err = errors.Join(err, scope.Close())
	}
	l.work.Wait()
	l.session.mu.Lock()
	delete(l.session.dataDocuments, l)
	l.session.mu.Unlock()
	l.mu.Lock()
	l.revision = 0
	l.access = nil
	l.accessFingerprint = ""
	l.epoch++
	l.key = [32]byte{}
	l.closeErr = err
	l.mu.Unlock()
	close(l.done)
	return err
}

var _ contract.DataDocumentsProvider = (*Session)(nil)

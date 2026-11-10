package etcd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type preparedDataEdit struct {
	edit          contract.DataEdit
	before, after contract.KVEntry
	observation   dataObservation
	rename        bool
}

// These values belong exclusively to the original document. Snapshot hands
// out deep copies; neither a modified copy nor a second submission authorizes I/O.
type dataPlan struct {
	owner    *dataLease
	closed   bool
	request  *pb.TxnRequest
	prepared []preparedDataEdit
	snapshot contract.DataMutationSnapshot
}

func (p *dataPlan) Snapshot() contract.DataMutationSnapshot {
	p.owner.mu.Lock()
	defer p.owner.mu.Unlock()
	if p.closed || p.owner.closed || p.owner.plan != p {
		return contract.DataMutationSnapshot{}
	}
	return contract.CloneDataMutationSnapshot(p.snapshot)
}
func (p *dataPlan) releaseLocked() {
	p.closed = true
	p.request, p.prepared = nil, nil
	p.snapshot = contract.DataMutationSnapshot{}
	if p.owner.plan == p {
		p.owner.plan = nil
	}
}
func (p *dataPlan) Close() error {
	p.owner.mu.Lock()
	defer p.owner.mu.Unlock()
	p.releaseLocked()
	return nil
}

func dataHash(value []byte) string {
	h := sha256.Sum256(value)
	return hex.EncodeToString(h[:])
}

func (l *dataLease) validateSnapshot(s *contract.RowSnapshot) (dataObservation, error) {
	var o dataObservation
	if s == nil || l.validateRef(s.Ref) != nil || l.decode([]byte(s.Metadata), &o) != nil {
		return o, ErrInvalid
	}
	if !bytes.Equal(o.Key, s.Ref.Values[0]) || o.CreateRevision <= 0 || o.ModRevision < o.CreateRevision || o.Version <= 0 || len(o.ValueHash) != 64 {
		return o, ErrInvalid
	}
	want := []string{"key", "value", "create_revision", "mod_revision", "version", "lease"}
	if !slices.Equal(s.Columns, want) {
		return o, ErrInvalid
	}
	hashes := []string{dataHash(o.Key), o.ValueHash}
	for _, n := range []int64{o.CreateRevision, o.ModRevision, o.Version, o.Lease} {
		hashes = append(hashes, dataHash([]byte(strconv.FormatInt(n, 10))))
	}
	switch s.Mode {
	case "values":
		if len(s.Original) != 6 || len(s.Fingerprints) != 0 {
			return o, ErrInvalid
		}
		for i, cell := range s.Original {
			if cell.Truncated || (i < 2 && (cell.Kind != contract.Binary || cell.Text != "")) || (i >= 2 && (cell.Kind != contract.Integer || len(cell.Bytes) != 0)) {
				return o, ErrInvalid
			}
			raw := []byte(cell.Text)
			if i < 2 {
				raw = cell.Bytes
			}
			if dataHash(raw) != hashes[i] {
				return o, ErrInvalid
			}
		}
	case "fingerprints":
		if len(s.Original) != 0 || len(s.Fingerprints) != 6 {
			return o, ErrInvalid
		}
		for i, f := range s.Fingerprints {
			if f == nil || *f != hashes[i] {
				return o, ErrInvalid
			}
		}
	default:
		return o, ErrInvalid
	}
	return o, nil
}

func dataBytes(v contract.DataValue) ([]byte, error) {
	if v.Null || v.Default || v.Kind != contract.Binary || v.Text != "" || len(v.Bytes) > contract.DataEditMaxBytes {
		return nil, contract.ErrUnsupported
	}
	return bytes.Clone(v.Bytes), nil
}
func dataLeaseID(v contract.DataValue) (int64, error) {
	if v.Null || v.Default || v.Binary || v.Kind != contract.Integer || len(v.Bytes) != 0 {
		return 0, contract.ErrUnsupported
	}
	n, err := strconv.ParseInt(v.Text, 10, 64)
	if err != nil || strconv.FormatInt(n, 10) != v.Text {
		return 0, ErrInvalid
	}
	return n, nil
}

// Validate every payload, key overlap and native operation count before opening
// a channel. Prefix bytes are separate from the qualified object identity.
func (l *dataLease) prepareDataEdits(edits []contract.DataEdit) ([]preparedDataEdit, error) {
	if len(edits) == 0 || len(edits) > 42 {
		return nil, ErrBounds
	}
	owned := contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: edits}).Edits
	prepared := make([]preparedDataEdit, 0, len(owned))
	touched := map[string]bool{}
	operations, size := 0, 0
	for _, e := range owned {
		if e.GraphEndpoints != nil {
			return nil, contract.ErrUnsupported
		}
		p := preparedDataEdit{edit: e}
		if e.Kind == "insert" {
			if e.Original != nil {
				return nil, ErrInvalid
			}
			operations += 3 // destination comparison, put, exact saved Range
		} else if e.Kind == "update" || e.Kind == "delete" {
			o, err := l.validateSnapshot(e.Original)
			if err != nil {
				return nil, err
			}
			p.observation = o
			p.before = contract.KVEntry{Key: bytes.Clone(o.Key), CreateRevision: o.CreateRevision, ModRevision: o.ModRevision, Version: o.Version, Lease: o.Lease}
			p.after = p.before.Clone()
			operations += 6 // five comparisons and delete
			if e.Kind == "update" {
				operations++
			}
			if touched[string(o.Key)] {
				return nil, ErrInvalid
			}
			touched[string(o.Key)] = true
		} else {
			return nil, contract.ErrUnsupported
		}
		if e.Kind == "delete" && len(e.Values) != 0 || e.Kind == "update" && len(e.Values) == 0 {
			return nil, ErrInvalid
		}
		for field, v := range e.Values {
			switch field {
			case "key", "value":
				b, err := dataBytes(v)
				if err != nil {
					return nil, err
				}
				size += len(b)
				if field == "key" {
					p.after.Key = b
				} else {
					p.after.Value = b
				}
			case "lease":
				n, err := dataLeaseID(v)
				if err != nil {
					return nil, err
				}
				p.after.Lease = n
			default:
				return nil, contract.ErrUnsupported
			}
		}
		if e.Kind != "delete" {
			if !validKey(p.after.Key) || !bytes.HasPrefix(p.after.Key, l.session.prefix) {
				return nil, ErrInvalid
			}
			p.rename = e.Kind == "update" && !bytes.Equal(p.before.Key, p.after.Key)
			if e.Kind == "insert" || p.rename {
				if touched[string(p.after.Key)] {
					return nil, ErrInvalid
				}
				touched[string(p.after.Key)] = true
				if p.rename {
					operations += 2
				} // destination comparison and source delete
			}
		}
		if operations > 128 || size > requestLimit {
			return nil, ErrBounds
		}
		prepared = append(prepared, p)
	}
	return prepared, nil
}

func (l *dataLease) preview(ctx context.Context, id int64, catalog, name string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
	if err := l.check(id, catalog, name); err != nil {
		return nil, err
	}
	prepared, err := l.prepareDataEdits(edits)
	if err != nil {
		return nil, err
	}
	if !l.writeMu.TryLock() {
		return nil, contract.ErrKVBusy
	}
	defer l.writeMu.Unlock()
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	l.mu.Lock()
	if l.uncertain {
		l.mu.Unlock()
		return nil, contract.ErrDataCommitUncertain
	}
	l.previewEpoch++
	epoch := l.previewEpoch
	if l.plan != nil {
		l.plan.releaseLocked()
	}
	l.mu.Unlock()
	if err = l.checkWriteAccess(ctx, prepared); err != nil {
		return nil, err
	}
	request := &pb.TxnRequest{}
	for i := range prepared {
		p := &prepared[i]
		if p.edit.Kind != "insert" {
			e, err := l.readEntry(ctx, dataWriteScope, p.edit.Original.Ref)
			if err != nil {
				return nil, err
			}
			if len(e.Value) > contract.DataEditMaxBytes {
				return nil, contract.ErrDataEditLimit
			}
			if !reflect.DeepEqual(entryObservation(e), p.observation) {
				return nil, contract.ErrDataConflict
			}
			p.before = e
			if _, set := p.edit.Values["value"]; !set {
				p.after.Value = bytes.Clone(e.Value)
			}
			request.Compare = append(request.Compare, dataComparisons(e)...)
		}
		if p.edit.Kind == "insert" || p.rename {
			request.Compare = append(request.Compare, &pb.Compare{Key: bytes.Clone(p.after.Key), Target: pb.Compare_CREATE, TargetUnion: &pb.Compare_CreateRevision{CreateRevision: 0}})
		}
		if p.edit.Kind == "delete" || p.rename {
			request.Success = append(request.Success, &pb.RequestOp{Request: &pb.RequestOp_RequestDeleteRange{RequestDeleteRange: &pb.DeleteRangeRequest{Key: bytes.Clone(p.before.Key)}}})
		}
		if p.edit.Kind != "delete" {
			put := &pb.PutRequest{Key: bytes.Clone(p.after.Key), Value: bytes.Clone(p.after.Value), Lease: p.after.Lease}
			if p.edit.Kind == "update" && !p.rename {
				// Preserve omitted fields natively. Keep the complete expected
				// after image above for verification of the genuine saved Range.
				if _, set := p.edit.Values["value"]; !set {
					put.IgnoreValue, put.Value = true, nil
				}
				if _, set := p.edit.Values["lease"]; !set {
					put.IgnoreLease, put.Lease = true, 0
				}
			}
			request.Success = append(request.Success, &pb.RequestOp{Request: &pb.RequestOp_RequestPut{RequestPut: put}},
				&pb.RequestOp{Request: &pb.RequestOp_RequestRange{RequestRange: &pb.RangeRequest{Key: bytes.Clone(p.after.Key)}}})
		}
	}
	if _, _, err = validateRequest(request); err != nil {
		return nil, err
	}
	if proto.Size(request) > requestLimit {
		return nil, ErrBounds
	}
	raw, err := protojson.Marshal(request)
	if err != nil {
		return nil, err
	}
	if _, err := l.scopes[dataWriteScope].Prepare(contract.KVRequest{Method: "/etcdserverpb.KV/Txn", JSON: string(raw)}); err != nil {
		return nil, err
	}
	p := &dataPlan{owner: l, request: request, prepared: prepared, snapshot: contract.DataMutationSnapshot{Object: l.object, NativeJSON: string(raw)}}
	for _, e := range prepared {
		p.snapshot.Edits = append(p.snapshot.Edits, e.edit)
		p.snapshot.Items = append(p.snapshot.Items, contract.DataMutationItem{Kind: e.edit.Kind, EntityID: "0x" + hex.EncodeToString(e.after.Key)})
	}
	p.snapshot = contract.CloneDataMutationSnapshot(p.snapshot)
	l.mu.Lock()
	defer l.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if l.closed || l.rangeLost || l.uncertain || l.previewEpoch != epoch {
		return nil, contract.ErrRevoked
	}
	l.plan = p
	return p, nil
}

func dataComparisons(e contract.KVEntry) []*pb.Compare {
	return []*pb.Compare{
		{Key: bytes.Clone(e.Key), Target: pb.Compare_CREATE, TargetUnion: &pb.Compare_CreateRevision{CreateRevision: e.CreateRevision}},
		{Key: bytes.Clone(e.Key), Target: pb.Compare_MOD, TargetUnion: &pb.Compare_ModRevision{ModRevision: e.ModRevision}},
		{Key: bytes.Clone(e.Key), Target: pb.Compare_VERSION, TargetUnion: &pb.Compare_Version{Version: e.Version}},
		{Key: bytes.Clone(e.Key), Target: pb.Compare_LEASE, TargetUnion: &pb.Compare_Lease{Lease: e.Lease}},
		{Key: bytes.Clone(e.Key), Target: pb.Compare_VALUE, TargetUnion: &pb.Compare_Value{Value: bytes.Clone(e.Value)}},
	}
}

func (l *dataLease) save(ctx context.Context, id int64, catalog, name string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
	p, err := l.preview(ctx, id, catalog, name, edits)
	if err != nil {
		return nil, err
	}
	defer p.Close()
	return l.submit(ctx, id, catalog, name, p)
}

func (l *dataLease) submit(ctx context.Context, id int64, catalog, name string, supplied contract.DataMutationPlan) (*contract.DataEditResult, error) {
	if err := l.check(id, catalog, name); err != nil {
		return nil, err
	}
	p, ok := supplied.(*dataPlan)
	if !ok || p == nil || p.owner != l {
		return nil, contract.ErrKVPlanUsed
	}
	if !l.writeMu.TryLock() {
		return nil, contract.ErrKVBusy
	}
	defer l.writeMu.Unlock()
	// A confirmed transaction starts a new read epoch. An already running page
	// must settle before that transition, without canceling count/field/export.
	if !l.pageMu.TryLock() {
		return nil, contract.ErrKVBusy
	}
	defer l.pageMu.Unlock()
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	l.mu.Lock()
	if p.closed || l.plan != p || l.closed || l.rangeLost || l.uncertain {
		l.mu.Unlock()
		return nil, contract.ErrKVPlanUsed
	}
	request, prepared := p.request, p.prepared
	p.releaseLocked()
	l.mu.Unlock()
	r, executeErr := l.native(ctx, dataWriteScope, request)
	raw, receiptErr := json.Marshal(r)
	out := &contract.DataEditResult{NativeJSON: string(raw), CommitUncertain: r.WriteOutcomeUnknown}
	if !r.Completed {
		if r.Attempted && executeErr == nil {
			out.CommitUncertain = true
		}
	} else if r.Reply == nil || r.ProjectionError != "" || r.Reply.Kind != "txn" || len(r.Native) == 0 {
		out.Incomplete = true
	} else if !r.Reply.Succeeded {
		return out, errors.Join(contract.ErrDataConflict, executeErr, receiptErr)
	} else {
		out.Outcomes, err = l.dataOutcomes(prepared, request, r)
		if err != nil {
			out.Incomplete = true
		} else {
			out.CommitConfirmed, out.RefreshRequired = true, true
			out.Applied = len(prepared)
		}
	}
	l.snapshotMu.Lock()
	l.mu.Lock()
	if out.CommitUncertain || out.Incomplete || receiptErr != nil {
		l.uncertain = true
	}
	if out.CommitConfirmed {
		l.revision = 0
		l.pageStarted = false
		l.epoch++
	}
	l.mu.Unlock()
	l.snapshotMu.Unlock()
	if out.CommitUncertain {
		err = errors.Join(err, contract.ErrDataCommitUncertain)
	}
	return out, errors.Join(executeErr, receiptErr, err)
}

func (l *dataLease) dataOutcomes(prepared []preparedDataEdit, request *pb.TxnRequest, result contract.KVResult) ([]contract.DataEditOutcome, error) {
	reply := result.Reply
	if len(reply.Operations) != len(request.Success) || result.Header.Revision <= 0 {
		return nil, ErrProtocol
	}
	out := make([]contract.DataEditOutcome, 0, len(prepared))
	i := 0
	for _, p := range prepared {
		o := contract.DataEditOutcome{Kind: p.edit.Kind, Original: p.edit.Original}
		if p.edit.Kind == "delete" || p.rename {
			if reply.Operations[i].Kind != "delete" || reply.Operations[i].Deleted != 1 {
				return nil, ErrProtocol
			}
			i++
		}
		if p.edit.Kind != "delete" {
			if reply.Operations[i].Kind != "put" {
				return nil, ErrProtocol
			}
			i++
			r := reply.Operations[i]
			i++
			if r.Kind != "range" || r.More || r.Count != 1 || len(r.Entries) != 1 {
				return nil, ErrProtocol
			}
			e := r.Entries[0]
			create, version := result.Header.Revision, int64(1)
			if p.edit.Kind == "update" && !p.rename {
				create, version = p.before.CreateRevision, p.before.Version+1
			}
			if !validDataEntry(e) || !bytes.Equal(e.Key, p.after.Key) || !bytes.Equal(e.Value, p.after.Value) || e.Lease != p.after.Lease || e.CreateRevision != create || e.ModRevision != result.Header.Revision || e.Version != version {
				return nil, ErrProtocol
			}
			o.Saved, o.Previews = l.snapshot(e, true), entryCells(e, true)
			for _, c := range dataColumns() {
				o.PreviewColumns = append(o.PreviewColumns, c.Name)
			}
		}
		out = append(out, o)
	}
	return out, nil
}

var _ contract.DataMutationPlan = (*dataPlan)(nil)

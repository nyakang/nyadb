package consul

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func dataColumns() []contract.StructureColumn {
	columns := []contract.StructureColumn{
		{Name: "key", Type: "text", DataType: "text", ValueKind: contract.Text, Key: "PRI"},
		{Name: "value", Type: "bytes", DataType: "bytes", ValueKind: contract.Binary, Nullable: true},
	}
	for _, name := range []string{"flags", "create_index", "modify_index", "lock_index"} {
		columns = append(columns, contract.StructureColumn{Name: name, Type: "uint64", DataType: "uint64", ValueKind: contract.Integer, Nullable: true, Generated: name != "flags"})
	}
	columns = append(columns, contract.StructureColumn{Name: "session", Type: "uuid", DataType: "uuid", ValueKind: contract.Text, Nullable: true, Generated: true})
	for i := range columns {
		columns[i].Position = int64(i + 1)
	}
	return columns
}

func dataCells(entry contract.KVHTTPEntry, preview bool) []contract.DataCell {
	values := []contract.Cell{{Kind: contract.Text, Text: entry.Key}, {Kind: contract.Binary, Bytes: entry.Value}}
	if !entry.ValuePresent || entry.ValueNull {
		values[1] = contract.Cell{Kind: contract.Null}
	}
	fields := []string{"Flags", "CreateIndex", "ModifyIndex", "LockIndex"}
	for i, number := range []uint64{entry.Flags, entry.CreateIndex, entry.ModifyIndex, entry.LockIndex} {
		cell := contract.Cell{Kind: contract.Integer, Text: strconv.FormatUint(number, 10)}
		if !slices.Contains(entry.PresentFields, fields[i]) {
			cell = contract.Cell{Kind: contract.Null}
		}
		values = append(values, cell)
	}
	session := contract.Cell{Kind: contract.Text, Text: entry.Session}
	if !slices.Contains(entry.PresentFields, "Session") || entry.SessionNull {
		session = contract.Cell{Kind: contract.Null}
	}
	values = append(values, session)
	cells := make([]contract.DataCell, len(values))
	for i := range values {
		cell := values[i]
		if preview && cell.Kind == contract.Text && len(cell.Text) > 1024 {
			end := 1024
			for end > 0 && !utf8.RuneStart(cell.Text[end]) {
				end--
			}
			cell.Text, cell.Truncated = cell.Text[:end], true
		}
		if preview && cell.Kind == contract.Binary && len(cell.Bytes) > 1024 {
			cell.Bytes = cell.Bytes[:1024]
			cell.Truncated = true
		}
		cell.Bytes = bytes.Clone(cell.Bytes)
		cells[i] = contract.DataCell{Text: cell.Display(), Null: cell.Kind == contract.Null, Truncated: cell.Truncated, Value: &cell}
	}
	return cells
}

type dataObservation struct {
	Object                                     contract.ObjectRef
	Epoch                                      uint64
	Key, ValueHash                             string
	Flags, CreateIndex, ModifyIndex, LockIndex string
	Session                                    string
	PresentFields                              []string
	ValuePresent, ValueNull                    bool
	SessionNull                                bool
	HTTPIndex                                  *string
	ACLFiltered                                *bool
}

func (l *dataLease) snapshot(entry contract.KVHTTPEntry, metadata contract.KVHTTPMetadata) *contract.RowSnapshot {
	l.mu.Lock()
	epoch := l.epoch
	l.mu.Unlock()
	return l.snapshotAt(entry, metadata, epoch)
}

func (l *dataLease) snapshotAt(entry contract.KVHTTPEntry, metadata contract.KVHTTPMetadata, epoch uint64) *contract.RowSnapshot {
	hash := sha256.Sum256(entry.Value)
	observation := dataObservation{Object: l.object, Epoch: epoch, Key: entry.Key, ValueHash: hex.EncodeToString(hash[:]),
		Flags: strconv.FormatUint(entry.Flags, 10), CreateIndex: strconv.FormatUint(entry.CreateIndex, 10), ModifyIndex: strconv.FormatUint(entry.ModifyIndex, 10), LockIndex: strconv.FormatUint(entry.LockIndex, 10),
		Session: entry.Session, SessionNull: entry.SessionNull, PresentFields: slices.Clone(entry.PresentFields), ValuePresent: entry.ValuePresent, ValueNull: entry.ValueNull, ACLFiltered: metadata.ACLFiltered}
	if metadata.Index != nil {
		v := strconv.FormatUint(*metadata.Index, 10)
		observation.HTTPIndex = &v
	}
	raw := l.encode(observation)
	ref := contract.RowRef{Object: l.object, Database: l.object.Catalog, Table: l.object.Name, Columns: []string{"_consul_key_identity"}, Values: [][]byte{raw}, TypedValues: []contract.Cell{{Kind: contract.Text, Text: entry.Key}}}
	snapshot := &contract.RowSnapshot{Mode: "fingerprints", Ref: ref, Metadata: string(raw)}
	for i, cell := range dataCells(entry, false) {
		snapshot.Columns = append(snapshot.Columns, dataColumns()[i].Name)
		if cell.Null {
			snapshot.Fingerprints = append(snapshot.Fingerprints, nil)
			continue
		}
		raw := []byte(cell.Value.Text)
		if cell.Value.Kind == contract.Binary {
			raw = cell.Value.Bytes
		}
		h := sha256.Sum256(raw)
		fingerprint := hex.EncodeToString(h[:])
		snapshot.Fingerprints = append(snapshot.Fingerprints, &fingerprint)
	}
	return snapshot
}

func (l *dataLease) validateRef(ref contract.RowRef) (string, error) {
	observation, err := l.refObservation(ref)
	return observation.Key, err
}

func (l *dataLease) refObservation(ref contract.RowRef) (dataObservation, error) {
	var observation dataObservation
	if ref.Object != l.object || ref.Database != l.object.Catalog || ref.Table != l.object.Name || !slices.Equal(ref.Columns, []string{"_consul_key_identity"}) || len(ref.Values) != 1 {
		return observation, ErrInvalid
	}
	if err := l.decode(ref.Values[0], &observation); err != nil || observation.Object != l.object {
		return observation, ErrInvalid
	}
	l.mu.Lock()
	epoch := l.epoch
	l.mu.Unlock()
	key := observation.Key
	if observation.Epoch == 0 || observation.Epoch > epoch || !validKey(key, false) || !bytes.HasPrefix([]byte(key), l.session.scope.Prefix) {
		return observation, ErrInvalid
	}
	if len(ref.TypedValues) != 1 || ref.TypedValues[0].Kind != contract.Text || ref.TypedValues[0].Truncated || len(ref.TypedValues[0].Bytes) != 0 || ref.TypedValues[0].Text != key {
		return observation, ErrInvalid
	}
	return observation, nil
}

func cloneDataRef(ref contract.RowRef) contract.RowRef {
	ref.Columns = slices.Clone(ref.Columns)
	ref.Values = slices.Clone(ref.Values)
	for i := range ref.Values {
		ref.Values[i] = bytes.Clone(ref.Values[i])
	}
	ref.TypedValues = slices.Clone(ref.TypedValues)
	for i := range ref.TypedValues {
		ref.TypedValues[i] = ref.TypedValues[i].Clone()
	}
	return ref
}

func (l *dataLease) metadata(ctx context.Context, id int64, catalog, name string) (*contract.TableStructure, error) {
	if err := l.check(id, catalog, name); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &contract.TableStructure{Object: l.object, Database: catalog, Name: name, Kind: "键", Engine: "Consul", Columns: dataColumns(), Comment: "Bounded live observations in the original visible prefix; no fixed MVCC snapshot."}, nil
}

func (l *dataLease) info(ctx context.Context, id int64, catalog, name string) (*contract.TableInfo, error) {
	m, err := l.metadata(ctx, id, catalog, name)
	if err != nil {
		return nil, err
	}
	return &contract.TableInfo{Database: catalog, Name: name, Kind: m.Kind, Engine: m.Engine, Comment: m.Comment}, nil
}

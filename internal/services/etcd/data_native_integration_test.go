package etcd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"github.com/nyakang/nyadb/internal/services/connections"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

func nativeDataDocument(t *testing.T) (*Session, *dataLease, contract.KVRequestLease, []byte) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_DATA_IT") != "1" {
		t.Skip("requires owned native etcd data fixture")
	}
	p, secret := sessionProfile(t)
	prefix := append([]byte{0, 255}, nativeKey(t)...)
	p.EtcdOptions.PrefixEncoding, p.EtcdOptions.Prefix = "base64", base64.StdEncoding.EncodeToString(prefix)
	raw, err := OpenSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	t.Cleanup(func() { _ = s.Close() })
	doc, err := s.ReserveDataDocument(s.KVScope().Namespace.Object("keyspace", "keys"))
	if err != nil {
		t.Fatal(err)
	}
	return s, doc.(*dataLease), reserveNativeKV(t, s, 99), prefix
}

func TestEtcdNativeDataActualTxnReceiptFaultCancelAndOriginalClose(t *testing.T) {
	for _, mode := range []string{"lost", "cancel", "close", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			s, l, admin, prefix := nativeDataDocument(t)
			upstream := nativeClient(t)
			defer upstream.Close()
			key := append(bytes.Clone(prefix), 1)
			nativeKVDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("before")})
			row, err := l.row(context.Background(), s.ProfileID(), "kv", "keys", l.ref(key))
			if err != nil {
				t.Fatal(err)
			}
			client, fault := faultClient(t, upstream, "/etcdserverpb.KV/Txn", mode)
			s.opener = func(context.Context, connections.Profile, string) (*Client, error) { return client, nil }
			plan, err := l.preview(context.Background(), s.ProfileID(), "kv", "keys", []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: map[string]contract.DataValue{"value": {Kind: contract.Binary, Bytes: []byte("original-write")}}}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				result *contract.DataEditResult
				err    error
			}
			done := make(chan completion, 1)
			go func() { r, err := l.submit(ctx, s.ProfileID(), "kv", "keys", plan); done <- completion{r, err} }()
			waitSignal(t, fault.entered)
			if mode == "cancel" {
				cancel()
			}
			if mode == "close" {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			var got completion
			select {
			case got = <-done:
			case <-time.After(8 * time.Second):
				t.Fatal("original executor did not settle")
			}
			waitSignal(t, fault.done)
			if got.result == nil || got.result.CommitConfirmed || got.result.NativeJSON == "" || len(got.result.Outcomes) != 0 || fault.calls.Load() != 1 {
				t.Fatal("fault accepted/replayed original transaction", got.result, got.err)
			}
			if mode == "incomplete" {
				if !got.result.Incomplete || !l.uncertain {
					t.Fatal("incomplete original acknowledgement admitted")
				}
			} else if got.err == nil || !got.result.CommitUncertain || !l.uncertain {
				t.Fatal("lost native write confirmation", got.result, got.err)
			}
			actual := fault.receipt.(*pb.TxnResponse)
			if !actual.Succeeded || len(actual.Responses) != 2 || !bytes.Equal(actual.Responses[1].GetResponseRange().Kvs[0].Value, []byte("original-write")) {
				t.Fatal("fixture did not process real original atomic write")
			}
			observed := nativeDo(t, upstream, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse)
			if !bytes.Equal(observed.Kvs[0].Value, []byte("later-independent")) {
				t.Fatal("readback/recovery replayed original request")
			}
			if _, err := l.preview(context.Background(), s.ProfileID(), "kv", "keys", []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"key": {Kind: contract.Binary, Bytes: append(bytes.Clone(prefix), 9)}}}}); err == nil {
				t.Fatal("unknown write allowed further document writes")
			}
			if fault.calls.Load() != 1 {
				t.Fatal("unknown original request retried")
			}
		})
	}
}

func TestEtcdNativeDataAuxiliaryCancellationKeepsOriginalPageScope(t *testing.T) {
	for _, scope := range []int{dataCountScope, dataFieldScope, dataExportScope} {
		t.Run(strconv.Itoa(scope), func(t *testing.T) {
			s, l, admin, prefix := nativeDataDocument(t)
			for i := 0; i < 3; i++ {
				nativeKVDo(t, admin, &pb.PutRequest{Key: append(bytes.Clone(prefix), byte(i)), Value: []byte("original")})
			}
			page, err := l.page(context.Background(), s.ProfileID(), "kv", "keys", contract.DataOptions{Limit: 1})
			if err != nil {
				t.Fatal(err)
			}
			upstream := nativeClient(t)
			defer upstream.Close()
			client, fault := faultClient(t, upstream, "/etcdserverpb.KV/Range", "read-block")
			s.opener = func(context.Context, connections.Profile, string) (*Client, error) { return client, nil }
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				var err error
				switch scope {
				case dataCountScope:
					_, err = l.count(ctx, s.ProfileID(), "kv", "keys")
				case dataFieldScope:
					_, err = l.chunk(ctx, s.ProfileID(), "kv", "keys", "value", l.ref(append(bytes.Clone(prefix), 0)), 0, 1024)
				case dataExportScope:
					err = l.export(ctx, s.ProfileID(), "kv", "keys", contract.DataOptions{}, []string{"key"}, func([]contract.DataValue) error { return nil })
				}
				done <- err
			}()
			waitSignal(t, fault.entered)
			cancel()
			if err := <-done; err == nil {
				t.Fatal("canceled auxiliary request succeeded")
			}
			waitSignal(t, fault.done)
			if l.rangeLost || !s.Alive() {
				t.Fatal("auxiliary cancellation retired original page/session")
			}
			if next, err := l.page(context.Background(), s.ProfileID(), "kv", "keys", contract.DataOptions{Limit: 1, Offset: page.NextCursor.Offset, After: page.NextCursor}); err != nil || len(next.Rows) != 1 {
				t.Fatal("page scope lost after auxiliary cancellation", err)
			}
		})
	}
}

func TestEtcdNativeDataAtomicEditsExactReceiptsAndPreservedLease(t *testing.T) {
	s, l, admin, prefix := nativeDataDocument(t)
	lease := nativeKVDo(t, admin, &pb.LeaseGrantRequest{TTL: 90, ID: 9007199254740993}).Reply.Lease.ID
	defer nativeKVDo(t, admin, &pb.LeaseRevokeRequest{ID: lease})
	keys := make([][]byte, 4)
	for i := range keys {
		keys[i] = append(bytes.Clone(prefix), byte(i))
	}
	for i := 0; i < 3; i++ {
		nativeKVDo(t, admin, &pb.PutRequest{Key: keys[i], Value: []byte{0, byte(i), 255}, Lease: lease})
	}
	page, err := l.page(context.Background(), s.ProfileID(), "kv", "keys", contract.DataOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]*contract.DataRow, 3)
	for i := range rows {
		rows[i], err = l.row(context.Background(), s.ProfileID(), "kv", "keys", l.ref(keys[i]))
		if err != nil {
			t.Fatal(err)
		}
	}
	edits := []contract.DataEdit{
		{Kind: "update", Original: rows[0].Snapshot, Values: map[string]contract.DataValue{"value": {Kind: contract.Binary, Bytes: []byte{255, 0, 1}}}},
		{Kind: "update", Original: l.snapshot(contract.KVEntry{Key: keys[1], Value: rows[1].Values["value"].Bytes, CreateRevision: 1}, false)},
		{Kind: "delete", Original: rows[2].Snapshot},
		{Kind: "insert", Values: map[string]contract.DataValue{"key": {Kind: contract.Binary, Bytes: append(bytes.Clone(prefix), 4)}, "value": {Kind: contract.Binary, Bytes: []byte{0, 255}}}},
	}
	// Exercise both complete values and original page fingerprint observations.
	entry := nativeKVDo(t, admin, &pb.RangeRequest{Key: keys[1]}).Reply.Entries[0]
	edits[0].Original = l.snapshot(nativeKVDo(t, admin, &pb.RangeRequest{Key: keys[0]}).Reply.Entries[0], true)
	edits[1].Original = l.snapshot(entry, false)
	edits[1].Values = map[string]contract.DataValue{"key": {Kind: contract.Binary, Bytes: keys[3]}}
	plan, err := l.preview(context.Background(), s.ProfileID(), "kv", "keys", edits)
	if err != nil {
		t.Fatal(err)
	}
	before := plan.Snapshot()
	copy := plan.Snapshot()
	copy.Edits[0].Values["value"].Bytes[0] = 8
	copy.Items[0].Kind = "changed"
	if !reflect.DeepEqual(before, plan.Snapshot()) {
		t.Fatal("public copy changed original plan")
	}
	edits[0].Values["value"].Bytes[0] = 9
	saved, err := l.submit(context.Background(), s.ProfileID(), "kv", "keys", plan)
	if err != nil || !saved.CommitConfirmed || saved.CommitUncertain || saved.Incomplete || !saved.RefreshRequired || saved.Applied != 4 || len(saved.Outcomes) != 4 || saved.NativeJSON == "" {
		t.Fatal("native atomic receipt missing", saved, err)
	}
	if _, err := l.submit(context.Background(), s.ProfileID(), "kv", "keys", plan); err == nil {
		t.Fatal("original transaction replayed")
	}
	if !bytes.Equal(saved.Outcomes[0].Saved.Original[1].Bytes, []byte{255, 0, 1}) || saved.Outcomes[0].Saved.Original[5].Text != "9007199254740993" || !bytes.Equal(saved.Outcomes[1].Saved.Ref.Values[0], keys[3]) || !bytes.Equal(saved.Outcomes[1].Saved.Original[1].Bytes, []byte{0, 1, 255}) || saved.Outcomes[1].Saved.Original[4].Text != "1" {
		t.Fatal("saved binary bytes/rename/lease lost")
	}
	if len(nativeKVDo(t, admin, &pb.RangeRequest{Key: keys[1]}).Reply.Entries) != 0 || len(nativeKVDo(t, admin, &pb.RangeRequest{Key: keys[2]}).Reply.Entries) != 0 {
		t.Fatal("rename/delete not applied")
	}
	if _, err := l.page(context.Background(), s.ProfileID(), "kv", "keys", contract.DataOptions{Limit: 1, Offset: page.NextCursor.Offset, After: page.NextCursor}); err == nil {
		t.Fatal("confirmed mutation retained old cursor epoch")
	}
	if _, err := l.page(context.Background(), s.ProfileID(), "kv", "keys", contract.DataOptions{Limit: 10}); err != nil {
		t.Fatal("explicit fresh range after confirmed transaction", err)
	}
}

func TestEtcdNativeDataPreviewConflictsRollBackWholeOriginalBatch(t *testing.T) {
	for _, change := range []string{"value", "lease", "delete-recreate", "destination", "expired-lease"} {
		t.Run(change, func(t *testing.T) {
			s, l, admin, prefix := nativeDataDocument(t)
			a, b, destination := append(bytes.Clone(prefix), 0), append(bytes.Clone(prefix), 1), append(bytes.Clone(prefix), 2)
			lease := nativeKVDo(t, admin, &pb.LeaseGrantRequest{TTL: 60}).Reply.Lease.ID
			defer func() {
				if lease != 0 {
					nativeKVDo(t, admin, &pb.LeaseRevokeRequest{ID: lease})
				}
			}()
			nativeKVDo(t, admin, &pb.PutRequest{Key: a, Value: []byte("first")})
			nativeKVDo(t, admin, &pb.PutRequest{Key: b, Value: []byte("second"), Lease: lease})
			arow, _ := l.row(context.Background(), s.ProfileID(), "kv", "keys", l.ref(a))
			brow, _ := l.row(context.Background(), s.ProfileID(), "kv", "keys", l.ref(b))
			plan, err := l.preview(context.Background(), s.ProfileID(), "kv", "keys", []contract.DataEdit{{Kind: "update", Original: arow.Snapshot, Values: map[string]contract.DataValue{"value": {Kind: contract.Binary, Bytes: []byte("must-roll-back")}}}, {Kind: "update", Original: brow.Snapshot, Values: map[string]contract.DataValue{"key": {Kind: contract.Binary, Bytes: destination}}}})
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "value":
				nativeKVDo(t, admin, &pb.PutRequest{Key: b, Value: []byte("later"), Lease: lease})
			case "lease":
				nativeKVDo(t, admin, &pb.PutRequest{Key: b, IgnoreValue: true})
			case "delete-recreate":
				nativeKVDo(t, admin, &pb.DeleteRangeRequest{Key: b})
				nativeKVDo(t, admin, &pb.PutRequest{Key: b, Value: []byte("second"), Lease: lease})
			case "destination":
				nativeKVDo(t, admin, &pb.PutRequest{Key: destination, Value: []byte("owned-by-other")})
			case "expired-lease":
				nativeKVDo(t, admin, &pb.LeaseRevokeRequest{ID: lease})
				lease = 0
			}
			result, err := l.submit(context.Background(), s.ProfileID(), "kv", "keys", plan)
			if !errors.Is(err, contract.ErrDataConflict) || result == nil || result.CommitConfirmed || result.CommitUncertain || result.Applied != 0 || len(result.Outcomes) != 0 || result.NativeJSON == "" || l.uncertain {
				t.Fatal("failed comparison became commit/unknown", result, err)
			}
			if got := nativeKVDo(t, admin, &pb.RangeRequest{Key: a}).Reply.Entries[0]; !bytes.Equal(got.Value, []byte("first")) {
				t.Fatal("earlier mutation escaped atomic rollback")
			}
		})
	}
}

func TestEtcdNativeDataFixedRevisionLosslessExportAndMaximumKey(t *testing.T) {
	s, l, admin, prefix := nativeDataDocument(t)
	for i := 0; i < 70; i++ {
		nativeKVDo(t, admin, &pb.PutRequest{Key: append(bytes.Clone(prefix), byte(i)), Value: bytes.Repeat([]byte{0, byte(i), 255}, 500)})
	}
	maxKey := append(bytes.Clone(prefix), bytes.Repeat([]byte{77}, 4096-len(prefix))...)
	nativeKVDo(t, admin, &pb.PutRequest{Key: maxKey, Value: []byte("max-length")})
	nativeKVDo(t, admin, &pb.PutRequest{Key: append(bytes.Clone(prefix), 78), Value: []byte("next")})
	page, err := l.page(context.Background(), s.ProfileID(), "kv", "keys", contract.DataOptions{Limit: 71})
	if err != nil || len(page.Rows) != 71 || len(page.RowRefs[70].Values[0]) != 4096 || page.NextCursor == nil {
		t.Fatal("maximum native key page", err)
	}
	options := contract.DataOptions{Limit: 71, Offset: page.NextCursor.Offset, After: page.NextCursor}
	if next, err := l.page(context.Background(), s.ProfileID(), "kv", "keys", options); err != nil || len(next.Rows) != 1 {
		t.Fatal("maximum length key continuation", err)
	}
	nativeKVDo(t, admin, &pb.PutRequest{Key: append(bytes.Clone(prefix), 0), Value: []byte("later")})
	count := 0
	err = l.export(context.Background(), s.ProfileID(), "kv", "keys", contract.DataOptions{}, []string{"key", "value", "mod_revision"}, func(values []contract.DataValue) error {
		if values[0].Kind != contract.Binary || !values[0].Binary || values[2].Kind != contract.Integer {
			return ErrInvalid
		}
		if count < 70 && !bytes.Equal(values[1].Bytes, bytes.Repeat([]byte{0, byte(count), 255}, 500)) {
			return ErrInvalid
		}
		if count == 0 {
			nativeKVDo(t, admin, &pb.PutRequest{Key: append(bytes.Clone(prefix), 255), Value: []byte("insert-during-export")})
		}
		count++
		return nil
	})
	if err != nil || count != 72 {
		t.Fatal("export changed bytes or original MVCC membership", count, err)
	}
}
func TestEtcdNativeDataOriginalBinaryMVCCCountAndFieldScopes(t *testing.T) {
	s, l, admin, prefix := nativeDataDocument(t)
	lease := nativeKVDo(t, admin, &pb.LeaseGrantRequest{TTL: 60, ID: 9007199254740993}).Reply.Lease.ID
	defer nativeKVDo(t, admin, &pb.LeaseRevokeRequest{ID: lease})
	for i := 0; i < 9; i++ {
		key := append(bytes.Clone(prefix), byte(i))
		value := bytes.Repeat([]byte{0, byte(i), 255}, 500)
		nativeKVDo(t, admin, &pb.PutRequest{Key: key, Value: value, Lease: lease})
	}
	runtime := l.Runtime()
	options := contract.DataOptions{Object: l.object, Limit: 3}
	page, err := runtime.AdvancedLoader(context.Background(), s.ProfileID(), "kv", "keys", options)
	if err != nil || len(page.Rows) != 3 || !page.HasMore || page.LiveRead || !page.Rows[0][1].Value.Truncated || page.Rows[0][5].Value.Text != "9007199254740993" {
		t.Fatal("original binary page or exact lease changed", err)
	}
	oldNext := page.NextCursor
	count, err := runtime.Count(context.Background(), s.ProfileID(), "kv", "keys")
	if err != nil || count != 9 {
		t.Fatal("fixed original count", count, err)
	}
	key4 := append(bytes.Clone(prefix), byte(4))
	nativeKVDo(t, admin, &pb.PutRequest{Key: key4, Value: []byte("later")})
	nativeKVDo(t, admin, &pb.PutRequest{Key: append(bytes.Clone(prefix), byte(9)), Value: []byte("new")})
	seen := map[string]bool{}
	for {
		for i, ref := range page.RowRefs {
			if seen[string(ref.Values[0])] {
				t.Fatal("cursor repeated binary key")
			}
			seen[string(ref.Values[0])] = true
			if bytes.Equal(ref.Values[0], key4) && !bytes.Equal(page.Rows[i][1].Value.Bytes, bytes.Repeat([]byte{0, 4, 255}, 500)[:1024]) {
				t.Fatal("MVCC traversal changed to current revision")
			}
		}
		if !page.HasMore {
			break
		}
		options.After = page.NextCursor
		options.Offset = page.NextCursor.Offset
		page, err = runtime.AdvancedLoader(context.Background(), s.ProfileID(), "kv", "keys", options)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 9 {
		t.Fatal("original traversal changed membership", len(seen))
	}
	if count, err = runtime.Count(context.Background(), s.ProfileID(), "kv", "keys"); err != nil || count != 9 {
		t.Fatal("count switched revision", err)
	}
	ref := l.ref(append(bytes.Clone(prefix), byte(0)))
	row, err := runtime.RowReader(context.Background(), s.ProfileID(), "kv", "keys", ref)
	if err != nil || row.Snapshot.Mode != "fingerprints" || len(row.Values["value"].Bytes) != 1500 || row.Values["lease"].Text != "9007199254740993" {
		t.Fatal("complete original row lost native values", err)
	}
	var assembled []byte
	for offset := int64(0); offset < 1500; {
		chunk, err := runtime.Chunk(context.Background(), s.ProfileID(), "kv", "keys", "value", ref, offset, 1024)
		if err != nil || !chunk.Binary || chunk.TotalUnits != 1500 || chunk.NextOffset-offset > 1024 {
			t.Fatal("explicit bounded binary chunk", err)
		}
		part, err := hex.DecodeString(strings.TrimPrefix(chunk.Text, "0x"))
		if err != nil {
			t.Fatal(err)
		}
		assembled = append(assembled, part...)
		offset = chunk.NextOffset
	}
	if !bytes.Equal(assembled, row.Values["value"].Bytes) {
		t.Fatal("binary chunks changed original bytes")
	}
	// A healthy explicit refresh replaces only this document's read epoch;
	// ordinary authenticated paging above kept the original nine-key snapshot.
	refreshed, err := l.page(context.Background(), s.ProfileID(), "kv", "keys", contract.DataOptions{Limit: 3})
	if err != nil || len(refreshed.Rows) != 3 {
		t.Fatal("healthy refresh failed", err)
	}
	if count, err := l.count(context.Background(), s.ProfileID(), "kv", "keys"); err != nil || count != 10 {
		t.Fatal("explicit refresh retained original membership", count, err)
	}
	if _, err := l.page(context.Background(), s.ProfileID(), "kv", "keys", contract.DataOptions{Limit: 3, Offset: oldNext.Offset, After: oldNext}); err == nil || l.rangeLost {
		t.Fatal("old refresh epoch accepted or healthy range retired", err)
	}
	updated, err := l.page(context.Background(), s.ProfileID(), "kv", "keys", contract.DataOptions{Limit: 3, Offset: refreshed.NextCursor.Offset, After: refreshed.NextCursor})
	if err != nil || !bytes.Equal(updated.Rows[1][1].Value.Bytes, []byte("later")) {
		t.Fatal("refreshed paging did not observe external update", err)
	}
	foreign, err := s.ReserveDataDocument(l.object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = foreign.Runtime().AdvancedLoader(context.Background(), s.ProfileID(), "kv", "keys", options); err == nil {
		t.Fatal("foreign cursor entered new original lease")
	}
}
func TestEtcdNativeDataCompactionRetiresOriginalRangeAndExplicitReopen(t *testing.T) {
	s, l, admin, prefix := nativeDataDocument(t)
	for i := 0; i < 3; i++ {
		nativeKVDo(t, admin, &pb.PutRequest{Key: append(bytes.Clone(prefix), byte(i)), Value: []byte("original")})
	}
	options := contract.DataOptions{Object: l.object, Limit: 1}
	page, err := l.page(context.Background(), s.ProfileID(), "kv", "keys", options)
	if err != nil {
		t.Fatal(err)
	}
	later := nativeKVDo(t, admin, &pb.PutRequest{Key: append(bytes.Clone(prefix), byte(1)), Value: []byte("later")})
	if _, err = pb.NewKVClient(s.client.conn).Compact(s.client.authContext(context.Background()), &pb.CompactionRequest{Revision: later.Header.Revision}); err != nil {
		t.Fatal(err)
	}
	options.After = page.NextCursor
	options.Offset = page.NextCursor.Offset
	if _, err = l.page(context.Background(), s.ProfileID(), "kv", "keys", options); err == nil || !l.rangeLost || !s.Alive() {
		t.Fatal("compacted range silently restarted or revoked unrelated session", err)
	}
	if !bytes.Equal(page.Rows[0][1].Value.Bytes, []byte("original")) {
		t.Fatal("failed next page replaced original cache")
	}
	if _, err = l.count(context.Background(), s.ProfileID(), "kv", "keys"); err == nil {
		t.Fatal("retired range admitted auxiliary current count")
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.ReserveDataDocument(l.object)
	if err != nil {
		t.Fatal(err)
	}
	options.After = nil
	options.Offset = 0
	if _, err = fresh.Runtime().AdvancedLoader(context.Background(), s.ProfileID(), "kv", "keys", options); err != nil {
		t.Fatal("explicit fresh MVCC reservation failed", err)
	}
}

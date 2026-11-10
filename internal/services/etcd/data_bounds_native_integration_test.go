package etcd

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func TestEtcdNativeDataLargePagesUseBoundedRPCsAndOriginalRevision(t *testing.T) {
	s, l, admin, prefix := nativeDataDocument(t)
	const total, limit = 19, 17
	value := bytes.Repeat([]byte{0, 255, 1, 128}, contract.DataEditMaxBytes/4)
	for i := 0; i < total; i++ {
		nativeKVDo(t, admin, &pb.PutRequest{Key: append(bytes.Clone(prefix), byte(i)), Value: value})
	}
	if len(value)*limit <= responseLimit {
		t.Fatal("fixture does not exceed the original single-RPC response bound")
	}
	if count, err := l.count(context.Background(), s.ProfileID(), "kv", "keys"); err != nil || count != total {
		t.Fatal("count before first page", count, err)
	}
	page, err := l.page(context.Background(), s.ProfileID(), "kv", "keys", contract.DataOptions{Limit: limit})
	if err != nil || len(page.Rows) != limit || page.NextCursor == nil || page.CurrentCursor == nil {
		t.Fatal("large native page failed", err)
	}
	for i, row := range page.Rows {
		if !row[1].Truncated || len(row[1].Value.Bytes) != 1024 || !bytes.Equal(row[1].Value.Bytes, value[:1024]) {
			t.Fatal("large value preview changed", i)
		}
		observation, err := l.validateSnapshot(page.RowSnapshots[i])
		if err != nil || observation.ValueHash != dataHash(value) {
			t.Fatal("large value observation lost complete bytes", i, err)
		}
	}
	first := page.CurrentCursor
	nativeKVDo(t, admin, &pb.PutRequest{Key: append(bytes.Clone(prefix), 18), Value: []byte("later")})
	nativeKVDo(t, admin, &pb.PutRequest{Key: append(bytes.Clone(prefix), 19), Value: []byte("new")})
	next, err := l.page(context.Background(), s.ProfileID(), "kv", "keys", contract.DataOptions{Limit: limit, Offset: page.NextCursor.Offset, After: page.NextCursor})
	if err != nil || len(next.Rows) != 2 || next.HasMore || !bytes.Equal(next.Rows[1][1].Value.Bytes, value[:1024]) {
		t.Fatal("large traversal changed revision or membership", err)
	}
	back, err := l.page(context.Background(), s.ProfileID(), "kv", "keys", contract.DataOptions{Limit: limit, After: first})
	if err != nil || len(back.Rows) != limit || !bytes.Equal(back.NextCursor.Values[0], page.NextCursor.Values[0]) {
		t.Fatal("large backtracking changed boundary", err)
	}
	if count, err := l.count(context.Background(), s.ProfileID(), "kv", "keys"); err != nil || count != total {
		t.Fatal("count changed original large range", count, err)
	}
	row, err := l.row(context.Background(), s.ProfileID(), "kv", "keys", *page.RowRefs[0])
	if err != nil || !bytes.Equal(row.Values["value"].Bytes, value) {
		t.Fatal("complete 1 MiB editing value lost bytes", err)
	}
	refreshed, err := l.page(context.Background(), s.ProfileID(), "kv", "keys", contract.DataOptions{Limit: limit})
	if err != nil || len(refreshed.Rows) != limit {
		t.Fatal("healthy large refresh failed", err)
	}
	if count, err := l.count(context.Background(), s.ProfileID(), "kv", "keys"); err != nil || count != total+1 {
		t.Fatal("large refresh kept stale membership", count, err)
	}
	if _, err := l.page(context.Background(), s.ProfileID(), "kv", "keys", contract.DataOptions{Limit: limit, After: first}); err == nil || l.rangeLost {
		t.Fatal("old large range accepted or invalid cursor retired healthy range")
	}
}

func TestEtcdNativeDataPartialPageCancelAndClosePublishNoRows(t *testing.T) {
	for _, mode := range []string{"cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			s, l, admin, prefix := nativeDataDocument(t)
			for i := 0; i < 3; i++ {
				nativeKVDo(t, admin, &pb.PutRequest{Key: append(bytes.Clone(prefix), byte(i)), Value: bytes.Repeat([]byte{0, 255}, contract.DataEditMaxBytes/2)})
			}
			if _, err := l.count(context.Background(), s.ProfileID(), "kv", "keys"); err != nil {
				t.Fatal(err)
			}
			upstream := nativeClient(t)
			defer upstream.Close()
			client, fault := faultClient(t, upstream, "/etcdserverpb.KV/Range", "read-block-second")
			s.opener = func(context.Context, connections.Profile, string) (*Client, error) { return client, nil }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				page *contract.DataPage
				err  error
			}
			done := make(chan completion, 1)
			go func() {
				p, err := l.page(ctx, s.ProfileID(), "kv", "keys", contract.DataOptions{Limit: 3})
				done <- completion{p, err}
			}()
			waitSignal(t, fault.entered)
			if fault.calls.Load() != 2 {
				t.Fatal("fixture did not interrupt an actual partially aggregated page")
			}
			if mode == "cancel" {
				cancel()
			} else if err := l.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-done:
				if got.err == nil || got.page != nil || !l.rangeLost || !s.Alive() {
					t.Fatal("partial native page published or unrelated session retired", got.err)
				}
			case <-time.After(8 * time.Second):
				t.Fatal("original partial executor did not acknowledge")
			}
			waitSignal(t, fault.done)
			if fault.calls.Load() != 2 {
				t.Fatal("failed partial native page retried")
			}
			if _, err := l.page(context.Background(), s.ProfileID(), "kv", "keys", contract.DataOptions{Limit: 3}); err == nil {
				t.Fatal("retired partial page restarted implicitly")
			}
		})
	}
}

func TestEtcdNativeDataLargeOmittedFieldsPreserveNativeValueAndLease(t *testing.T) {
	s, l, admin, prefix := nativeDataDocument(t)
	key := append(bytes.Clone(prefix), 1)
	value := bytes.Repeat([]byte{0, 255, 1, 128}, contract.DataEditMaxBytes/4)
	lease := nativeKVDo(t, admin, &pb.LeaseGrantRequest{TTL: 90}).Reply.Lease.ID
	defer nativeKVDo(t, admin, &pb.LeaseRevokeRequest{ID: lease})
	nativeKVDo(t, admin, &pb.PutRequest{Key: key, Value: value, Lease: lease})
	for _, change := range []string{"same-key", "lease-zero", "lease-restore", "conflict", "empty-value"} {
		t.Run(change, func(t *testing.T) {
			row, err := l.row(context.Background(), s.ProfileID(), "kv", "keys", l.ref(key))
			if err != nil {
				t.Fatal(err)
			}
			values := map[string]contract.DataValue{}
			wantValue, wantLease := value, lease
			switch change {
			case "same-key", "conflict":
				values["key"] = contract.DataValue{Kind: contract.Binary, Bytes: bytes.Clone(key)}
			case "lease-zero":
				values["lease"] = contract.DataValue{Kind: contract.Integer, Text: "0"}
				wantLease = 0
			case "lease-restore":
				// Restore the original lease after the explicit zero above.
				values["lease"] = contract.DataValue{Kind: contract.Integer, Text: strconv.FormatInt(lease, 10)}
			case "empty-value":
				values["value"] = contract.DataValue{Kind: contract.Binary}
				wantValue = nil
			}
			plan, err := l.preview(context.Background(), s.ProfileID(), "kv", "keys", []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: values}})
			if err != nil {
				t.Fatal("large original update preview", err)
			}
			defer plan.Close()
			request := plan.(*dataPlan).request
			put := request.Success[0].GetRequestPut()
			_, setValue := values["value"]
			_, setLease := values["lease"]
			if put.IgnoreValue == setValue || put.IgnoreLease == setLease || put.IgnoreValue && len(put.Value) != 0 || put.IgnoreLease && put.Lease != 0 || len(request.Compare) != 5 || proto.Size(request) >= 3<<19 {
				t.Fatal("omitted field flags or original five comparisons changed")
			}
			if change == "conflict" {
				nativeKVDo(t, admin, &pb.PutRequest{Key: key, IgnoreValue: true, IgnoreLease: true})
			}
			saved, err := l.submit(context.Background(), s.ProfileID(), "kv", "keys", plan)
			if change == "conflict" {
				if !errors.Is(err, contract.ErrDataConflict) || saved == nil || saved.CommitConfirmed || saved.CommitUncertain || saved.Incomplete {
					t.Fatal("large preserved-value conflict accepted", err)
				}
				return
			}
			if err != nil || saved == nil || !saved.CommitConfirmed || saved.CommitUncertain || saved.Incomplete || len(saved.Outcomes) != 1 {
				t.Fatal("large native update exceeded default server request bound or lost receipt", err)
			}
			actual := nativeKVDo(t, admin, &pb.RangeRequest{Key: key}).Reply.Entries[0]
			if !bytes.Equal(actual.Value, wantValue) || actual.Lease != wantLease || !bytes.Equal(saved.Outcomes[0].Saved.Original[1].Bytes, wantValue) {
				t.Fatal("omitted/empty value or omitted/zero lease changed native semantics")
			}
		})
	}
}

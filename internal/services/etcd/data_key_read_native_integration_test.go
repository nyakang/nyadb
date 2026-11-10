package etcd

import (
	"bytes"
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	authpb "go.etcd.io/etcd/api/v3/authpb"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

func TestEtcdNativeDataKeyReadKeepsOriginalPageRevisionAndCompleteKey(t *testing.T) {
	s, l, admin, prefix := nativeDataDocument(t)
	key := append(bytes.Clone(prefix), bytes.Repeat([]byte{255}, 4096-len(prefix))...)
	put := nativeKVDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("original")})
	page, err := l.page(context.Background(), s.ProfileID(), "kv", "keys", contract.DataOptions{Limit: 100})
	if err != nil || len(page.Rows) != 1 || !page.Rows[0][0].Truncated || page.CurrentCursor == nil {
		t.Fatal("original maximum-key page unavailable", err)
	}
	later := nativeKVDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("later")})
	request := contract.KVKeyReadRequest{Snapshot: page.RowSnapshots[0], Cursor: page.CurrentCursor}
	read, err := l.captureKeyRead(context.Background(), s.ProfileID(), "kv", "keys", request)
	if err != nil || read == nil || !bytes.Equal(read.Key, key) || read.Revision < put.Header.Revision || read.Revision >= later.Header.Revision || read.Scope.Namespace != s.KVScope().Namespace || read.Scope.Generation != s.Generation() {
		t.Fatal("key read substituted current revision/value", err)
	}
	if _, err := l.page(context.Background(), s.ProfileID(), "kv", "keys", contract.DataOptions{Limit: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.captureKeyRead(context.Background(), s.ProfileID(), "kv", "keys", request); err == nil {
		t.Fatal("old page observation survived explicit refresh")
	}
}

func TestEtcdNativeDataKeyReadRejectsActualRevokedReadGrant(t *testing.T) {
	admin := accessNativeAdmin(t)
	key := nativeKey(t)
	nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("original")})
	user := "key-read-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	accessNativeUser(t, admin, user, user, []*authpb.Permission{{PermType: authpb.READ, Key: key}})
	p, _ := sessionProfile(t)
	p.Username, p.EtcdOptions.Auth = user, "basic"
	s, l := accessNativeDocument(t, p)
	page, err := l.page(context.Background(), p.ID, "kv", "keys", contract.DataOptions{Limit: 100})
	if err != nil || len(page.Rows) != 1 {
		t.Fatal("actual read grant did not produce original page", err)
	}
	r := contract.KVKeyReadRequest{Snapshot: page.RowSnapshots[0], Cursor: page.CurrentCursor}
	if _, err := l.captureKeyRead(context.Background(), s.ProfileID(), "kv", "keys", r); err != nil {
		t.Fatal("read-only original key action denied", err)
	}
	if _, err := pb.NewAuthClient(admin.conn).RoleRevokePermission(admin.authContext(context.Background()), &pb.AuthRoleRevokePermissionRequest{Role: user, Key: key}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.captureKeyRead(context.Background(), s.ProfileID(), "kv", "keys", r); err == nil {
		t.Fatal("revoked actual read grant opened original key events")
	}
	if !l.rangeLost || len(page.Rows) != 1 {
		t.Fatal("grant change did not retire original scope and preserve cached page")
	}
}

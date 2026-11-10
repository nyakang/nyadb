package etcd

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	authpb "go.etcd.io/etcd/api/v3/authpb"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

func accessNativeAdmin(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("NYADB_ETCD_ACCESS_IT") != "1" {
		t.Skip("requires owned etcd access fixture")
	}
	c := nativeClient(t)
	if os.Getenv("NYADB_ETCD_MODE") != "anonymous" {
		return c
	}
	auth := pb.NewAuthClient(c.conn)
	ctx := c.authContext(context.Background())
	secret := os.Getenv("NYADB_ETCD_SECRET")
	if _, err := auth.UserAdd(ctx, &pb.AuthUserAddRequest{Name: "root", Password: secret}); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.UserGrantRole(ctx, &pb.AuthUserGrantRoleRequest{User: "root", Role: "root"}); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AuthEnable(ctx, &pb.AuthEnableRequest{}); err != nil {
		t.Fatal(err)
	}
	cfg := c.config
	cfg.Auth, cfg.Username = "basic", "root"
	admin, err := Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	t.Cleanup(func() {
		auth := pb.NewAuthClient(admin.conn)
		ctx := admin.authContext(context.Background())
		if _, err := auth.AuthDisable(ctx, &pb.AuthDisableRequest{}); err != nil {
			t.Error("restore original anonymous fixture", err)
		}
		if _, err := auth.UserDelete(ctx, &pb.AuthUserDeleteRequest{Name: "root"}); err != nil {
			t.Error("remove original owned root fixture user", err)
		}
	})
	return admin
}
func accessNativeUser(t *testing.T, admin *Client, user, role string, permissions []*authpb.Permission) {
	t.Helper()
	auth := pb.NewAuthClient(admin.conn)
	ctx := admin.authContext(context.Background())
	if _, err := auth.UserAdd(ctx, &pb.AuthUserAddRequest{Name: user, Password: os.Getenv("NYADB_ETCD_SECRET")}); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.RoleAdd(ctx, &pb.AuthRoleAddRequest{Name: role}); err != nil {
		t.Fatal(err)
	}
	for _, permission := range permissions {
		if _, err := auth.RoleGrantPermission(ctx, &pb.AuthRoleGrantPermissionRequest{Name: role, Perm: permission}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := auth.UserGrantRole(ctx, &pb.AuthUserGrantRoleRequest{User: user, Role: role}); err != nil {
		t.Fatal(err)
	}
}
func accessNativeDocument(t *testing.T, p connections.Profile) (*Session, *dataLease) {
	t.Helper()
	raw, err := OpenSession(context.Background(), p, os.Getenv("NYADB_ETCD_SECRET"))
	if err != nil {
		t.Fatal("authorized profile handshake", err)
	}
	s := raw.(*Session)
	t.Cleanup(func() { _ = s.Close() })
	doc, err := s.ReserveDataDocument(s.KVScope().Namespace.Object("keyspace", "keys"))
	if err != nil {
		t.Fatal(err)
	}
	return s, doc.(*dataLease)
}

func TestEtcdNativeAccessBasicTokenAndCertificateActualIdentity(t *testing.T) {
	admin := accessNativeAdmin(t)
	key := nativeKey(t)
	nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("identity")})
	user := "token-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	accessNativeUser(t, admin, user, user, []*authpb.Permission{{PermType: authpb.READ, Key: key}})
	p, _ := sessionProfile(t)
	secret := os.Getenv("NYADB_ETCD_SECRET")
	p.Username, p.EtcdOptions.Auth = user, "basic"
	p.EtcdOptions.Prefix, p.EtcdOptions.PrefixEncoding = "", "text"
	check := func(t *testing.T, profile connections.Profile, principal string, all bool) {
		t.Helper()
		credential := secret
		if profile.EtcdOptions.Auth != "basic" {
			credential = ""
		}
		c, err := OpenProfile(context.Background(), profile, credential)
		if err != nil {
			t.Fatal("actual identity handshake", err)
		}
		defer c.Close()
		a, err := c.permissions(context.Background())
		if err != nil || a.Principal != principal || !allowsKey(a.Read, key) || allowsKey(a.Read, []byte("/foreign/identity")) != all {
			t.Fatal("captured permissions followed the wrong identity", err)
		}
		if result := nativeDo(t, c, &pb.RangeRequest{Key: key}).Native().(*pb.RangeResponse); len(result.Kvs) != 1 {
			t.Fatal("captured identity cannot read its granted key")
		}
		if !all {
			plan, err := c.Prepare(&pb.RangeRequest{Key: []byte("/foreign/identity")})
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.Execute(context.Background(), plan)
			if err == nil || result.Completed || result.WriteOutcomeUnknown {
				t.Fatal("restricted actual token escaped its grant")
			}
		}
	}
	t.Run("basic-original-username", func(t *testing.T) { check(t, p, user, false) })
	if os.Getenv("NYADB_ETCD_CA") == "" {
		return
	}
	certUser := "acl-cert"
	accessNativeUser(t, admin, certUser, certUser, []*authpb.Permission{{PermType: authpb.READ, Key: key}})
	t.Run("basic-limited-root-certificate", func(t *testing.T) {
		profile := p
		profile.EtcdOptions.ClientCertFile, profile.EtcdOptions.ClientKeyFile = os.Getenv("NYADB_ETCD_ROOT_CERT"), os.Getenv("NYADB_ETCD_ROOT_KEY")
		check(t, profile, user, false)
	})
	t.Run("basic-root-limited-certificate", func(t *testing.T) {
		profile := p
		profile.Username = "root"
		profile.EtcdOptions.ClientCertFile, profile.EtcdOptions.ClientKeyFile = os.Getenv("NYADB_ETCD_ACCESS_CERT"), os.Getenv("NYADB_ETCD_ACCESS_KEY")
		check(t, profile, "root", true)
	})
	t.Run("pure-limited-certificate", func(t *testing.T) {
		profile := p
		profile.Username, profile.EtcdOptions.Auth = "", "certificate"
		profile.EtcdOptions.ClientCertFile, profile.EtcdOptions.ClientKeyFile = os.Getenv("NYADB_ETCD_ACCESS_CERT"), os.Getenv("NYADB_ETCD_ACCESS_KEY")
		if os.Getenv("NYADB_ETCD_MODE") == "mtls" {
			check(t, profile, certUser, false)
		} else {
			c, err := OpenProfile(context.Background(), profile, "")
			if c != nil {
				_ = c.Close()
			}
			if err == nil {
				t.Fatal("TLS without client auth invented a certificate identity")
			}
		}
	})
}
func TestEtcdNativeAccessOriginalBinaryRangesCountPagingExportAndWritePermissions(t *testing.T) {
	admin := accessNativeAdmin(t)
	prefix := []byte("/access/" + strconv.FormatInt(time.Now().UnixNano(), 36) + "/")
	path := func(s string) []byte { return append(bytes.Clone(prefix), s...) }
	exact := append(bytes.Clone(prefix), 0, 255, 1)
	exactAdjacent := append(bytes.Clone(exact), 0)
	high := append([]byte{255, 255, 0}, prefix...)
	maxKey := bytes.Repeat([]byte{255}, 4096)
	keys := [][]byte{exact, exactAdjacent, path("a/key"), path("b/key"), path("c/key"), path("z/key"), append(bytes.Clone(high), 1), maxKey}
	for i, key := range keys {
		nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte{0, 255, byte(i)}})
	}
	nativeDo(t, admin, &pb.PutRequest{Key: path("outside/key"), Value: []byte("must not leak")})
	role := "access-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	user := " limited " + role + " 用户 "
	permissions := []*authpb.Permission{{PermType: authpb.READ, Key: exactAdjacent}, {PermType: authpb.READ, Key: exact}, {PermType: authpb.READ, Key: path("a/"), RangeEnd: path("c/")}, {PermType: authpb.READ, Key: path("b/"), RangeEnd: path("d/")}, {PermType: authpb.READ, Key: path("d/"), RangeEnd: path("e/")}, {PermType: authpb.READ, Key: path("z/"), RangeEnd: path("z0")}, {PermType: authpb.READ, Key: high, RangeEnd: []byte{0}}, {PermType: authpb.READWRITE, Key: path("a/"), RangeEnd: path("b/")}}
	accessNativeUser(t, admin, user, role, permissions)
	p, _ := sessionProfile(t)
	p.Username = user
	p.EtcdOptions.Auth = "basic"
	p.EtcdOptions.Prefix = ""
	p.EtcdOptions.PrefixEncoding = "text"
	s, l := accessNativeDocument(t, p)
	ctx := context.Background()
	count, err := l.count(ctx, p.ID, "kv", "keys")
	if err != nil || count != int64(len(keys)) {
		t.Fatal("complete authorized count before page", count, err)
	}
	var got [][]byte
	var cursor *contract.DataCursor
	offset := int64(0)
	var writableSnapshot *contract.RowSnapshot
	for {
		page, err := l.page(ctx, p.ID, "kv", "keys", contract.DataOptions{Limit: 2, Offset: offset, After: cursor})
		if err != nil {
			t.Fatal("authorized fixed-revision page", err)
		}
		if page.Editable == nil || !*page.Editable {
			t.Fatal("writable subset disabled whole document")
		}
		for i, ref := range page.RowRefs {
			key := ref.Values[0]
			got = append(got, bytes.Clone(key))
			writable := bytes.HasPrefix(key, path("a/"))
			if page.ReadOnlyRows[i] == writable {
				t.Fatal("per-row native write permissions collapsed")
			}
			if writable {
				writableSnapshot = page.RowSnapshots[i]
			}
		}
		if !page.HasMore {
			break
		}
		if len(page.Rows) != 2 || page.NextCursor == nil {
			t.Fatal("authorized cursor incomplete")
		}
		cursor = page.NextCursor
		offset += int64(len(page.Rows))
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })
	if len(got) != len(keys) {
		t.Fatal("authorized traversal missed or duplicated keys", len(got))
	}
	for i := range keys {
		if !bytes.Equal(got[i], keys[i]) {
			t.Fatal("authorized byte ordering changed", i)
		}
	}
	var exported [][]byte
	if err := l.export(ctx, p.ID, "kv", "keys", contract.DataOptions{}, []string{"key", "value"}, func(row []contract.DataValue) error {
		exported = append(exported, bytes.Clone(row[0].Bytes))
		if len(row[1].Bytes) != 3 {
			t.Fatal("complete native binary export lost bytes")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(exported) != len(got) {
		t.Fatal("export did not use the captured authorized ranges")
	}
	if writableSnapshot == nil {
		t.Fatal("original writable observation missing")
	}
	result, err := l.save(ctx, p.ID, "kv", "keys", []contract.DataEdit{{Kind: "update", Original: writableSnapshot, Values: map[string]contract.DataValue{"value": {Kind: contract.Binary, Bytes: []byte("saved")}}}})
	if err != nil || result == nil || !result.CommitConfirmed {
		t.Fatal("authorized original atomic save", err)
	}
	// A rename may not escape the captured writable/readable destination range.
	current, err := l.row(ctx, p.ID, "kv", "keys", l.ref(path("a/key")))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := l.preview(ctx, p.ID, "kv", "keys", []contract.DataEdit{{Kind: "update", Original: current.Snapshot, Values: map[string]contract.DataValue{"key": {Kind: contract.Binary, Bytes: path("b/renamed")}}}})
	if err == nil || plan != nil {
		t.Fatal("read-only rename destination admitted")
	}
	for _, narrow := range []string{"b/", "missing/"} {
		sub := p
		sub.ID++
		sub.EtcdOptions.PrefixEncoding = "base64"
		sub.EtcdOptions.Prefix = base64.StdEncoding.EncodeToString(path(narrow))
		_, doc := accessNativeDocument(t, sub)
		page, err := doc.page(ctx, sub.ID, "kv", "keys", contract.DataOptions{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if page.Editable == nil || *page.Editable {
			t.Fatal("read-only prefix advertised grid writes")
		}
		want := 1
		if narrow == "missing/" {
			want = 0
		}
		if len(page.Rows) != want {
			t.Fatal("profile prefix intersection", narrow, len(page.Rows))
		}
	}
	if !s.Alive() {
		t.Fatal("independent denied preview retired the original session")
	}
	for _, writeOnly := range []bool{false, true} {
		t.Run("no-readable-user-"+strconv.FormatBool(writeOnly), func(t *testing.T) {
			name := "no-read-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			var grants []*authpb.Permission
			if writeOnly {
				grants = []*authpb.Permission{{PermType: authpb.WRITE, Key: prefix, RangeEnd: PrefixRange(prefix, 1, 0).RangeEnd}}
			}
			accessNativeUser(t, admin, name, name, grants)
			profile := p
			profile.ID++
			profile.Username = name
			_, doc := accessNativeDocument(t, profile)
			page, err := doc.page(ctx, profile.ID, "kv", "keys", contract.DataOptions{Limit: 10})
			if err != nil || len(page.Rows) != 0 || page.Editable == nil || *page.Editable {
				t.Fatal("no-readable user probed forbidden keys or advertised writes", err)
			}
			count, err := doc.count(ctx, profile.ID, "kv", "keys")
			if err != nil || count != 0 {
				t.Fatal("no-readable count", count, err)
			}
			emitted := false
			if err := doc.export(ctx, profile.ID, "kv", "keys", contract.DataOptions{}, []string{"key"}, func([]contract.DataValue) error { emitted = true; return nil }); err != nil || emitted {
				t.Fatal("no-readable export", err)
			}
			plan, err := doc.preview(ctx, profile.ID, "kv", "keys", []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"key": {Kind: contract.Binary, Bytes: path("new")}}}})
			if err == nil || plan != nil {
				t.Fatal("grid admitted a write without readable saved observations")
			}
		})
	}
}

func TestEtcdNativeAccessActualGrantChangeAndOriginalMetadataCancelClose(t *testing.T) {
	admin := accessNativeAdmin(t)
	prefix := nativeKey(t)
	a := append(bytes.Clone(prefix), 'a')
	b := append(bytes.Clone(prefix), 'b')
	for _, key := range [][]byte{a, b} {
		nativeDo(t, admin, &pb.PutRequest{Key: key, Value: []byte("original")})
	}
	role := "changed-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	user := " " + role + " "
	accessNativeUser(t, admin, user, role, []*authpb.Permission{{PermType: authpb.READWRITE, Key: a}, {PermType: authpb.READWRITE, Key: b}})
	p, _ := sessionProfile(t)
	secret := os.Getenv("NYADB_ETCD_SECRET")
	p.Username = user
	p.EtcdOptions.Auth = "basic"
	p.EtcdOptions.PrefixEncoding = "base64"
	p.EtcdOptions.Prefix = base64.StdEncoding.EncodeToString(prefix)
	ctx := context.Background()
	for _, mode := range []string{"cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			s, l := accessNativeDocument(t, p)
			page, err := l.page(ctx, p.ID, "kv", "keys", contract.DataOptions{Limit: 10})
			if err != nil || len(page.Rows) != 2 {
				t.Fatal(err)
			}
			upstream, err := OpenProfile(ctx, p, secret)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = upstream.Close() })
			cfg := upstream.config
			cfg.DiscoverAccess = false
			cfg.ProbeKey = bytes.Clone(a)
			blocked, fault := faultClientConfig(t, upstream, cfg, secret, "/etcdserverpb.Auth/RoleGet", "cancel")
			s.opener = func(context.Context, connections.Profile, string) (*Client, error) { return blocked, nil }
			request, cancel := context.WithCancel(ctx)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := l.count(request, p.ID, "kv", "keys"); done <- err }()
			waitSignal(t, fault.entered)
			if mode == "close" {
				if err := l.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("original metadata interruption reported completion")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("original metadata executor did not settle")
			}
			waitSignal(t, fault.done)
			if fault.calls.Load() != 1 || fault.receipt == nil {
				t.Fatal("original role query retried")
			}
			if mode == "cancel" {
				again, err := l.page(ctx, p.ID, "kv", "keys", contract.DataOptions{Limit: 10})
				if err != nil || len(again.Rows) != 2 {
					t.Fatal("count metadata cancellation retired the independent original page", err)
				}
			}
			if !s.Alive() {
				t.Fatal("document metadata interruption closed unrelated session")
			}
		})
	}
	s, l := accessNativeDocument(t, p)
	page, err := l.page(ctx, p.ID, "kv", "keys", contract.DataOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := l.preview(ctx, p.ID, "kv", "keys", []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[1], Values: map[string]contract.DataValue{"value": {Kind: contract.Binary, Bytes: []byte("must not write")}}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = pb.NewAuthClient(admin.conn).RoleRevokePermission(admin.authContext(ctx), &pb.AuthRoleRevokePermissionRequest{Role: role, Key: b})
	if err != nil {
		t.Fatal(err)
	}
	result, err := l.submit(ctx, p.ID, "kv", "keys", plan)
	if err == nil || result == nil || result.CommitConfirmed || result.CommitUncertain {
		t.Fatal("actual revoked Txn admitted or mislabeled as unknown", err)
	}
	original := nativeDo(t, admin, &pb.RangeRequest{Key: b}).Native().(*pb.RangeResponse)
	if len(original.Kvs) != 1 || !bytes.Equal(original.Kvs[0].Value, []byte("original")) {
		t.Fatal("revoked original write changed server data")
	}
	// Fresh auxiliary authorization cannot silently replace the page's captured ranges.
	if _, err := l.count(ctx, p.ID, "kv", "keys"); !errors.Is(err, errAccessChanged) {
		t.Fatal("changed original permission snapshot accepted", err)
	}
	if _, err := l.page(ctx, p.ID, "kv", "keys", contract.DataOptions{Limit: 10}); !errors.Is(err, errDataRangeLost) {
		t.Fatal("retired range retried or silently reopened", err)
	}
	if len(page.Rows) != 2 || !bytes.Equal(page.RowRefs[1].Values[0], b) || !s.Alive() {
		t.Fatal("last original page or unrelated session lost")
	}
	fresh, err := s.ReserveDataDocument(s.KVScope().Namespace.Object("keyspace", "keys"))
	if err != nil {
		t.Fatal(err)
	}
	next, err := fresh.(*dataLease).page(ctx, p.ID, "kv", "keys", contract.DataOptions{Limit: 10})
	if err != nil || len(next.Rows) != 1 || !bytes.Equal(next.RowRefs[0].Values[0], a) {
		t.Fatal("explicit new document did not capture current authorized ranges", err)
	}
}

func TestEtcdNativeAccessMaximumPrefixesRejectLongKeysAndRetainCompleteAuthorizedCounts(t *testing.T) {
	admin := accessNativeAdmin(t)
	ctx := admin.authContext(context.Background())
	kv := pb.NewKVClient(admin.conn)
	maxKey := bytes.Repeat([]byte{255}, 4096)
	longKey := append(bytes.Clone(maxKey), 0)
	for _, key := range [][]byte{maxKey, longKey} {
		// The server permits this longer key; the application's data domain
		// must reject it explicitly instead of silently narrowing the prefix.
		if _, err := kv.Put(ctx, &pb.PutRequest{Key: key, Value: []byte("domain")}); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if _, err := kv.DeleteRange(ctx, &pb.DeleteRangeRequest{Key: longKey}); err != nil {
			t.Error("owned long-key fixture cleanup", err)
		}
	})
	profile, _ := sessionProfile(t)
	profile.EtcdOptions.Auth = "basic"
	profile.EtcdOptions.PrefixEncoding = "base64"
	profile.EtcdOptions.Prefix = base64.StdEncoding.EncodeToString(maxKey)
	for _, exact := range []bool{false, true} {
		t.Run("maximum-exact-"+strconv.FormatBool(exact), func(t *testing.T) {
			name := "max-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			permission := &authpb.Permission{PermType: authpb.READ, Key: maxKey}
			if !exact {
				permission.RangeEnd = []byte{0}
			}
			accessNativeUser(t, admin, name, name, []*authpb.Permission{permission})
			p := profile
			p.Username = name
			_, doc := accessNativeDocument(t, p)
			count, err := doc.count(context.Background(), p.ID, "kv", "keys")
			want := int64(2)
			if exact {
				want = 1
			}
			if err != nil || count != want {
				t.Fatal("maximum prefix collapsed into an exact key", count, err)
			}
			page, err := doc.page(context.Background(), p.ID, "kv", "keys", contract.DataOptions{Limit: 10})
			if exact {
				if err != nil || len(page.Rows) != 1 || page.HasMore {
					t.Fatal("valid maximum exact grant", err)
				}
			} else if !errors.Is(err, ErrBounds) || page != nil {
				t.Fatal("oversized server key silently omitted from a complete page", err)
			}
			_, exportDoc := accessNativeDocument(t, p)
			emitted := 0
			err = exportDoc.export(context.Background(), p.ID, "kv", "keys", contract.DataOptions{}, []string{"key"}, func([]contract.DataValue) error { emitted++; return nil })
			if exact {
				if err != nil || emitted != 1 {
					t.Fatal("maximum exact export", emitted, err)
				}
			} else if !errors.Is(err, ErrBounds) || emitted != 0 {
				t.Fatal("oversized server key silently omitted from export", emitted, err)
			}
		})
	}
	if _, err := kv.DeleteRange(ctx, &pb.DeleteRangeRequest{Key: longKey}); err != nil {
		t.Fatal(err)
	}
	// Two supported 4 KiB keys still page and export completely. Advancement
	// observes the next actual key instead of carrying past unobserved bytes.
	common := bytes.Repeat([]byte{122}, 4095)
	a, b, end := append(bytes.Clone(common), 1), append(bytes.Clone(common), 2), append(bytes.Clone(common), 3)
	for _, key := range [][]byte{a, b} {
		if _, err := kv.Put(ctx, &pb.PutRequest{Key: key, Value: []byte("supported")}); err != nil {
			t.Fatal(err)
		}
	}
	name := "bounded-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	accessNativeUser(t, admin, name, name, []*authpb.Permission{{PermType: authpb.READ, Key: a, RangeEnd: end}})
	profile.Username = name
	profile.EtcdOptions.Prefix = base64.StdEncoding.EncodeToString(common)
	_, doc := accessNativeDocument(t, profile)
	page, err := doc.page(context.Background(), profile.ID, "kv", "keys", contract.DataOptions{Limit: 1})
	if err != nil || len(page.Rows) != 1 || !page.HasMore || !bytes.Equal(page.RowRefs[0].Values[0], a) {
		t.Fatal("first supported maximum key", err)
	}
	next, err := doc.page(context.Background(), profile.ID, "kv", "keys", contract.DataOptions{Limit: 1, Offset: 1, After: page.NextCursor})
	if err != nil || len(next.Rows) != 1 || next.HasMore || !bytes.Equal(next.RowRefs[0].Values[0], b) {
		t.Fatal("next supported maximum key", err)
	}
	emitted := 0
	if err := doc.export(context.Background(), profile.ID, "kv", "keys", contract.DataOptions{}, []string{"key"}, func([]contract.DataValue) error { emitted++; return nil }); err != nil || emitted != 2 {
		t.Fatal("complete supported maximum key export", emitted, err)
	}
}

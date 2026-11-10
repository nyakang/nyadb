package dataworkspace

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func etcdDataProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_DATA_IT") != "1" {
		t.Skip("requires owned public etcd data fixture")
	}
	u, err := url.Parse(os.Getenv("NYADB_ETCD_ENDPOINT"))
	if err != nil || u.Hostname() == "" {
		t.Fatal("missing owned fixture")
	}
	p, _ := connections.Defaults(connections.Etcd)
	p.ID, p.Name, p.Host = 93, "native key data", u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	p.Port = uint16(port)
	p.EtcdOptions.ConnectTimeoutSeconds, p.EtcdOptions.RequestTimeoutSeconds = 5, 5
	if u.Scheme == "https" {
		p.TLSMode = "verify-full"
	}
	p.EtcdOptions.CAFile, p.EtcdOptions.ClientCertFile, p.EtcdOptions.ClientKeyFile = os.Getenv("NYADB_ETCD_CA"), os.Getenv("NYADB_ETCD_CERT"), os.Getenv("NYADB_ETCD_KEY")
	prefix := append([]byte{0, 255}, []byte("/grid/"+t.Name()+"/"+strconv.FormatInt(time.Now().UnixNano(), 36)+"/")...)
	p.EtcdOptions.PrefixEncoding, p.EtcdOptions.Prefix = "base64", base64.StdEncoding.EncodeToString(prefix)
	secret := ""
	if os.Getenv("NYADB_ETCD_MODE") != "anonymous" {
		p.EtcdOptions.Auth, p.Username, secret = "basic", os.Getenv("NYADB_ETCD_USERNAME"), os.Getenv("NYADB_ETCD_SECRET")
	}
	return p, secret
}

func etcdDataDo(t *testing.T, lease contract.KVRequestLease, message proto.Message) contract.KVResult {
	t.Helper()
	method := "KV/Range"
	switch message.(type) {
	case *pb.PutRequest:
		method = "KV/Put"
	case *pb.DeleteRangeRequest:
		method = "KV/DeleteRange"
	}
	raw, err := protojson.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := lease.Prepare(contract.KVRequest{Method: "/etcdserverpb." + method, JSON: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	r, err := lease.Execute(context.Background(), plan)
	if err != nil || !r.Completed || r.Reply == nil {
		t.Fatal("owned native data fixture request", err)
	}
	return r
}

func waitEtcdData(t *testing.T, s *State, d *Document) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for d.Busy() || d.loading {
		s.Consume(ui.Services{})
		// A ready confirmation intentionally keeps editing busy until the user
		// confirms or cancels. Only its reader acknowledgement is awaited here.
		if d.mutation.open && d.mutation.results == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("original data request did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}

func captureEtcdData(t *testing.T, s *State, d *Document, name string) {
	t.Helper()
	output := os.Getenv("NYADB_ETCD_DATA_IMAGES")
	if output == "" {
		return
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.View(c, d); s.Modals(c) }, size[0], size[1])
			tt.Frame()
			if name == "confirmation" {
				for _, label := range []string{"确认键更改", "复制完整预览", "取消", "确认并提交更改"} {
					r, ok := tt.Find(locales.Text(label))
					if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
						t.Fatal("key confirmation control clipped", language, label, r)
					}
				}
			}
			f, err := os.Create(filepath.Join(output, language+"-"+strconv.Itoa(size[0])+"x"+strconv.Itoa(size[1])+"-"+name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(f, tt.Image())
			closeErr := f.Close()
			if err != nil || closeErr != nil {
				t.Fatal(err, closeErr)
			}
		}
	}
}

func TestEtcdNativeDataSharedGridOriginalPreviewParkingRefreshAndGeneration(t *testing.T) {
	p, secret := etcdDataProfile(t)
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	defer m.Close()
	handle, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := m.KVRequests(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := provider.ReserveKV(99)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	prefix, _ := connections.EtcdPrefixBytes(p.EtcdOptions)
	for i := 0; i < 7; i++ {
		etcdDataDo(t, admin, &pb.PutRequest{Key: append(bytes.Clone(prefix), byte(i)), Value: bytes.Repeat([]byte{0, byte(i), 255}, 500)})
	}
	active := Session{ProfileID: p.ID, Generation: handle.Generation(), Connected: true}
	s := New(Dependencies{Session: func() Session { return active }, RuntimeFor: func(id int64) Runtime {
		if id == p.ID {
			return m.Data(id)
		}
		return Runtime{}
	}})
	defer s.CloseAll()
	object := m.Namespace(p.ID, "kv").Object("keyspace", "keys")
	d := s.OpenObject(1, object, handle.Capabilities())
	sibling := s.OpenObject(2, object, handle.Capabilities())
	if d == nil || sibling == nil {
		t.Fatal("runtime did not publish original qualified namespace", object)
	}
	d.limit = 3
	sibling.limit = 3
	s.Load(ui.Services{}, d, false)
	waitEtcdData(t, s, d)
	s.Load(ui.Services{}, sibling, false)
	waitEtcdData(t, s, sibling)
	if d.page == nil {
		t.Fatal("shared native page missing", d.err)
	}
	if d.err != "" || len(d.page.Rows) != 3 || !d.page.HasMore || d.page.LiveRead || !d.editable() || d.runtimeLease == nil || s.runtimeFor(d).Selector.Validate != nil {
		t.Fatalf("shared native grid: error=%q rows=%d more=%v live=%v editable=%v lease=%v selector=%v capabilities=%+v", d.err, len(d.page.Rows), d.page.HasMore, d.page.LiveRead, d.editable(), d.runtimeLease != nil, s.runtimeFor(d).Selector.Validate != nil, d.capabilities)
	}
	for column, metadata := range d.page.ColumnMetadata {
		if mysqlDataCanEdit(d, 0, column) == metadata.Generated {
			t.Fatal("generated native revision editable", metadata.Name)
		}
	}
	captureEtcdData(t, s, d, "grid")
	cached := bytes.Clone(sibling.page.Rows[0][1].Value.Bytes)
	// Refresh observes an external insertion in this document, while the sibling
	// keeps its original fixed revision until it requests its own refresh.
	etcdDataDo(t, admin, &pb.PutRequest{Key: append(bytes.Clone(prefix), 7), Value: []byte("external")})
	s.Load(ui.Services{}, d, true)
	waitEtcdData(t, s, d)
	if count, err := s.runtimeFor(d).Count(context.Background(), p.ID, "kv", "keys"); err != nil || count != 8 {
		t.Fatal("shared Refresh did not create a new MVCC range", count, err)
	}
	if count, err := s.runtimeFor(sibling).Count(context.Background(), p.ID, "kv", "keys"); err != nil || count != 7 {
		t.Fatal("refresh changed sibling MVCC membership", count, err)
	}
	d.edit.Batch = true
	s.beginMySQLCellEdit(ui.Services{}, d, 0, 1, true)
	waitEtcdData(t, s, d)
	if d.edit.Editor == nil {
		t.Fatal("complete byte editor missing", d.edit.Err)
	}
	d.edit.Editor.Text = "0x" + hex.EncodeToString([]byte{255, 0, 255, 1})
	s.finishMySQLCellEdit(d, true)
	if d.edit.Editor != nil || !d.HasDraft() {
		t.Fatal("binary editor did not capture draft", d.edit.Err)
	}
	s.saveMySQLData(ui.Services{}, d)
	waitEtcdData(t, s, d)
	if !d.mutation.open || d.mutation.plan == nil {
		t.Fatal("immutable original key preview missing", d.edit.Err)
	}
	captureEtcdData(t, s, d, "confirmation")
	s.cancelDataMutation(d)
	if !d.HasDraft() || d.mutation.open {
		t.Fatal("preview cancel discarded drafts")
	}
	s.saveMySQLData(ui.Services{}, d)
	waitEtcdData(t, s, d)
	s.confirmDataMutation(ui.Services{}, d)
	active.ProfileID = 94
	select {
	case result := <-d.edit.Results:
		out := make(chan mysqlEditResult, 1)
		out <- result
		d.edit.Results = out
		if result.Err != nil || result.Saved == nil || !result.Saved.CommitConfirmed {
			t.Fatal("real native key grid submission", result.Err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("original key write stalled")
	}
	s.Consume(ui.Services{})
	if !d.edit.Busy || !d.HasDraft() || d.nativeReceipt.JSON != "" {
		t.Fatal("parked completion consumed by another profile")
	}
	active.ProfileID = p.ID
	waitEtcdData(t, s, d)
	if d.err != "" || d.HasDraft() || d.edit.Uncertain != nil || d.nativeReceipt.JSON == "" || !bytes.Equal(d.page.Rows[0][1].Value.Bytes, []byte{255, 0, 255, 1}) || !bytes.Equal(sibling.page.Rows[0][1].Value.Bytes, cached) {
		t.Fatal("confirmed original grid refresh replaced parked sibling", d.err, d.edit.Err)
	}
	captureEtcdData(t, s, d, "saved")
	if err := s.runtimeFor(d).Exporter(context.Background(), p.ID, "kv", "keys", contract.DataOptions{}, []string{"key", "value"}, func(v []contract.DataValue) error {
		if !v[0].Binary || !v[1].Binary {
			return contract.ErrDataPage
		}
		return nil
	}); err != nil {
		t.Fatal("original grid export", err)
	}
	old := s.runtimeFor(d)
	m.Disconnect(p.ID)
	fresh, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	active.Generation = fresh.Generation()
	if _, err := old.RowReader(context.Background(), p.ID, "kv", "keys", *d.page.RowRefs[0]); err == nil {
		t.Fatal("generation replacement admitted old native row")
	}
	s.Load(ui.Services{}, d, true)
	if d.err == "" || !bytes.Equal(d.page.Rows[0][1].Value.Bytes, []byte{255, 0, 255, 1}) {
		t.Fatal("replacement changed original cached document")
	}
	captureEtcdData(t, s, d, "generation")
	s.Close(d)
	s.Close(d)
	if !m.IsConnected(p.ID) {
		t.Fatal("old document close disconnected replacement session")
	}
}

package shell

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
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
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func etcdPublicURI(t *testing.T) string {
	t.Helper()
	if os.Getenv("NYADB_ETCD_PUBLIC_IT") != "1" {
		t.Skip("requires owned public etcd workbench fixture")
	}
	u, err := url.Parse(os.Getenv("NYADB_ETCD_ENDPOINT"))
	if err != nil || u.Hostname() == "" {
		t.Fatal("missing owned native endpoint")
	}
	u.Scheme = "etcd"
	if os.Getenv("NYADB_ETCD_CA") != "" {
		u.Scheme = "etcds"
	}
	prefix := append([]byte{0, 255}, []byte("/public/"+t.Name()+"/"+strconv.FormatInt(time.Now().UnixNano(), 36)+"/")...)
	q := url.Values{"prefix_encoding": {"base64"}, "prefix": {base64.StdEncoding.EncodeToString(prefix)}}
	if os.Getenv("NYADB_ETCD_MODE") != "anonymous" {
		u.User = url.UserPassword(os.Getenv("NYADB_ETCD_USERNAME"), os.Getenv("NYADB_ETCD_SECRET"))
		q.Set("auth", "basic")
	}
	for key, env := range map[string]string{"ca_file": "NYADB_ETCD_CA", "client_cert_file": "NYADB_ETCD_CERT", "client_key_file": "NYADB_ETCD_KEY"} {
		if value := os.Getenv(env); value != "" {
			q.Set(key, value)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func etcdPublicWait(t *testing.T, tt *ui.Tester, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !done() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if !done() {
		t.Fatal("original public etcd workbench did not settle")
	}
	tt.Frame()
}
func etcdPublicClick(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	tt.Frame()
	if err := tt.Click(locales.Text(label)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
}
func etcdPublicDo(t *testing.T, lease contract.KVRequestLease, message proto.Message) contract.KVResult {
	t.Helper()
	method := "KV/Range"
	switch message.(type) {
	case *pb.PutRequest:
		method = "KV/Put"
	case *pb.LeaseGrantRequest:
		method = "Lease/LeaseGrant"
	case *pb.LeaseRevokeRequest:
		method = "Lease/LeaseRevoke"
	}
	raw, err := protojson.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := lease.Prepare(contract.KVRequest{Method: "/etcdserverpb." + method, JSON: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lease.Execute(context.Background(), plan)
	if err != nil || !result.Completed || result.Reply == nil {
		t.Fatal("original public native request", err)
	}
	return result
}
func captureEtcdPublic(t *testing.T, s *State, phase string) {
	t.Helper()
	output := os.Getenv("NYADB_ETCD_PUBLIC_IMAGES")
	if output == "" {
		return
	}
	defer i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := ui.NewTester(s.View, size[0], size[1])
			tt.Frame()
			if phase == "lease-management-public-grant-confirmation" || phase == "lease-management-public-revoke-confirmation" {
				labels := []string{"确认 KV 写入请求", "我确认执行此原生写入请求", "确认执行 KV 写入", "取消"}
				if phase == "lease-management-public-revoke-confirmation" {
					labels = append(labels, "复制完整撤销观察", "撤销按 ID 执行；关联键可能变化，ID 可能被重新使用", "没有 CAS 保护；后来 TTL 为 -1 也不能确认原未知撤销")
				}
				for _, label := range labels {
					r, ok := tt.Find(locales.Text(label))
					if !ok || r.W <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
						t.Fatal("public management confirmation clipped", phase, language, label, r)
					}
				}
			}
			if phase == "insert-preview" || phase == "rename-preview" {
				for _, label := range []string{"确认键更改", "复制完整预览", "取消", "确认并提交更改"} {
					r, ok := tt.Find(locales.Text(label))
					if !ok || r.W <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
						t.Fatal("public confirmation clipped", phase, language, label, r)
					}
				}
			}
			if phase == "lease-browser-public-detail" {
				detail, ok := s.currentTab().kv.LeaseDetailReceipt()
				if !ok || len(detail.Detail.Result.Reply.Lease.Keys) == 0 {
					t.Fatal("missing original attached key")
				}
				key := detail.Detail.Result.Reply.Lease.Keys[0]
				text := base64.StdEncoding.EncodeToString(key[:min(len(key), 256)])
				if len(key) > 256 {
					text += "…"
				}
				r, found := tt.Find(text)
				if !found || r.W <= 0 || r.Y+r.H > float32(size[1])-18 {
					t.Fatal("public attached key row clipped", language, size, r)
				}
			}
			if phase == "renewal-public-confirmation" {
				for _, label := range []string{"确认持续续租", "我授权持续续租此原租约", "确认开始持续续租", "取消"} {
					r, ok := tt.Find(locales.Text(label))
					if !ok || r.W <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
						t.Fatal("public renewal confirmation clipped", language, label, r)
					}
				}
			}
			file, err := os.Create(filepath.Join(output, language+"-"+strconv.Itoa(size[0])+"x"+strconv.Itoa(size[1])+"-"+phase+".png"))
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(file, tt.Image())
			closeErr := file.Close()
			if err != nil || closeErr != nil {
				t.Fatal("public image write")
			}
		}
	}
}

func TestEtcdNativeShellPublicChooserReopenQualifiedGridAtomicMutationsAndGuards(t *testing.T) {
	rawURI := etcdPublicURI(t)
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	s, tt, profiles, path := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	s.openConnectionDialog()
	tt.Frame()
	etcdPublicClick(t, tt, "搜索连接类型")
	tt.Type("etcd")
	tt.Frame()
	etcdPublicClick(t, tt, "选择 etcd")
	etcdPublicClick(t, tt, "下一步")
	if s.ConnectionWindowTitle() != "新建连接 (etcd)" {
		t.Fatal("public chooser lost engine")
	}
	if err := s.connectionEditor.ImportURI(rawURI); err != nil {
		t.Fatal("generic etcd URI did not enter editor", err)
	}
	s.connectionStep = 1
	s.updateConnectionWindowTitle()
	tt.Frame()
	if os.Getenv("NYADB_ETCD_MODE") != "anonymous" {
		etcdPublicClick(t, tt, "保存密码")
	}
	captureEtcdPublic(t, s, "profile")
	etcdPublicClick(t, tt, "保存连接")
	list, err := profiles.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatal("public profile not saved", err)
	}
	p := list[0]
	if p.Kind != connections.Etcd || s.runtime.IsConnected(p.ID) {
		t.Fatal("profile save lost original engine or connected implicitly")
	}
	s.CloseMySQLSessions()
	db, err := localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = NewWithProfiles(connections.NewService(localdb.NewProfileStore(db)))
	s.SetCredentialVault(vault)
	defer s.CloseMySQLSessions()
	var services ui.Services
	tt = ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	s.selectProfileByName(p.Name)
	tt.Frame()
	etcdPublicClick(t, tt, "连接所选数据库")
	etcdPublicWait(t, tt, func() bool { return s.sessions.StatusFor(p.ID) != "连接中" })
	w := s.sessions.Current()
	if w.Status != "已连接" || !s.runtime.IsConnected(p.ID) || w.Namespaces["kv"] != (contract.NamespaceRef{ProfileID: p.ID, Catalog: "kv"}) {
		t.Fatal("public handshake/namespace status diverged", w.Error)
	}
	caps := s.objectCapabilitiesFor(p.ID)
	if !caps.KeyValues || !caps.EditableTables || caps.Query || caps.Transactions || caps.DesignTables || caps.Accounts {
		t.Fatal("public KV capability gates changed", caps)
	}
	node := navigation.DatabaseNode(p.ID, "kv")
	s.selectNavigationNode(services, node.Key())
	tt.Frame()
	if s.sessions.DatabaseOpened(p.ID, "kv") || w.Cache["kv"] != nil {
		t.Fatal("single selection opened database implicitly")
	}
	s.submitNavigation(services, node)
	etcdPublicWait(t, tt, func() bool { return w.Pending == "" && w.Cache["kv"] != nil })
	children := s.navigation.Children(node.Key())
	if len(children) != 1 || navigation.Parse(children[0]).Category != "键" {
		t.Fatal("public KV navigator exposed unsupported SQL categories", children)
	}
	for _, child := range s.navigation.Children(navigation.ConnectionKey(p.ID)) {
		if navigation.Parse(child).Category == "查询" {
			t.Fatal("public KV connection exposed saved SQL queries")
		}
	}
	object := contract.NamespaceRef{ProfileID: p.ID, Catalog: "kv"}.Object("keyspace", "keys")
	if len(w.Cache["kv"]) != 1 || w.Cache["kv"][0].Ref != object {
		t.Fatal("public keyspace identity lost")
	}
	s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: p.ID, Database: "kv", Category: "键", Object: "keys"})
	d := s.currentTab().data
	if d == nil || d.Object() != object {
		t.Fatal("public browser did not open the sole qualified shared grid")
	}
	etcdPublicWait(t, tt, func() bool { return !d.Busy() && !d.Loading() })
	if d.Error() != "" || d.Page() == nil || len(d.Page().Rows) != 0 {
		t.Fatal("empty original shared grid", d.Error())
	}
	provider, err := s.runtime.KVRequests(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := provider.ReserveKV(999)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	prefix, _ := connections.EtcdPrefixBytes(p.EtcdOptions)
	key := append(bytes.Clone(prefix), 0, 255, 1)
	destination := append(bytes.Clone(prefix), 0, 255, 2)
	lease := etcdPublicDo(t, admin, &pb.LeaseGrantRequest{TTL: 90}).Reply.Lease.ID
	defer etcdPublicDo(t, admin, &pb.LeaseRevokeRequest{ID: lease})
	preview := func() {
		etcdPublicClick(t, tt, "保存数据更改")
		etcdPublicWait(t, tt, func() bool { _, ok := tt.Find(locales.Text("确认并提交更改")); return ok })
	}
	confirm := func() {
		etcdPublicClick(t, tt, "确认并提交更改")
		etcdPublicWait(t, tt, func() bool { return !d.Busy() && !d.Loading() })
	}
	check := func(key, value []byte, expectedLease int64) {
		t.Helper()
		result := etcdPublicDo(t, admin, &pb.RangeRequest{Key: key})
		if len(result.Reply.Entries) != 1 || !bytes.Equal(result.Reply.Entries[0].Value, value) || result.Reply.Entries[0].Lease != expectedLease {
			t.Fatal("public grid changed exact native value/lease")
		}
	}
	etcdPublicClick(t, tt, "添加记录")
	s.dataWorkspace.EditCell(services, d, 0, "key", contract.Cell{Kind: contract.Binary, Bytes: key})
	s.dataWorkspace.EditCell(services, d, 0, "value", contract.Cell{Kind: contract.Binary, Bytes: []byte{0, 255, 1, 0}})
	s.dataWorkspace.EditCell(services, d, 0, "lease", contract.Cell{Kind: contract.Integer, Text: strconv.FormatInt(lease, 10)})
	preview()
	captureEtcdPublic(t, s, "insert-preview")
	if result := etcdPublicDo(t, admin, &pb.RangeRequest{Key: key}); len(result.Reply.Entries) != 0 {
		t.Fatal("insert preview wrote before confirmation")
	}
	confirm()
	check(key, []byte{0, 255, 1, 0}, lease)
	if d.HasDraft() || len(d.Page().Rows) != 1 || d.Page().RowRefs[0] == nil {
		t.Fatal("confirmed insert did not generate native identity")
	}
	s.dataWorkspace.EditCell(services, d, 0, "value", contract.Cell{Kind: contract.Binary})
	preview()
	etcdPublicClick(t, tt, "取消")
	if !d.HasDraft() {
		t.Fatal("preview cancel discarded original draft")
	}
	check(key, []byte{0, 255, 1, 0}, lease)
	preview()
	confirm()
	check(key, nil, lease)
	s.dataWorkspace.EditCell(services, d, 0, "lease", contract.Cell{Kind: contract.Integer, Text: "0"})
	preview()
	confirm()
	check(key, nil, 0)
	s.dataWorkspace.EditCell(services, d, 0, "key", contract.Cell{Kind: contract.Binary, Bytes: destination})
	preview()
	captureEtcdPublic(t, s, "rename-preview")
	confirm()
	check(destination, nil, 0)
	if result := etcdPublicDo(t, admin, &pb.RangeRequest{Key: key}); len(result.Reply.Entries) != 0 {
		t.Fatal("rename left original source")
	}
	etcdPublicClick(t, tt, "原生事务回执")
	etcdPublicClick(t, tt, "复制完整回执")
	var receipt contract.KVResult
	if json.Unmarshal([]byte(tt.Clipboard()), &receipt) != nil || !receipt.Completed || receipt.Reply == nil || !receipt.Reply.Succeeded || len(receipt.Native) == 0 {
		t.Fatal("public exact native Txn receipt unavailable")
	}
	captureEtcdPublic(t, s, "receipt")
	etcdPublicClick(t, tt, "关闭")
	etcdPublicClick(t, tt, "删除记录")
	preview()
	confirm()
	if result := etcdPublicDo(t, admin, &pb.RangeRequest{Key: destination}); len(result.Reply.Entries) != 0 || d.HasDraft() || len(d.Page().Rows) != 0 {
		t.Fatal("confirmed atomic delete did not refresh original grid")
	}
	captureEtcdPublic(t, s, "empty-grid")
	// A dirty data document owns disconnect approval, and cancel keeps the
	// original public session and document alive.
	etcdPublicClick(t, tt, "添加记录")
	s.dataWorkspace.EditCell(services, d, 0, "key", contract.Cell{Kind: contract.Binary, Bytes: key})
	s.disconnectMySQLProfile(p.ID)
	tt.Frame()
	etcdPublicClick(t, tt, "取消")
	if !s.runtime.IsConnected(p.ID) || !d.HasDraft() || s.currentTab().data != d {
		t.Fatal("canceled original disconnect lost dirty grid")
	}
	// Use the actual workbench exit guard for the retained draft.
	if !s.RequestWorkbenchClose(func() {}) {
		t.Fatal("public exit ignored dirty key document")
	}
	tt.Frame()
	captureEtcdPublic(t, s, "dirty-guard")
}

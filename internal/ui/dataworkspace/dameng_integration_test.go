package dataworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestDamengNativeFileImportAndCompleteExportWizards(t *testing.T) {
	p, password := damengUIFixture(t)
	p.ID = 1210
	m := dbruntime.New()
	defer m.Close()
	dbruntime.RegisterBuiltins(m, nil)
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	if err := m.Connect(ctx, p, password); err != nil {
		t.Fatal(err)
	}
	session, err := m.Session(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := contract.Require[contract.QueryProvider](session)
	if err != nil {
		t.Fatal(err)
	}
	l, err := provider.ReserveQuery(99)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	name := fmt.Sprintf("NYADB_UI_TRANSFER_DM_%d", time.Now().UnixNano())
	if _, err = l.Execute(ctx, "SYSDBA", "CREATE TABLE "+name+"(ID BIGINT PRIMARY KEY,N DECIMAL(38,20),T VARCHAR(128)); INSERT INTO "+name+" VALUES(1,1.25,'original')"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if _, err := l.Execute(cleanup, "SYSDBA", "DROP TABLE "+name); err != nil {
			t.Error(err)
		}
	}()
	state := Session{ProfileID: p.ID, Generation: session.Generation(), Connected: true}
	s := New(Dependencies{Session: func() Session { return state }, Runtime: func() Runtime { return m.Data(p.ID) }})
	d := s.OpenObject(1, m.Namespace(p.ID, "SYSDBA").Object("table", name), session.Capabilities())
	defer s.Close(d)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) {
		services = c.Services()
		s.Consume(services)
		theme.Apply(c)
		s.View(c, d)
		s.Modals(c)
	}, 1280, 850)
	wait := func(done func() bool) {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); !done() && time.Now().Before(deadline); {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		if !done() {
			t.Fatal("native wizard did not settle", d.err, d.transfer.Err)
		}
		tt.Frame()
	}
	s.Load(services, d, false)
	wait(func() bool { return !d.loading })
	if d.page == nil || d.err != "" {
		t.Fatal("native wizard grid", d.err)
	}
	path := filepath.Join(t.TempDir(), "source.csv")
	if err := os.WriteFile(path, []byte("ID,N,T\n2,2.00000000000000000001,imported 中文\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("导入"); err != nil {
		t.Fatal(err)
	}
	d.transfer.OpenDialog = func(mygo.OpenDialogOptions) ([]string, error) { return []string{path}, nil }
	tt.Frame()
	if err := tt.Click("选择文件"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !d.transfer.Busy && d.transfer.Input != nil })
	for i := 0; i < 4; i++ {
		if err := tt.Click("下一步"); err != nil {
			t.Fatal("native import step", i, err)
		}
	}
	if err := tt.Click("完整校验全部源记录"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !d.transfer.Busy && d.transfer.MappedSpool != nil })
	if err := tt.Click("输入表名称以确认导入"); err != nil {
		t.Fatal(err)
	}
	tt.Type(name)
	if err := tt.Click("确认导入"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !d.transfer.Busy && d.transfer.Submitted })
	if !d.transfer.Completed || d.transfer.Uncertain {
		t.Fatal("native file import not confirmed", d.transfer.Err)
	}
	if out, err := l.Execute(ctx, "SYSDBA", "SELECT N,T FROM "+name+" WHERE ID=2"); err != nil || len(out.Rows) != 1 || out.Rows[0][0] != "2.00000000000000000001" || out.Rows[0][1] != "imported 中文" {
		t.Fatal("native imported file lost exact value", err)
	}
	if err := tt.Click("关闭"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !d.transfer.Open && !d.loading })
	if err := tt.Click("导出"); err != nil {
		t.Fatal(err)
	}
	d.transfer.Scope = "all"
	for i := 0; i < 3; i++ {
		if err := tt.Click("下一步"); err != nil {
			t.Fatal("native export step", i, err)
		}
	}
	output := filepath.Join(t.TempDir(), "export.csv")
	if err := tt.Click("目标文件路径"); err != nil {
		t.Fatal(err)
	}
	tt.Type(output)
	if err := tt.Click("导出"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !d.transfer.Busy && d.transfer.Completed })
	data, err := os.ReadFile(output)
	if err != nil || !strings.Contains(string(data), "2.00000000000000000001") || !strings.Contains(string(data), "imported 中文") || !tt.HasText("导出完成") {
		t.Fatal("complete native file export", err)
	}
	if root := os.Getenv("NYADB_DAMENG_VISUAL_DIR"); root != "" {
		if err := os.MkdirAll(root, 0755); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(root, "dameng-live-export.png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, tt.Image()); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// Only the explicit disposable fixture runner supplies this private file.
// The password remains in a local file, never a command-line argument.
func damengUIFixture(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_DAMENG_DPI_IT") != "1" {
		t.Skip("requires owned DM8 fixture")
	}
	data, err := os.ReadFile(os.Getenv("NYADB_DAMENG_DPI_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Endpoint     string `json:"endpoint"`
		PasswordFile string `json:"password_file"`
		SSL          string `json:"native_ssl_directory"`
	}
	if err = json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(f.Endpoint)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("captured loopback fixture required")
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		t.Fatal("explicit native fixture port required")
	}
	secret, err := os.ReadFile(f.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := connections.Defaults(connections.Dameng)
	p.Name, p.Host, p.Port, p.TLSMode = "Dameng fixture", host, uint16(n), "disable"
	a := &p.DamengOptions
	a.LibraryFile = os.Getenv("NYADB_DAMENG_DPI_LIBRARY")
	a.MaxConnections, a.SQLMaxConnections = 2, 3
	if f.SSL != "" {
		p.TLSMode = "verify-full"
		a.CAFile = filepath.Join(f.SSL, "ca-cert.pem")
		a.CertificateFile = filepath.Join(f.SSL, "client-cert.pem")
		a.KeyFile = filepath.Join(f.SSL, "client-key.pem")
	}
	p, err = connections.NormalizeProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	return p, string(secret)
}

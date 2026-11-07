package connectioneditor

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestDamengURIImportsIndependentNativeProfileAndTemporarySecret(t *testing.T) {
	s, _, _, _ := profileTestState(t)
	q := url.Values{"library_file": {filepath.Join(t.TempDir(), "libdmdpi.so")}, "sslmode": {"disable"}, "schema": {` Schema " `}}
	u := url.URL{Scheme: "dameng", Host: "localhost", Path: "/DAMENG", User: url.UserPassword("SYSDBA", "temporary-secret"), RawQuery: q.Encode()}
	if err := s.ImportURI(u.String()); err != nil {
		t.Fatal(err)
	}
	if !s.active || s.URIActive() || s.connectionDraft.Kind != connections.Dameng || s.connectionSecret != "temporary-secret" || s.connectionSavePassword || s.connectionDraft.DamengOptions.DefaultSchema != q.Get("schema") {
		t.Fatal("native URI lost identity, settings or temporary credential")
	}
	s.BeginURIFromEditor()
	before := s.connectionDraft
	if err := s.ImportURI("dameng://SYSDBA:other@localhost/DAMENG"); err == nil {
		t.Fatal("missing native client accepted")
	}
	if !reflect.DeepEqual(s.connectionDraft, before) || s.connectionSecret != "temporary-secret" || !s.URIActive() {
		t.Fatal("invalid URI replaced the parked draft")
	}
	s.ReturnFromURI()
	if !s.active || s.URIActive() || !reflect.DeepEqual(s.connectionDraft, before) {
		t.Fatal("native editor could not resume")
	}
}

func TestDamengEditorPersistsNativeSettingsAndVaultRollback(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.Dameng, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name, s.connectionSecret = "Dameng vault", "private-dameng"
	s.connectionSavePassword = true
	dir := t.TempDir()
	a := &s.connectionDraft.DamengOptions
	a.LibraryFile, a.CAFile = filepath.Join(dir, "libdmdpi.so"), filepath.Join(dir, "ca.pem")
	a.CertificateFile, a.KeyFile = filepath.Join(dir, "client.pem"), filepath.Join(dir, "key.pem")
	a.DefaultSchema = ` Native " schema `
	s.engineNumbers[0], s.engineNumbers[1], s.engineNumbers[2] = "3", "2", "7"
	s.sqlMaxConnections = "4"
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatal("native profile save", err, s.Error())
	}
	p := profiles[0]
	s.OpenProfile(p)
	if s.Title() != "编辑连接 (达梦)" || s.connectionSecret != "private-dameng" || s.engineNumbers[0] != "3" || s.engineNumbers[1] != "2" || s.engineNumbers[2] != "7" || s.sqlMaxConnections != "4" || s.connectionDraft.DamengOptions != p.DamengOptions {
		t.Fatal("native reopen lost identity, settings or credential")
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.State.View(c, 900, 700) }, 900, 700)
	if err = tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"达梦原厂客户端库", "达梦默认模式", "执行超时（秒）", "后台连接上限", "SQL 会话上限"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatal("missing native setting", label)
		}
	}
	if err = tt.Click("SSL"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"达梦 TLS 模式", "达梦 CA 证书", "达梦客户端证书", "达梦客户端密钥", "达梦证书服务器名"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatal("missing native TLS setting", label)
		}
	}
	vault.setErr = errors.New("locked")
	s.connectionDraft.Host, s.connectionSecret = "changed.example", "replacement"
	s.Save()
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.Host != p.Host || got.Database != p.Database || got.DamengOptions != p.DamengOptions || s.Error() == "" {
		t.Fatal("vault failure changed the stored native profile", err)
	}
	vault.setErr = nil
	s.OpenProfile(p)
	if s.connectionSecret != "private-dameng" {
		t.Fatal("vault rollback lost original credential")
	}
	s.SetEngineFilePicker(func(EngineFile, int, uint64) {})
	version := s.EngineFileVersion()
	s.SelectEngineFile(DamengLibraryFile, -1, version, filepath.Join(dir, "selected.so"))
	if s.connectionDraft.DamengOptions.LibraryFile != filepath.Join(dir, "selected.so") {
		t.Fatal("native client picker ignored current draft")
	}
	s.Close()
	s.OpenProfile(p)
	s.SelectEngineFile(DamengLibraryFile, -1, version, filepath.Join(dir, "stale.so"))
	if s.connectionDraft.DamengOptions.LibraryFile != p.DamengOptions.LibraryFile {
		t.Fatal("late picker changed a reopened native draft")
	}
	if err := s.OpenNew(connections.Dameng, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name, s.connectionSecret = "failed new native profile", "unsaved-secret"
	s.connectionDraft.DamengOptions.LibraryFile = p.DamengOptions.LibraryFile
	s.connectionDraft.TLSMode = "disable"
	s.connectionSavePassword = true
	vault.setErr = errors.New("locked")
	s.Save()
	profiles, err = service.List(context.Background())
	if err != nil || len(profiles) != 1 || profiles[0].ID != p.ID || s.Error() == "" {
		t.Fatal("credential failure left a new native profile", err)
	}
}

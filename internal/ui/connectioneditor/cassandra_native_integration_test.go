package connectioneditor

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	cassandrasvc "github.com/nyakang/nyadb/internal/services/cassandra"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func cassandraNativeProfileConfig(t *testing.T) (connections.CassandraConfig, string) {
	t.Helper()
	if os.Getenv("NYADB_CASSANDRA_IT") != "1" || os.Getenv("NYADB_CASSANDRA_PROFILE_IT") != "1" {
		t.Skip("requires owned native Cassandra profile fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_CASSANDRA_CONFIG"))
	if err != nil {
		t.Fatal("private fixture config unavailable")
	}
	var cfg struct {
		Endpoint, Auth, Username, TLSMode string
		CAFile                            string `json:"ca_file"`
		ServerName                        string `json:"server_name"`
		PasswordFile                      string `json:"password_file"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		t.Fatal("private fixture config invalid")
	}
	secret, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		t.Fatal("private fixture credential unavailable")
	}
	return connections.CassandraConfig{Endpoint: cfg.Endpoint, Auth: cfg.Auth, Username: cfg.Username, TLSMode: cfg.TLSMode, CAFile: cfg.CAFile, ServerName: cfg.ServerName}, string(secret)
}

func nativeCassandraQuery(t *testing.T, c *cassandrasvc.Client, statement string) *cassandrasvc.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := c.Execute(ctx, cassandrasvc.Request{Statement: statement})
	if err != nil {
		t.Fatal("native fixture request failed", err)
	}
	return r
}

func TestCassandraNativeProfileURIReopenAndOriginalEditorCheck(t *testing.T) {
	cfg, secret := cassandraNativeProfileConfig(t)
	originalConfig := cfg
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, err := cassandrasvc.Open(ctx, cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	keyspace := "NyaDB_" + hex.EncodeToString(id[:])
	nativeCassandraQuery(t, admin, "CREATE KEYSPACE \""+keyspace+"\" WITH replication={'class':'SimpleStrategy','replication_factor':1}")
	defer nativeCassandraQuery(t, admin, "DROP KEYSPACE \""+keyspace+"\"")
	nativeCassandraQuery(t, admin, "CREATE TABLE \""+keyspace+"\".proof (id int PRIMARY KEY,value bigint)")
	nativeCassandraQuery(t, admin, "INSERT INTO \""+keyspace+"\".proof(id,value) VALUES(1,9007199254740993)")
	t.Run("configured_principal", func(t *testing.T) { cassandraNativeProfileReopen(t, cfg, secret, keyspace) })
	if cfg.Auth == "password" {
		// This exact quoted role and padded password prove that SASL PLAIN does
		// not have the trim behavior used by some HTTP authentication plugins.
		role := " native 用户 " + hex.EncodeToString(id[:]) + " "
		password := " " + secret + " "
		if strings.Contains(password, "'") {
			t.Fatal("owned fixture password must be quote-free")
		}
		nativeCassandraQuery(t, admin, "CREATE ROLE \""+role+"\" WITH PASSWORD='"+password+"' AND LOGIN=true")
		defer nativeCassandraQuery(t, admin, "DROP ROLE \""+role+"\"")
		nativeCassandraQuery(t, admin, "GRANT SELECT ON KEYSPACE \""+keyspace+"\" TO \""+role+"\"")
		cfg.Username = role
		t.Run("exact_padded_unicode_principal", func(t *testing.T) { cassandraNativeProfileReopen(t, cfg, password, keyspace) })
	}
	bad := originalConfig
	bad.Keyspace = "Missing_" + hex.EncodeToString(id[:])
	if c, err := cassandrasvc.Open(ctx, bad, secret); err == nil {
		c.Close()
		t.Fatal("missing initial Keyspace accepted")
	} else {
		var native *cassandrasvc.ServerError
		if !errors.As(err, &native) || native.Code != 0x2200 {
			t.Fatal("missing initial Keyspace failed outside the native namespace boundary", err)
		}
	}
}

func cassandraNativeProfileReopen(t *testing.T, cfg connections.CassandraConfig, secret, keyspace string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	u := url.URL{Scheme: "cassandra", Host: cfg.Endpoint, Path: "/" + keyspace}
	q := url.Values{"auth": {cfg.Auth}, "sslmode": {cfg.TLSMode}}
	if cfg.CAFile != "" {
		q.Set("ca_file", cfg.CAFile)
		q.Set("server_name", "localhost")
	}
	if cfg.Auth == "password" {
		u.User = url.UserPassword(cfg.Username, secret)
	}
	u.RawQuery = q.Encode()
	parsed, err := connections.ParseCassandraURI(u.String())
	if err != nil || parsed.Password != secret || parsed.Profile.Database != keyspace || parsed.Profile.Username != cfg.Username {
		t.Fatal("native pure URI lost identity", err)
	}
	path := filepath.Join(t.TempDir(), "config.sqlite")
	db, err := localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if db != nil {
			db.Close()
		}
	}()
	service := connections.NewService(localdb.NewProfileStore(db))
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	var closed atomic.Int32
	probe := func(ctx context.Context, p connections.Profile, secret string) error {
		config, err := connections.CassandraProfileConfig(p)
		if err != nil {
			return err
		}
		c, err := cassandrasvc.Open(ctx, config, secret)
		if err != nil {
			return err
		}
		defer c.Close()
		r, err := c.Execute(ctx, cassandrasvc.Request{Statement: "SELECT value FROM proof WHERE id=1"})
		if err != nil {
			return err
		}
		if c.Identity().ReleaseVersion != "5.0.6" || len(r.Rows) != 1 || r.Rows[0][0].Text != "9007199254740993" || r.Columns[0].Keyspace != keyspace {
			return errors.New("native initial Keyspace or precise value lost")
		}
		c.Close()
		if c.Alive() {
			return errors.New("original test client remained open")
		}
		closed.Add(1)
		return nil
	}
	s := New(Dependencies{Profiles: service, Vault: vault, TestConnection: probe})
	defer s.Close()
	s.OpenProfile(parsed.Profile)
	s.connectionSecret, s.connectionSavePassword = secret, cfg.Auth == "password"
	s.Save()
	if s.Error() != "" || s.Active() {
		t.Fatal("native editor save failed", s.Error())
	}
	profiles, err := service.List(ctx)
	if err != nil || len(profiles) != 1 {
		t.Fatal("saved profile missing")
	}
	copy, err := service.Duplicate(ctx, profiles[0].ID, "private native copy")
	if err != nil || copy.CassandraOptions != profiles[0].CassandraOptions {
		t.Fatal("copy changed native options")
	}
	if _, found, _ := vault.Get(copy.ID, ""); found {
		t.Fatal("copy duplicated credential")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	service = connections.NewService(localdb.NewProfileStore(db))
	p, err := service.Get(ctx, profiles[0].ID)
	if err != nil || p.Database != keyspace || p.Username != cfg.Username {
		t.Fatal("restart changed native identity", err)
	}
	s = New(Dependencies{Profiles: service, Vault: vault, TestConnection: probe})
	defer s.Close()
	s.OpenProfile(p)
	if s.connectionSecret != secret || s.connectionSavePassword != (cfg.Auth == "password") {
		t.Fatal("restart lost separate credential")
	}
	tt := cassandraEditorTester(s)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for s.connectionTesting && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if s.connectionTesting || s.Error() != "" || closed.Load() != 1 || !strings.Contains(s.connectionTestMessage, "已关闭测试会话") {
		t.Fatal("native editor check did not close original client", s.Error())
	}
	public, _ := json.Marshal(p)
	if secret != "" && strings.Contains(string(public), secret) {
		t.Fatal("credential entered public profile")
	}
	config, err := connections.CassandraProfileConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	other, err := cassandrasvc.Open(ctx, config, secret)
	if err != nil {
		t.Fatal("independent native reopen failed", err)
	}
	defer other.Close()
	if r := nativeCassandraQuery(t, other, "SELECT value FROM proof WHERE id=1"); r.Columns[0].Keyspace != keyspace || r.Rows[0][0].Text != "9007199254740993" {
		t.Fatal("physical reopen lost original namespace")
	}
}

func TestCassandraNativeEditorCanceledOriginalHandshake(t *testing.T) {
	cfg, secret := cassandraNativeProfileConfig(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seen, ended := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(ended)
		front, err := listener.Accept()
		if err != nil {
			return
		}
		defer front.Close()
		var back net.Conn
		if cfg.TLSMode == "disable" {
			back, err = (&net.Dialer{}).DialContext(ctx, "tcp", cfg.Endpoint)
		} else {
			raw, e := os.ReadFile(cfg.CAFile)
			if e != nil {
				return
			}
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(raw) {
				return
			}
			host, _, _ := net.SplitHostPort(cfg.Endpoint)
			back, err = (&tls.Dialer{NetDialer: &net.Dialer{}, Config: &tls.Config{RootCAs: roots, ServerName: host, MinVersion: tls.VersionTLS12}}).DialContext(ctx, "tcp", cfg.Endpoint)
		}
		if err != nil {
			return
		}
		defer back.Close()
		stop := context.AfterFunc(ctx, func() { front.Close(); back.Close() })
		defer stop()
		var header [9]byte
		if _, err = io.ReadFull(front, header[:]); err != nil || header[4] != 5 || binary.BigEndian.Uint32(header[5:]) != 0 {
			return
		}
		if _, err = back.Write(header[:]); err != nil {
			return
		}
		if _, err = io.ReadFull(back, header[:]); err != nil || header[0] != 0x84 || header[4] != 6 {
			return
		}
		n := binary.BigEndian.Uint32(header[5:])
		if n > cassandrasvc.MaxFrame {
			return
		}
		if _, err = io.CopyN(io.Discard, back, int64(n)); err != nil {
			return
		}
		close(seen)
		_, _ = io.Copy(io.Discard, front)
	}()
	defer func() {
		cancel()
		listener.Close()
		select {
		case <-ended:
		case <-time.After(5 * time.Second):
			t.Error("original proxy handshake worker not closed")
		}
	}()
	var acknowledged atomic.Bool
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
		config, err := connections.CassandraProfileConfig(p)
		if err != nil {
			return err
		}
		c, err := cassandrasvc.Open(ctx, config, secret)
		if c != nil {
			c.Close()
		}
		acknowledged.Store(true)
		return err
	}})
	defer s.Close()
	_ = s.OpenNew(connections.Cassandra, "")
	s.connectionDraft.Name = "original handshake"
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	n, _ := strconv.Atoi(port)
	s.connectionDraft.Host, s.connectionDraft.Port = host, uint16(n)
	s.connectionPort = port
	s.connectionDraft.TLSMode = "disable"
	if cfg.Auth == "password" {
		s.setCassandraAuth("password")
		s.connectionDraft.Username = cfg.Username
		s.connectionSecret = secret
	}
	tt := cassandraEditorTester(s)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-seen:
	case <-time.After(10 * time.Second):
		t.Fatal("native OPTIONS response not observed")
	}
	s.Cancel()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not close original physical handshake")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !acknowledged.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	tt.Frame()
	if !acknowledged.Load() || s.connectionTesting || s.connectionTestResults != nil || s.connectionSecret != "" {
		t.Fatal("original canceled check survived editor close")
	}
}

func TestCassandraNativeEditorLocalizedFormEvidence(t *testing.T) {
	_, _ = cassandraNativeProfileConfig(t)
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{900, 700}, {720, 520}} {
			for _, auth := range []string{"none", "password"} {
				s := New(Dependencies{Vault: &memoryCredentialVault{secrets: map[string]string{}}})
				_ = s.OpenNew(connections.Cassandra, "")
				s.connectionDraft.Name = "Apache Cassandra"
				tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, float32(size[0]), float32(size[1])) }, size[0], size[1])
				if auth == "password" {
					if err := tt.Click(locales.Text("Cassandra 认证方式")); err != nil {
						t.Fatal(err)
					}
					if err := tt.Click(locales.Text("用户名和密码")); err != nil {
						t.Fatal(err)
					}
					tt.Frame()
				}
				if s.connectionDraft.CassandraOptions.Auth != auth {
					t.Fatal("localized auth selection changed value")
				}
				for _, name := range []string{"Cassandra 初始 Keyspace", "Cassandra 认证方式", "保存连接", "取消连接向导"} {
					assertCassandraControlVisible(t, tt, name, size)
				}
				prefix := language + "-" + strconv.Itoa(size[0]) + "-" + auth
				cassandraFormImage(t, tt, prefix+"-general")
				if err := tt.Click("SSL"); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"Cassandra TLS 模式", "Cassandra CA 证书", "证书服务器名", "保存连接", "取消连接向导"} {
					assertCassandraControlVisible(t, tt, name, size)
				}
				cassandraFormImage(t, tt, prefix+"-ssl")
				s.Close()
			}
		}
	}
}

func assertCassandraControlVisible(t *testing.T, tt *ui.Tester, name string, size [2]int) {
	t.Helper()
	r, ok := tt.Find(locales.Text(name))
	if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
		t.Fatal("localized native form control clipped", name)
	}
}

func cassandraFormImage(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	folder := os.Getenv("NYADB_CASSANDRA_PROFILE_IMAGES")
	if folder == "" {
		return
	}
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(folder, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(file, tt.Image())
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal("form evidence write failed")
	}
}

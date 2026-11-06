package connectioneditor

import (
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestRedisSentinelEditorSeparatesVaultSecretsAndClearsDrafts(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.Redis, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "Sentinel saved"
	s.connectionDraft.Port = 26379
	s.connectionPort = "26379"
	s.connectionDraft.RedisOptions.Topology = "sentinel"
	s.connectionDraft.RedisOptions.MasterName = "mymaster"
	s.connectionDraft.RedisOptions.Seeds = "127.0.0.1:26380"
	s.connectionSecret, s.connectionSavePassword = "data-password", true
	s.redisSentinelPassword, s.redisSaveSentinelPassword = "sentinel-password", true
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatal(err, s.Error())
	}
	p := profiles[0]
	for key, want := range map[string]string{"": "data-password", connections.RedisSentinelCredential: "sentinel-password"} {
		got, found, err := vault.Get(p.ID, key)
		if err != nil || !found || got != want {
			t.Fatal("independent vault entry lost", err)
		}
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "password") {
		t.Fatal("profile persisted secret")
	}
	s.OpenProfile(p)
	if s.redisSentinelPassword != "sentinel-password" || s.connectionSecret != "data-password" {
		t.Fatal("vault reopen lost separate secrets")
	}
	s.redisSaveSentinelPassword = false
	s.Save()
	if _, found, _ := vault.Get(p.ID, connections.RedisSentinelCredential); found {
		t.Fatal("explicit Sentinel password removal ignored")
	}
	if got, found, _ := vault.Get(p.ID, ""); !found || got != "data-password" {
		t.Fatal("Sentinel removal erased data credential")
	}
	s.OpenProfile(p)
	s.redisSentinelPassword = "temporary"
	s.Back()
	if s.redisSentinelPassword != "" || s.redisSaveSentinelPassword {
		t.Fatal("temporary Sentinel credential survived closing editor")
	}
}

func TestRedisSentinelEditorRendersTopologyAndIndependentTLS(t *testing.T) {
	s, _, _, _ := profileTestState(t)
	if err := s.OpenNew(connections.Redis, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.RedisOptions.Topology = "sentinel"
	s.connectionDraft.RedisOptions.SentinelTLSMode = "disable"
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.State.View(c, 820, 740) }, 820, 740)
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	setEngineOptionInput(t, tt, "Sentinel 主节点名称", "mymaster")
	setEngineOptionInput(t, tt, "Redis 附加节点", "127.0.0.1:26380,[::1]:26381")
	setEngineOptionInput(t, tt, "Sentinel 用户名", "monitor")
	setEngineOptionInput(t, tt, "Sentinel 密码", "temporary-secret")
	captureRedisEditor(t, tt, "redis-sentinel-advanced")
	if err := tt.Click("SSL"); err != nil {
		t.Fatal(err)
	}
	setEngineOptionInput(t, tt, "Sentinel CA 证书", "/tmp/sentinel.crt")
	captureRedisEditor(t, tt, "redis-sentinel-tls")
	if s.redisSentinelPassword != "temporary-secret" || s.connectionDraft.RedisOptions.MasterName != "mymaster" || s.connectionDraft.RedisOptions.SentinelCAFile != "/tmp/sentinel.crt" {
		t.Fatal("Sentinel form inputs lost")
	}
}

func captureRedisEditor(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	if dir := os.Getenv("NYADB_REDIS_CAPTURE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err = png.Encode(f, tt.Image()); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRedisSentinelConnectionCheckCapturesAndRedactsBothSecrets(t *testing.T) {
	started := make(chan string, 1)
	finish := make(chan struct{})
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, password string) error {
		started <- connections.RedisSentinelPassword(ctx)
		<-finish
		return errors.New(password + " and " + connections.RedisSentinelPassword(ctx))
	}})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.Redis, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "Sentinel check"
	s.connectionDraft.RedisOptions.Topology = "sentinel"
	s.connectionDraft.RedisOptions.MasterName = "mymaster"
	s.connectionSecret, s.redisSentinelPassword = "data-secret", "sentinel-secret"
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, 720, 520) }, 720, 520)
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	select {
	case secret := <-started:
		if secret != "sentinel-secret" {
			t.Fatal("wrong captured Sentinel secret")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("connection check did not start")
	}
	s.redisSentinelPassword = "changed-after-dispatch"
	close(finish)
	for until := time.Now().Add(3 * time.Second); s.connectionTesting && time.Now().Before(until); {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if s.connectionTesting || strings.Contains(s.Error(), "data-secret") || strings.Contains(s.Error(), "sentinel-secret") || !strings.Contains(s.Error(), "[已隐藏]") {
		t.Fatal("connection check exposed secrets", s.Error())
	}
}

func TestRedisEditorPreservesAuthenticationAndPoolSettings(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	if err := s.OpenNew(connections.Redis, ""); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.State.View(c, 720, 520) }, 720, 520)
	setEngineOptionInput(t, tt, "连接名称", "Redis saved")
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	setEngineOptionInput(t, tt, "连接超时（秒）", "12")
	setEngineOptionInput(t, tt, "连接池上限", "3")
	setEngineOptionInput(t, tt, "查询会话上限", "4")
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatal(err, s.Error())
	}
	p := profiles[0]
	if p.Kind != connections.Redis || p.RedisOptions.ConnectTimeoutSeconds != 12 || p.RedisOptions.MaxConnections != 3 || p.RedisOptions.QueryMaxConnections != 4 {
		t.Fatal("public Redis options lost")
	}
	s.OpenProfile(p)
	if s.connectionDraft.RedisOptions != p.RedisOptions {
		t.Fatal("reopen lost options")
	}
}

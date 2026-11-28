package connectioneditor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func prestoUnitDriver(t *testing.T) connections.ManagedDriver {
	t.Helper()
	return connections.ManagedDriver{Key: "prestosql", Version: "jdbc-350", SHA256: strings.Repeat("a", 64), Directory: t.TempDir(), Profiles: []string{"prestosql"}}
}
func TestPrestoSQLEditorURIKeepsPinAndParkingKeepsAllSecrets(t *testing.T) {
	s := New(Dependencies{})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.PrestoSQL, "group"); err != nil {
		t.Fatal(err)
	}
	d := prestoUnitDriver(t)
	s.selectPrestoSQLDriver(d)
	s.connectionDraft.Name = "original"
	original := connections.PrestoSQLCredentials{Password: "original", TrustStorePassword: "trust", KeyStorePassword: "key", AccessToken: "token", ExtraCredentials: "a:b"}
	s.restorePrestoCredentials(original)
	s.prestosqlNumbers[0] = "unfinished"
	s.BeginURIFromEditor()
	s.ReturnFromURI()
	if s.prestoCredentials() != original || s.prestosqlNumbers[0] != "unfinished" {
		t.Fatal("URI parking changed raw draft")
	}
	s.BeginURIFromEditor()
	raw := "jdbc:presto://host:8443/memory/default?user=reader&SSL=true&password=new+password&SSLTrustStorePassword=new+trust&SSLKeyStorePassword=new+key&accessToken=new+token&extraCredentials=a:new"
	if _, err := s.parseURI(raw); err != nil {
		t.Fatal("preflight", err)
	}
	if err := s.ImportURI(raw); err != nil {
		t.Fatal(err)
	}
	if s.connectionDraft.PrestoSQLOptions.DriverSHA256 != d.SHA256 || s.connectionDraft.PrestoSQLOptions.DriverDirectory != d.Directory || s.connectionDraft.Name != "original" || s.connectionSavePassword {
		t.Fatal("URI lost explicit package or identity")
	}
	c := s.prestoCredentials()
	if c.Password != "new+password" || c.TrustStorePassword != "new+trust" || c.KeyStorePassword != "new+key" || c.AccessToken != "new+token" || c.ExtraCredentials != "a:new" {
		t.Fatal("URI lost credential component")
	}
	s.Close()
	s.BeginURI("")
	if _, err := s.parseURI(raw); err == nil {
		t.Fatal("global import selected ambiguous product")
	}
	if s.prestoCredentials() != (connections.PrestoSQLCredentials{}) {
		t.Fatal("close retained credentials")
	}
}

func TestPrestoSQLSavedCredentialBundleRollbackAndDeletion(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	t.Cleanup(s.Close)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	s.OpenNew(connections.PrestoSQL, "")
	s.selectPrestoSQLDriver(prestoUnitDriver(t))
	s.connectionDraft.Name = "original"
	s.connectionDraft.TLSMode = "verify-full"
	original := connections.PrestoSQLCredentials{Password: "password", TrustStorePassword: "trust", KeyStorePassword: "key", AccessToken: "token", ExtraCredentials: "a:b"}
	s.restorePrestoCredentials(original)
	s.connectionSavePassword = true
	s.Save()
	list, err := service.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatal("save", err, s.Error())
	}
	p := list[0]
	saved := vault.secrets[fmt.Sprintf("%d/", p.ID)]
	decoded, err := connections.DecodePrestoSQLCredentials(saved)
	if err != nil || decoded != original {
		t.Fatal("vault lost credential bundle", err)
	}
	s.OpenProfile(p)
	if s.prestoCredentials() != original {
		t.Fatal("reopen lost credentials")
	}
	s.connectionDraft.Name = "replacement"
	s.connectionSecret = "replacement"
	vault.setErr = errors.New("locked")
	s.Save()
	got, err := service.Get(context.Background(), p.ID)
	if err != nil || got.Name != p.Name || s.Error() == "" || vault.secrets[fmt.Sprintf("%d/", p.ID)] != saved {
		t.Fatal("vault rollback lost original", err)
	}
	vault.setErr = nil
	s.OpenProfile(got)
	s.connectionSavePassword = false
	s.Save()
	if _, exists := vault.secrets[fmt.Sprintf("%d/", p.ID)]; exists {
		t.Fatal("explicit credential removal ignored")
	}
}

func TestPrestoSQLDriverRemovalUsesOriginalPinAndRejectsLateCompletion(t *testing.T) {
	for _, park := range []bool{false, true} {
		t.Run(fmt.Sprint(park), func(t *testing.T) {
			d := prestoUnitDriver(t)
			started := make(chan connections.ManagedDriver, 1)
			finish := make(chan struct{})
			s := New(Dependencies{DriverPackages: DriverPackages{Remove: func(ctx context.Context, kind connections.Kind, original connections.ManagedDriver) error {
				if kind != connections.PrestoSQL {
					t.Error("wrong product")
				}
				started <- original
				<-finish
				return nil
			}}})
			t.Cleanup(s.Close)
			s.OpenNew(connections.PrestoSQL, "")
			s.selectPrestoSQLDriver(d)
			s.driverWork.packages = []connections.ManagedDriver{d}
			s.removePrestoSQLDriver(d)
			original := s.driverWork.results
			captured := <-started
			if captured.SHA256 != d.SHA256 || captured.Directory != d.Directory {
				t.Fatal("removal did not capture original package")
			}
			if park {
				s.BeginURIFromEditor()
			} else {
				s.connectionDraft.PrestoSQLOptions.DriverSHA256 = strings.Repeat("b", 64)
			}
			close(finish)
			if park {
				select {
				case <-original:
				case <-time.After(time.Second):
					t.Fatal("removal worker did not settle")
				}
				s.consumeDriverWork()
				if s.connectionDraft.PrestoSQLOptions.DriverSHA256 != d.SHA256 {
					t.Fatal("late removal changed parked form")
				}
			} else {
				deadline := time.Now().Add(time.Second)
				for s.DriverBusy() && time.Now().Before(deadline) {
					s.consumeDriverWork()
					time.Sleep(time.Millisecond)
				}
				if s.DriverBusy() || s.connectionDraft.PrestoSQLOptions.DriverSHA256 != strings.Repeat("b", 64) {
					t.Fatal("original removal cleared a replacement pin")
				}
			}
		})
	}
}

func TestPrestoSQLFileCallbacksAreRevokedAndTLSCredentialsRequireEncryption(t *testing.T) {
	s := New(Dependencies{})
	t.Cleanup(s.Close)
	s.OpenNew(connections.PrestoSQL, "")
	s.selectPrestoSQLDriver(prestoUnitDriver(t))
	s.connectionDraft.Name = "original"
	s.connectionSecret = "password"
	if _, err := s.draftProfile(); err == nil {
		t.Fatal("password accepted without HTTPS")
	}
	s.connectionDraft.TLSMode = "verify-full"
	version := s.EngineFileVersion()
	s.BeginURIFromEditor()
	s.SelectEngineFile(PrestoSQLTrustStoreFile, -1, version, "/tmp/late")
	if s.connectionDraft.PrestoSQLOptions.TrustStorePath != "" {
		t.Fatal("late file selection modified parked form")
	}
	s.ReturnFromURI()
	s.SelectEngineFile(PrestoSQLTrustStoreFile, -1, s.EngineFileVersion(), "/tmp/original")
	if s.connectionDraft.PrestoSQLOptions.TrustStorePath != "/tmp/original" {
		t.Fatal("current file selection ignored")
	}
	if _, err := s.draftProfile(); err != nil {
		t.Fatal("valid TLS draft", err)
	}
}

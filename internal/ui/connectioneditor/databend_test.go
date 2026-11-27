package connectioneditor

import (
	"context"
	"errors"
	"fmt"
	"github.com/nyakang/nyadb/locales"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func databendEditorDriver(t *testing.T) connections.ManagedDriver {
	t.Helper()
	return connections.ManagedDriver{Key: "databend", Version: "jdbc-0.4.8-agent-0.1.82", SHA256: strings.Repeat("a", 64), Directory: t.TempDir(), Profiles: []string{"databend"}}
}

func TestDatabendEditorKeepsInvalidRawInputsAndExplicitDriverSelection(t *testing.T) {
	s := New(Dependencies{})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.Databend, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "original"
	if _, err := s.draftProfile(); err == nil {
		t.Fatal("unselected driver accepted")
	}
	d := databendEditorDriver(t)
	s.selectDatabendDriver(d)
	original := s.connectionDraft.DatabendOptions
	for _, change := range []func(*connections.ManagedDriver){
		func(d *connections.ManagedDriver) { d.Key = "trino" },
		func(d *connections.ManagedDriver) { d.Profiles = []string{"trino"} },
		func(d *connections.ManagedDriver) { d.JDBCClass = "foreign.Driver" },
		func(d *connections.ManagedDriver) { d.SHA256 = "implicit-latest" },
	} {
		other := d
		change(&other)
		s.selectDatabendDriver(other)
		if s.connectionDraft.DatabendOptions != original || s.driverWork.message == "" {
			t.Fatal("foreign package replaced original pin")
		}
	}
	for i, raw := range []string{"0", "3601", "-1", "33", "0", "32", "249"} {
		before := s.databendNumbers[i]
		s.databendNumbers[i] = raw
		if _, err := s.draftProfile(); err == nil || s.databendNumbers[i] != raw || s.connectionDraft.DatabendOptions != original {
			t.Fatal("invalid raw input was accepted or erased", i)
		}
		s.databendNumbers[i] = before
	}
	if p, err := s.draftProfile(); err != nil || p.DatabendOptions != original {
		t.Fatal("restored raw options did not validate", err)
	}
}

func TestDatabendEditorVaultRollbackAndAuthSwitchPreserveOriginalProfile(t *testing.T) {
	s, _, service, _ := profileTestState(t)
	vault := &memoryCredentialVault{secrets: map[string]string{}}
	s.SetCredentialVault(vault)
	if err := s.OpenNew(connections.Databend, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "original Databend"
	s.selectDatabendDriver(databendEditorDriver(t))
	s.connectionSecret, s.connectionSavePassword = "original-secret", true
	s.Save()
	profiles, err := service.List(context.Background())
	if err != nil || len(profiles) != 1 || s.Error() != "" {
		t.Fatal("original save failed", err, s.Error())
	}
	original := profiles[0]
	s.OpenProfile(original)
	if s.connectionSecret != "original-secret" || !s.connectionSavePassword {
		t.Fatal("reopen lost saved secret")
	}
	s.connectionDraft.Name, s.connectionSecret = "replacement", "replacement-secret"
	vault.setErr = errors.New("vault locked")
	s.Save()
	got, err := service.Get(context.Background(), original.ID)
	if err != nil || got.Name != original.Name || got.DatabendOptions != original.DatabendOptions || vault.secrets[fmt.Sprintf("%d/", original.ID)] != "original-secret" || s.Error() == "" {
		t.Fatal("vault failure changed original profile")
	}
	vault.setErr = nil
	s.OpenProfile(original)
	s.setDatabendAuth("none")
	if s.connectionSecret != "" || s.connectionDraft.Username != "" || s.connectionSavePassword {
		t.Fatal("auth change retained hidden credentials")
	}
	s.Cancel()
	if vault.secrets[fmt.Sprintf("%d/", original.ID)] != "original-secret" {
		t.Fatal("canceled form changed vault")
	}
	s.OpenProfile(original)
	s.setDatabendAuth("none")
	s.Save()
	if _, found, _ := vault.Get(original.ID, ""); found || s.Error() != "" {
		t.Fatal("anonymous save retained saved secret", s.Error())
	}
}

func TestDatabendEditorCheckCapturesOriginalConfigurationAndCancels(t *testing.T) {
	started, canceled, release, finished := make(chan connections.Profile, 1), make(chan struct{}), make(chan struct{}), make(chan struct{})
	s := New(Dependencies{TestConnection: func(ctx context.Context, p connections.Profile, secret string) error {
		defer close(finished)
		if secret != "original-secret" {
			return errors.New("wrong captured credential")
		}
		started <- p
		<-ctx.Done()
		close(canceled)
		<-release
		return errors.New("late original-secret")
	}})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.Databend, ""); err != nil {
		t.Fatal(err)
	}
	s.connectionDraft.Name = "original"
	s.selectDatabendDriver(databendEditorDriver(t))
	s.setDatabendAuth("basic")
	s.connectionDraft.Username, s.connectionSecret = "ops", "original-secret"
	s.connectionDraft.Database = "original_database"
	tt := zookeeperEditorTester(s)
	if err := tt.Click(locales.Text("测试连接")); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-started:
		if p.Database != "original_database" || p.DatabendOptions.Auth != "basic" || p.Username != "ops" {
			t.Fatal("check did not capture original configuration")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("native check did not start", s.Error())
	}
	chooseConsul(t, tt, "Databend 认证方式", "无认证")
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("auth switch did not cancel captured check")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("original executor did not acknowledge cancellation")
	}
	tt.Frame()
	if s.connectionTesting || s.Error() != "" || s.connectionSecret != "" {
		t.Fatal("late native check changed the new draft")
	}
}

func TestDatabendEditorPackageCompletionAndTrustFilesKeepOriginalForm(t *testing.T) {
	started, release := make(chan context.Context, 1), make(chan struct{})
	d := databendEditorDriver(t)
	s := New(Dependencies{DriverPackages: DriverPackages{Import: func(ctx context.Context, kind connections.Kind, directory, file string) (connections.ManagedDriver, error) {
		if kind != connections.Databend || directory != "" || file != "original.zip" {
			return connections.ManagedDriver{}, errors.New("wrong captured package request")
		}
		started <- ctx
		<-release
		return d, nil
	}}})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.Databend, ""); err != nil {
		t.Fatal(err)
	}
	s.ImportDriverPackage("original.zip")
	var ctx context.Context
	select {
	case ctx = <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("original native package import did not start")
	}
	original := s.driverWork.results
	s.Cancel()
	if err := s.OpenNew(connections.Databend, ""); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("original import context not revoked")
	}
	close(release)
	select {
	case <-original:
	case <-time.After(3 * time.Second):
		t.Fatal("captured package importer did not settle")
	}
	s.consumeDriverWork()
	if s.connectionDraft.DatabendOptions.DriverVersion != "" || len(s.driverWork.packages) != 0 {
		t.Fatal("late native package import selected a different form")
	}
	s.setDatabendTLS("verify-full")
	version := s.EngineFileVersion()
	s.SelectEngineFile(DatabendCertificateFile, -1, version, "/owned/client.crt")
	s.SelectEngineFile(DatabendKeyFile, -1, version, "/owned/client.key")
	if s.connectionDraft.DatabendOptions.ClientCertFile != "/owned/client.crt" || s.connectionDraft.DatabendOptions.ClientKeyFile != "/owned/client.key" {
		t.Fatal("mTLS files incorrectly required digest credentials")
	}
	s.setDatabendTLS("disable")
	s.SelectEngineFile(DatabendCertificateFile, -1, version, "/late/client.crt")
	if s.connectionDraft.DatabendOptions.ClientCertFile != "/owned/client.crt" {
		t.Fatal("stale trust picker altered changed TLS draft")
	}
}

func TestDatabendEditorURIAndTLSKeepOriginalRawDraftAndPin(t *testing.T) {
	s := New(Dependencies{})
	t.Cleanup(s.Close)
	if err := s.OpenNew(connections.Databend, "original-group"); err != nil {
		t.Fatal(err)
	}
	d := databendEditorDriver(t)
	s.selectDatabendDriver(d)
	s.connectionDraft.Name, s.connectionSecret = "original", "original-secret"
	s.databendNumbers[0] = "invalid-original"
	s.BeginURIFromEditor()
	if err := s.ImportURI("databend://ops:private@localhost?warehouse=private"); err == nil {
		t.Fatal("unsupported warehouse silently dropped")
	}
	s.ReturnFromURI()
	if s.connectionDraft.Name != "original" || s.connectionSecret != "original-secret" || s.databendNumbers[0] != "invalid-original" {
		t.Fatal("rejected URI lost raw form")
	}
	s.BeginURIFromEditor()
	if err := s.ImportURI("jdbc:databend://ops:replacement@localhost:8000/original_database"); err != nil {
		t.Fatal(err)
	}
	a := s.connectionDraft.DatabendOptions
	if s.connectionDraft.Name != "original" || s.connectionDraft.GroupName != "original-group" || a.DriverVersion != d.Version || a.DriverSHA256 != d.SHA256 || a.DriverDirectory != d.Directory || s.connectionSecret != "replacement" || s.connectionSavePassword {
		t.Fatal("URI lost pin, metadata or temporary credential")
	}
	s.setDatabendTLS("verify-full")
	version := s.EngineFileVersion()
	s.SelectEngineFile(DatabendCAFile, -1, version, "/owned/ca.pem")
	s.setDatabendTLS("disable")
	s.SelectEngineFile(DatabendCAFile, -1, version, "/late/ca.pem")
	p, err := s.draftProfile()
	if err != nil || p.DatabendOptions.CAFile != "" || s.connectionDraft.DatabendOptions.CAFile != "/owned/ca.pem" {
		t.Fatal("disabled TLS lost raw trust draft or saved ineffective trust", err)
	}
	s.setDatabendTLS("verify-full")
	if p, err := s.draftProfile(); err != nil || p.DatabendOptions.CAFile != "/owned/ca.pem" {
		t.Fatal("restored TLS lost original trust", err)
	}
}

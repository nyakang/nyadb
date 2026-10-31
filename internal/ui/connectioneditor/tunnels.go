package connectioneditor

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/transport"
)

func (s *State) SetTunnelServices(store *credentials.TunnelStore, test func(context.Context, connections.ActiveSettings, string, transport.Secrets) error, validate func(connections.ActiveSettings, string) error) {
	s.tunnelSecrets = store
	s.mysqlTunnelTester = test
	s.validateMySQL = validate
}
func (s *State) tunnelSnapshot() transport.Secrets {
	return transport.Secrets{SSHPassword: s.mysqlSSHPassword, SSHPassphrase: s.mysqlSSHPassphrase, HTTPToken: s.mysqlHTTPPassword, ProxyPassword: s.mysqlHTTPProxyPassword, HTTPKeyPassphrase: s.mysqlHTTPPassphrase}
}
func (s *State) restoreTunnelSnapshot(v transport.Secrets) {
	s.mysqlSSHPassword = v.SSHPassword
	s.mysqlSSHPassphrase = v.SSHPassphrase
	s.mysqlHTTPPassword = v.HTTPToken
	s.mysqlHTTPProxyPassword = v.ProxyPassword
	s.mysqlHTTPPassphrase = v.HTTPKeyPassphrase
}
func (s *State) currentTunnelWasSaved() bool {
	if s.connectionActiveConfig == 0 {
		return s.connectionMainConfig.hadSavedTunnel
	}
	i := s.connectionActiveConfig - 1
	return i >= 0 && i < len(s.connectionOtherConfigs) && s.connectionOtherConfigs[i].hadSavedTunnel
}
func (s *State) loadTunnelSecrets() {
	if s.tunnelSecrets == nil {
		return
	}
	load := func(snap *connectionEditorSnapshot, name string) error {
		if !snap.profile.MySQLAdvanced.SSH.Enabled && !snap.profile.MySQLAdvanced.HTTP.Enabled {
			return nil
		}
		v, saved, err := s.tunnelSecrets.Get(s.connectionDraft.ID, name)
		if err != nil {
			return err
		}
		snap.tunnel = v
		snap.saveTunnel = saved
		snap.hadSavedTunnel = saved
		return nil
	}
	if err := load(&s.connectionMainConfig, ""); err != nil {
		s.connectionError = err.Error()
		s.tunnelSecretsLoadFailed = true
		return
	}
	for i := range s.connectionOtherConfigs {
		if err := load(&s.connectionOtherConfigs[i], s.connectionOtherConfigs[i].name); err != nil {
			s.connectionError = err.Error()
			s.tunnelSecretsLoadFailed = true
			return
		}
	}
	if s.connectionActiveConfig == 0 {
		s.restoreConnectionConfiguration(s.connectionMainConfig)
	} else {
		s.restoreConnectionConfiguration(s.connectionOtherConfigs[s.connectionActiveConfig-1])
	}
}
func (s *State) persistTunnelSecrets(saved, before connections.Profile) error {
	snaps := append([]connectionEditorSnapshot{s.connectionMainConfig}, s.connectionOtherConfigs...)
	if before.ID == 0 {
		snaps = []connectionEditorSnapshot{s.currentConnectionSnapshot()}
	}
	if s.tunnelSecrets == nil {
		for _, snap := range snaps {
			if snap.saveTunnel {
				return errors.New("系统隧道密钥库不可用")
			}
		}
		return nil
	}
	type change struct {
		name         string
		value        transport.Secrets
		save, remove bool
	}
	var changes []change
	for _, snap := range snaps {
		if s.tunnelSecretsLoadFailed && snap.saveTunnel {
			return errors.New("请先解锁隧道密钥库；旧凭据未修改")
		}
		if !snap.profile.MySQLAdvanced.SSH.Enabled && !snap.profile.MySQLAdvanced.HTTP.Enabled && !snap.hadSavedTunnel {
			continue
		}
		changes = append(changes, change{name: snap.name, value: snap.tunnel, save: snap.saveTunnel, remove: snap.hadSavedTunnel && !snap.saveTunnel})
	}
	remaining := map[string]bool{}
	for _, v := range saved.Variants {
		remaining[v.Name] = true
	}
	for _, v := range before.Variants {
		if !remaining[v.Name] && !s.tunnelSecretsLoadFailed {
			changes = append(changes, change{name: v.Name, remove: true})
		}
	}
	type original struct {
		value transport.Secrets
		saved bool
	}
	originals := map[string]original{}
	if !s.tunnelSecretsLoadFailed {
		for _, c := range changes {
			v, stored, err := s.tunnelSecrets.Get(saved.ID, c.name)
			if err != nil {
				return err
			}
			originals[c.name] = original{v, stored}
		}
	}
	applied := 0
	rollback := func(err error) error {
		for i := applied - 1; i >= 0; i-- {
			c := changes[i]
			old := originals[c.name]
			if !old.saved {
				err = errors.Join(err, s.tunnelSecrets.RemoveSaved(saved.ID, c.name))
			}
			err = errors.Join(err, s.tunnelSecrets.Put(saved.ID, c.name, old.value, old.saved))
		}
		return err
	}
	for _, c := range changes {
		applied++
		if c.remove {
			if err := s.tunnelSecrets.RemoveSaved(saved.ID, c.name); err != nil {
				return rollback(err)
			}
		}
		if err := s.tunnelSecrets.Put(saved.ID, c.name, c.value, c.save); err != nil {
			return rollback(err)
		}
	}
	return nil
}

// Capture the database-vault records that the existing save flow will mutate,
// so a later tunnel-vault failure can restore both credential namespaces.
func (s *State) connectionPasswordUndo(saved, before connections.Profile) (func() error, error) {
	if s.credentialVault == nil || s.connectionPasswordLoadFailed {
		return func() error { return nil }, nil
	}
	snaps := append([]connectionEditorSnapshot{s.connectionMainConfig}, s.connectionOtherConfigs...)
	if before.ID == 0 {
		snaps = []connectionEditorSnapshot{s.currentConnectionSnapshot()}
	}
	names := map[string]bool{}
	for _, snap := range snaps {
		if snap.savePassword && snap.secret != "" || snap.hadSavedPassword {
			names[snap.name] = true
		}
	}
	remaining := map[string]bool{}
	for _, v := range saved.Variants {
		remaining[v.Name] = true
	}
	for _, v := range before.Variants {
		if !remaining[v.Name] {
			names[v.Name] = true
		}
	}
	type original struct {
		value string
		found bool
	}
	values := map[string]original{}
	for name := range names {
		value, found, err := s.credentialVault.Get(saved.ID, name)
		if err != nil {
			return nil, err
		}
		values[name] = original{value, found}
	}
	return func() error {
		var err error
		for name, old := range values {
			if old.found {
				err = errors.Join(err, s.credentialVault.Set(saved.ID, name, old.value))
			} else {
				err = errors.Join(err, s.credentialVault.Delete(saved.ID, name))
			}
		}
		return err
	}, nil
}

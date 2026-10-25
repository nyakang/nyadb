package shell

import (
	"errors"
	"fmt"

	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
)

// SetCredentialVault connects the editor and session manager to a native
// password vault. An absent vault never causes plaintext SQLite fallback.
func (s *State) SetCredentialVault(vault credentials.Vault) {
	s.credentialVault = vault
}

// loadSavedConnectionPasswords is called only on opening an existing MySQL
// editor. Values stay in its masked, short-lived drafts; the profile model is
// never given a password.
func (s *State) loadSavedConnectionPasswords() {
	if s.credentialVault == nil || s.connectionDraft.ID == 0 {
		return
	}
	id := s.connectionDraft.ID
	load := func(snap *connectionEditorSnapshot, variant string) error {
		password, found, err := s.credentialVault.Get(id, variant)
		if err != nil {
			return err
		}
		snap.secret = password
		snap.savePassword = found
		snap.hadSavedPassword = found
		return nil
	}
	if err := load(&s.connectionMainConfig, ""); err != nil {
		s.connectionError = "无法读取系统保存的密码：" + err.Error()
		s.connectionPasswordLoadFailed = true
		return
	}
	for i := range s.connectionOtherConfigs {
		snap := &s.connectionOtherConfigs[i]
		if err := load(snap, snap.name); err != nil {
			s.connectionError = "无法读取系统保存的密码：" + err.Error()
			s.connectionPasswordLoadFailed = true
			return
		}
	}
	if s.connectionActiveConfig == 0 {
		s.restoreConnectionConfiguration(s.connectionMainConfig)
	} else {
		s.restoreConnectionConfiguration(s.connectionOtherConfigs[s.connectionActiveConfig-1])
	}
}

// persistConnectionPasswords saves each configuration separately, keyed by
// profile ID and configuration name. Password removal is always explicit:
// unchecking Save removes a previously saved password; cancelling the editor
// never touches the OS vault.
func (s *State) currentCredentialWasSaved() bool {
	if s.connectionDraft.ID == 0 {
		return false
	}
	if s.connectionActiveConfig == 0 {
		return s.connectionMainConfig.hadSavedPassword
	}
	i := s.connectionActiveConfig - 1
	return i >= 0 && i < len(s.connectionOtherConfigs) && s.connectionOtherConfigs[i].hadSavedPassword
}

func (s *State) persistConnectionPasswords(saved, previous connections.Profile) error {
	// A vault read failure must not make an ordinary host/port edit erase
	// previously saved credentials.
	if s.connectionPasswordLoadFailed {
		if s.connectionSavePassword {
			return fmt.Errorf("无法读取系统保存的密码，请先解锁密钥库")
		}
		if len(saved.Variants) != len(previous.Variants) {
			return fmt.Errorf("密钥库不可用时不能添加或删除备用配置，请先解锁密钥库")
		}
		for i, v := range saved.Variants {
			if v.Name != previous.Variants[i].Name {
				return fmt.Errorf("密钥库不可用时不能重命名备用配置，请先解锁密钥库")
			}
		}
		return nil
	}
	primary := s.connectionMainConfig
	if previous.ID == 0 {
		primary = s.currentConnectionSnapshot()
	}
	snapshots := []connectionEditorSnapshot{primary}
	if previous.ID != 0 {
		snapshots = append(snapshots, s.connectionOtherConfigs...)
	}
	if s.credentialVault == nil {
		for _, snap := range snapshots {
			if snap.savePassword {
				return fmt.Errorf("系统凭据存储不可用")
			}
		}
		return nil
	}

	type change struct {
		name, password string
		set            bool
	}
	changes := make([]change, 0, len(snapshots)+len(previous.Variants))
	enqueue := func(name string, snap connectionEditorSnapshot) {
		if snap.savePassword && snap.secret != "" {
			changes = append(changes, change{name: name, password: snap.secret, set: true})
		} else if snap.hadSavedPassword {
			changes = append(changes, change{name: name})
		}
	}
	enqueue("", primary)
	for _, snap := range snapshots[1:] {
		enqueue(snap.name, snap)
	}
	remaining := make(map[string]bool, len(saved.Variants))
	for _, variant := range saved.Variants {
		remaining[variant.Name] = true
	}
	for _, variant := range previous.Variants {
		if !remaining[variant.Name] {
			changes = append(changes, change{name: variant.Name})
		}
	}
	// Capture the previous state before the first write. All-or-nothing is
	// best-effort because native password vaults have no transaction API.
	type originalSecret struct {
		password string
		found    bool
	}
	before := make(map[string]originalSecret, len(changes))
	for _, change := range changes {
		if _, ok := before[change.name]; ok {
			continue
		}
		password, found, err := s.credentialVault.Get(saved.ID, change.name)
		if err != nil {
			return fmt.Errorf("读取旧凭据失败：%w", err)
		}
		before[change.name] = originalSecret{password: password, found: found}
	}
	for index, change := range changes {
		var err error
		if change.set {
			err = s.credentialVault.Set(saved.ID, change.name, change.password)
		} else {
			err = s.credentialVault.Delete(saved.ID, change.name)
		}
		if err != nil {
			for j := index - 1; j >= 0; j-- {
				applied := changes[j]
				savedOriginal := before[applied.name]
				if savedOriginal.found {
					if restoreErr := s.credentialVault.Set(saved.ID, applied.name, savedOriginal.password); restoreErr != nil {
						err = errors.Join(err, fmt.Errorf("回滚系统凭据失败：%w", restoreErr))
					}
				} else if restoreErr := s.credentialVault.Delete(saved.ID, applied.name); restoreErr != nil {
					err = errors.Join(err, fmt.Errorf("回滚系统凭据失败：%w", restoreErr))
				}
			}
			return fmt.Errorf("密码保存失败：%w", err)
		}
	}
	return nil
}

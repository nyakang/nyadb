package sessionbrowser

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

type sessionResult struct {
	generation                 uint64
	id                         int64
	names                      []string
	err, metadataErr, vaultErr error
}

// Connect consumes the current password draft, then owns the session job. The
// host must resolve any unsaved-document guard before calling lifecycle methods.
func (s *State) Connect(profile connections.Profile, invalidate func()) {
	if profile.Kind != connections.MySQL {
		s.report("请选择 MySQL 连接")
		return
	}
	s.EnsureManager()
	s.Activate(profile.ID)
	w := s.current
	settings, err := connections.ResolveActiveSettings(profile)
	password := w.Password
	var vaultErr error
	if err == nil && password == "" && s.deps.ReadSecret != nil {
		var exists bool
		password, exists, vaultErr = s.deps.ReadSecret(profile.ID, profile.ActiveVariant)
		if vaultErr != nil || !exists {
			password = ""
		}
	}
	if err == nil {
		_, err = mysqlsvc.DirectMySQLConfig(settings, password)
	}
	if err != nil {
		w.Status, w.Error = "连接失败", err.Error()
		s.report(w.Error)
		return
	}
	if w.ProfileID == profile.ID {
		s.Disconnect(profile.ID)
	}
	w = s.current
	ctx, cancel := context.WithCancel(context.Background())
	w.sessionCancel = cancel
	s.bumpGeneration(w)
	generation := w.Generation
	w.ProfileID, w.Status, w.Error = profile.ID, "连接中", ""
	w.Databases = nil
	s.ResetBrowser()
	s.report("正在连接 MySQL：" + profile.Name)
	w.Password = ""
	manager, opener, lister := s.Manager, s.Opener, s.DatabaseLister
	if opener == nil {
		opener = manager.Connect
	}
	if lister == nil {
		lister = manager.Databases
	}
	results := make(chan sessionResult, 1)
	w.sessionResults = results
	go func() {
		err := opener(ctx, settings, password)
		var names []string
		var metaErr error
		if err == nil && ctx.Err() == nil {
			names, metaErr = lister(ctx, profile.ID)
		}
		hide := func(err error) error {
			if err != nil && password != "" {
				return errors.New(strings.ReplaceAll(err.Error(), password, "[已隐藏]"))
			}
			return err
		}
		results <- sessionResult{generation: generation, id: profile.ID, names: names, err: hide(err), metadataErr: hide(metaErr), vaultErr: vaultErr}
		if invalidate != nil {
			invalidate()
		}
	}()
}

// Consume processes parked handshakes, the active handshake/refresh, then active
// object metadata. Parked object reads remain buffered until their profile returns.
func (s *State) Consume() {
	for id, w := range s.parked {
		s.consumeSession(id, w, false)
	}
	s.consumeSession(s.current.ProfileID, s.current, true)
	s.consumeObjects()
}
func (s *State) consumeSession(id int64, w *Workspace, active bool) {
	if w.sessionResults == nil {
		return
	}
	select {
	case result := <-w.sessionResults:
		if result.generation != w.Generation || result.id != id {
			return
		}
		w.sessionResults = nil
		if w.refreshCancel != nil {
			w.refreshCancel()
			w.refreshCancel = nil
		}
		if result.err != nil {
			w.Status, w.Error = "连接失败", result.err.Error()
			if active {
				if result.vaultErr != nil {
					w.Error = "无法读取已保存密码（可手动输入）：" + result.vaultErr.Error() + "；空密码连接失败：" + w.Error
				}
				s.report("连接 MySQL 失败：" + w.Error)
			}
			return
		}
		w.Status, w.Error = "已连接", ""
		if active && s.deps.Connected != nil {
			s.deps.Connected(id)
		}
		if result.metadataErr != nil {
			w.Error = "数据库列表读取失败：" + result.metadataErr.Error()
		} else {
			w.Databases = result.names
			if active {
				s.ReconcileDatabases(result.names)
			} else if s.deps.Connected != nil {
				s.deps.Connected(id)
			}
		}
		if active {
			s.report(fmt.Sprintf("已连接 MySQL（%d 个数据库）", len(result.names)))
		}
	default:
	}
}
func (s *State) RefreshDatabases(invalidate func()) {
	w := s.current
	if w.ProfileID == 0 || w.Status != "已连接" || s.Manager == nil || w.Refreshing() {
		return
	}
	s.CancelObjects()
	id, generation := w.ProfileID, w.Generation
	lister := s.DatabaseLister
	if lister == nil {
		lister = s.Manager.Databases
	}
	ctx, cancel := context.WithCancel(context.Background())
	// List refresh cancellation must also follow this workspace's lifetime.
	if w.refreshCancel != nil {
		w.refreshCancel()
	}
	w.refreshCancel = cancel
	results := make(chan sessionResult, 1)
	w.sessionResults = results
	go func() {
		names, err := lister(ctx, id)
		results <- sessionResult{generation: generation, id: id, names: names, metadataErr: err}
		if invalidate != nil {
			invalidate()
		}
	}()
}
func (s *State) Disconnect(id int64) {
	if id <= 0 {
		return
	}
	w := s.Workspace(id)
	if w != nil {
		s.bumpGeneration(w)
		if w.sessionCancel != nil {
			w.sessionCancel()
		}
		if w.refreshCancel != nil {
			w.refreshCancel()
			w.refreshCancel = nil
		}
		w.sessionCancel, w.sessionResults = nil, nil
		if w.objectCancel != nil {
			w.objectCancel()
		}
		w.objectCancel, w.objectResults = nil, nil
		w.Password = ""
		if w == s.current {
			w.ProfileID, w.Databases = 0, nil
			s.ResetBrowser()
			w.Error, w.Status = "", "未连接"
		} else {
			delete(s.parked, id)
		}
	}
	if s.deps.Disconnected != nil {
		s.deps.Disconnected(id)
	}
	if s.Manager != nil {
		_ = s.Manager.Disconnect(id)
	}
}
func (s *State) DisconnectAll() {
	for id := range s.parked {
		s.Disconnect(id)
	}
	if s.current.ProfileID != 0 {
		s.Disconnect(s.current.ProfileID)
	}
}
func (s *State) Close() {
	s.DisconnectAll()
	s.current.Password = ""
	if s.Manager != nil {
		_ = s.Manager.Close()
	}
	s.Manager = nil
}

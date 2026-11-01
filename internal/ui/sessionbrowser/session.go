package sessionbrowser

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"

	"github.com/nyakang/nyadb/internal/services/connections"
)

type sessionResult struct {
	published                  uint64
	namespaces                 []contract.Namespace
	generation                 uint64
	id                         int64
	names                      []string
	err, metadataErr, vaultErr error
}

// Connect consumes the current password draft, then owns the session job. The
// host must resolve any unsaved-document guard before calling lifecycle methods.
func (s *State) Connect(profile connections.Profile, invalidate func()) {
	s.connectEngine(profile, invalidate)
}

// Consume processes parked handshakes, the active handshake/refresh, then active
// object metadata. Parked object reads remain buffered until their profile returns.
func (s *State) Consume() {
	if s.engines.IsConnected != nil {
		var lost []int64
		for id, w := range s.parked {
			if w.Status == "已连接" && !s.engines.IsConnected(id) {
				lost = append(lost, id)
			}
		}
		if w := s.current; w.Status == "已连接" && !s.engines.IsConnected(w.ProfileID) {
			lost = append(lost, w.ProfileID)
		}
		for _, id := range lost {
			s.Disconnect(id)
			s.report("数据库会话已失效；请显式重新连接，不会重放 SQL 或事务")
		}
	}

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
				s.report("连接数据库失败：" + w.Error)
			}
			return
		}
		if result.published != 0 {
			w.Generation = result.published
			s.generation = max(s.generation, w.Generation)
		}
		if result.namespaces != nil && result.metadataErr == nil {
			known := make(map[contract.NamespaceRef]bool, len(result.namespaces))
			for _, n := range result.namespaces {
				known[n.Ref] = true
			}
			for n := range w.QualifiedCache {
				if !known[n] {
					delete(w.QualifiedCache, n)
				}
			}
			w.Namespaces = make(map[string]contract.NamespaceRef, len(result.namespaces))
			for _, n := range result.namespaces {
				w.Namespaces[n.Label] = n.Ref
			}
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
			s.report(s.engineStatus(w, len(result.names)))
		}
	default:
	}
}
func (s *State) RefreshDatabases(invalidate func()) {
	w := s.current
	if w.ProfileID == 0 || w.Status != "已连接" || s.engines.Session == nil || w.Refreshing() {
		return
	}
	s.CancelObjects()
	id, generation := w.ProfileID, w.Generation
	lister := s.DatabaseLister
	var metadata contract.MetadataProvider
	if lister == nil {
		session, err := s.engines.Session(w.ProfileID)
		if err != nil {
			w.Error = err.Error()
			return
		}
		metadata, err = contract.Require[contract.MetadataProvider](session)
		if err != nil {
			w.Error = err.Error()
			return
		}
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
		var names []string
		var namespaces []contract.Namespace
		var err error
		if metadata != nil {
			namespaces, err = metadata.Namespaces(ctx)
			for _, n := range namespaces {
				names = append(names, n.Label)
			}
		} else {
			names, err = lister(ctx, id)
		}
		results <- sessionResult{generation: generation, id: id, names: names, namespaces: namespaces, metadataErr: err}
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
	if s.engines.Disconnect != nil {
		_ = s.engines.Disconnect(id)
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
	if s.engines.Close != nil {
		_ = s.engines.Close()
	}
}

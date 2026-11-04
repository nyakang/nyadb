package dbruntime

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type backupOperation struct {
	owner  int64
	file   string
	cancel context.CancelFunc
	done   chan struct{}
}

func samePhysicalFile(a, b string) bool {
	aa, ea := os.Stat(a)
	bb, eb := os.Stat(b)
	if ea == nil && eb == nil && os.SameFile(aa, bb) {
		return true
	}
	a, _ = filepath.Abs(a)
	b, _ = filepath.Abs(b)
	return a == b
}
func profileUsesFile(p connections.Profile, file string) bool {
	if file == "" || p.Kind != connections.SQLite {
		return false
	}
	if samePhysicalFile(p.Database, file) {
		return true
	}
	for _, a := range p.SQLiteOptions.Attachments {
		if samePhysicalFile(a.File, file) {
			return true
		}
	}
	return false
}
func (m *Manager) backupLocked(p connections.Profile) bool {
	for op := range m.backupOperations {
		if profileUsesFile(p, op.file) {
			return true
		}
	}
	return false
}
func (m *Manager) backupAffected(file string) []contract.BackupSession {
	var out []contract.BackupSession
	for id, s := range m.sessions {
		if profileUsesFile(s.profile, file) {
			out = append(out, contract.BackupSession{ProfileID: id, Generation: s.generation})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProfileID < out[j].ProfileID })
	return out
}
func (m *Manager) Backups(id int64) contract.BackupOperations {
	current, err := m.Session(id)
	if err != nil {
		return contract.BackupOperations{}
	}
	s := current.(*session)
	provider, err := contract.Require[contract.BackupProvider](s.raw)
	if err != nil {
		return contract.BackupOperations{}
	}
	raw := provider.Backups()
	if raw.Availability == nil || raw.Plan == nil || raw.Execute == nil {
		return contract.BackupOperations{}
	}
	check := func(t contract.SchemaDesignTarget, action string) error {
		if e := s.schemaDesignTarget(t); e != nil {
			return e
		}
		caps := s.Capabilities()
		if action == "backup" && !caps.Backup || action == "restore" && !caps.Restore {
			return contract.ErrUnsupported
		}
		return nil
	}
	return contract.BackupOperations{Engine: raw.Engine,
		Availability: func(t contract.SchemaDesignTarget, action string) contract.Availability {
			if check(t, action) != nil {
				return contract.Availability{Reason: "unsupported"}
			}
			t.Generation = s.raw.Generation()
			return raw.Availability(t, action)
		},
		Plan: func(ctx context.Context, r contract.BackupRequest) (contract.BackupPlan, error) {
			if e := check(r.Target, r.Action); e != nil {
				return contract.BackupPlan{}, e
			}
			r = r.Clone()
			r.Target.Generation = s.raw.Generation()
			p, e := raw.Plan(ctx, r)
			if e != nil {
				return p, e
			}
			if p.Request.Target != r.Target || !s.alive() {
				return contract.BackupPlan{}, contract.ErrRevoked
			}
			p = p.Clone()
			p.Request.Target.Generation = s.generation
			m.mu.Lock()
			p.Affected = m.backupAffected(p.QuiesceFile)
			m.mu.Unlock()
			return p, nil
		},
		Execute: func(ctx context.Context, p contract.BackupPlan, progress func(contract.BackupProgress)) contract.BackupOutcome {
			if e := check(p.Request.Target, p.Request.Action); e != nil {
				return contract.BackupOutcome{State: "not_executed", Error: e}
			}
			p = p.Clone()
			affected := slices.Clone(p.Affected)
			p.Affected = nil
			p.Request.Target.Generation = s.raw.Generation()
			provider, e := contract.Require[contract.BackupTaskProvider](s.raw)
			if e != nil {
				return contract.BackupOutcome{State: "not_executed", Error: e}
			}
			opctx, cancel := context.WithCancel(ctx)
			defer cancel()
			task, e := provider.PrepareBackup(opctx, p)
			if e != nil {
				return contract.BackupOutcome{State: "not_executed", Error: e}
			}
			defer task.Close()
			op := &backupOperation{owner: id, file: p.QuiesceFile, cancel: cancel, done: make(chan struct{})}
			m.mu.Lock()
			if m.closed || m.sessions[id] != s || !slices.Equal(affected, m.backupAffected(p.QuiesceFile)) || m.backupLocked(s.profile) {
				m.mu.Unlock()
				return contract.BackupOutcome{State: "not_executed", Error: contract.ErrRevoked}
			}
			for prior := range m.backupOperations {
				if op.file != "" && prior.file != "" && samePhysicalFile(op.file, prior.file) {
					m.mu.Unlock()
					return contract.BackupOutcome{State: "not_executed", Error: contract.ErrRevoked}
				}
			}
			m.backupOperations[op] = true
			var drained []*session
			for _, a := range affected {
				drained = append(drained, m.sessions[a.ProfileID])
				delete(m.sessions, a.ProfileID)
			}
			for id, a := range m.pending {
				if profileUsesFile(a.profile, op.file) {
					a.cancel()
					delete(m.pending, id)
				}
			}
			m.mu.Unlock()
			defer func() { m.mu.Lock(); delete(m.backupOperations, op); close(op.done); m.mu.Unlock() }()
			for _, d := range drained {
				if e = d.Close(); e != nil {
					return contract.BackupOutcome{State: "not_executed", Error: e, Drained: affected}
				}
			}
			out := task.Execute(progress)
			out.Drained = affected
			return out
		},
	}
}

package engines

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) Backups() contract.BackupOperations {
	return contract.BackupOperations{Engine: string(s.kind), Availability: s.backupAvailability, Plan: s.backupPlan, Execute: func(ctx context.Context, p contract.BackupPlan, progress func(contract.BackupProgress)) contract.BackupOutcome {
		task, e := s.PrepareBackup(ctx, p)
		if e != nil {
			return contract.BackupOutcome{State: "not_executed", Error: e}
		}
		defer task.Close()
		return task.Execute(progress)
	}}
}
func (s *Session) backupAvailability(t contract.SchemaDesignTarget, action string) contract.Availability {
	if s.isClosed() || t.Generation != s.generation {
		return contract.Availability{Reason: "disconnected"}
	}
	o := t.Object
	o.Kind = "table"
	if o.Name == "" {
		o.Name = "_"
	}
	if s.check(o) != nil {
		return contract.Availability{Reason: "invalid_target"}
	}
	if s.kind == connections.CockroachDB || s.kind.IsGaussFamily() || s.kind == connections.Kingbase {
		return contract.Availability{Reason: "unsupported"}
	}
	if action != "backup" && action != "restore" {
		return contract.Availability{Reason: "unsupported"}
	}
	if action == "restore" && s.namespaceReadOnly(o.Schema) {
		return contract.Availability{Reason: "read_only"}
	}
	return contract.Availability{Enabled: true}
}
func (s *Session) backupSeal(p contract.BackupPlan) string {
	p.Seal = ""
	data, _ := json.Marshal(p)
	h := hmac.New(sha256.New, []byte(s.dataScope))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}
func (s *Session) backupPlan(ctx context.Context, r contract.BackupRequest) (contract.BackupPlan, error) {
	r = r.Clone()
	p := contract.BackupPlan{Request: r, Engine: string(s.kind)}
	if !s.backupAvailability(r.Target, r.Action).Enabled {
		return p, contract.ErrUnsupported
	}
	ctx, cancel := s.requestContext(ctx)
	defer cancel()
	if r.Path == "" || r.Action == "restore" && r.Destination == "" {
		return p, connections.ErrInvalid
	}
	var err error
	if s.kind == connections.SQLite {
		p, err = s.sqliteBackupPlan(ctx, r)
	} else {
		p, err = s.postgresBackupPlan(ctx, r)
	}
	if err != nil {
		return p, err
	}
	p.Seal = s.backupSeal(p)
	return p, nil
}

type backupTask struct {
	ctx             context.Context
	cancel          context.CancelFunc
	plan            contract.BackupPlan
	profile         connections.Profile
	password        string
	mu              sync.Mutex
	started, closed bool
	done            chan struct{}
}

func (s *Session) PrepareBackup(ctx context.Context, p contract.BackupPlan) (contract.BackupTask, error) {
	p = p.Clone()
	if !s.backupAvailability(p.Request.Target, p.Request.Action).Enabled || !hmac.Equal([]byte(p.Seal), []byte(s.backupSeal(p))) {
		return nil, contract.ErrRevoked
	}
	ctx, cancel := context.WithCancel(ctx)
	return &backupTask{ctx: ctx, cancel: cancel, plan: p, profile: s.backupProfile, password: s.backupPassword, done: make(chan struct{})}, nil
}
func (t *backupTask) Close() {
	t.mu.Lock()
	t.cancel()
	t.closed = true
	started := t.started
	t.mu.Unlock()
	if started {
		<-t.done
	}
	t.mu.Lock()
	t.password = ""
	t.mu.Unlock()
}
func (t *backupTask) Execute(progress func(contract.BackupProgress)) contract.BackupOutcome {
	t.mu.Lock()
	if t.closed || t.started {
		t.mu.Unlock()
		return contract.BackupOutcome{State: "not_executed", Error: contract.ErrRevoked}
	}
	t.started = true
	t.mu.Unlock()
	defer close(t.done)
	if e := t.ctx.Err(); e != nil {
		return contract.BackupOutcome{State: "not_executed", Error: e}
	}
	if t.plan.Request.Action == "restore" {
		// Execute from an owned immutable copy, not a pathname that can change
		// after hashing and before pg_restore or sqlite_backup opens it.
		// SQLite may create WAL/SHM siblings even while reading a copied
		// WAL-mode header. Own the directory so every staging resource is
		// removed after all driver/tool handles have closed.
		dir, e := os.MkdirTemp("", "nyadb-restore-source-*")
		if e != nil {
			return contract.BackupOutcome{State: "not_executed", Error: e}
		}
		defer os.RemoveAll(dir)
		staged, e := os.CreateTemp(dir, "archive-*")
		if e != nil {
			return contract.BackupOutcome{State: "not_executed", Error: e}
		}
		path := staged.Name()
		source, e := os.Open(t.plan.Request.Path)
		if e != nil {
			staged.Close()
			return contract.BackupOutcome{State: "not_executed", Error: e}
		}
		h := sha256.New()
		buffer := make([]byte, 64<<10)
		for {
			if e = t.ctx.Err(); e != nil {
				break
			}
			var n int
			n, e = source.Read(buffer)
			if n > 0 {
				if _, writeErr := staged.Write(buffer[:n]); writeErr != nil {
					e = writeErr
					break
				}
				h.Write(buffer[:n])
			}
			if e == io.EOF {
				e = nil
				break
			}
			if e != nil {
				break
			}
		}
		source.Close()
		e = errors.Join(e, staged.Close())
		digest := hex.EncodeToString(h.Sum(nil))
		if e != nil || digest != t.plan.Digest {
			return contract.BackupOutcome{State: "not_executed", Error: errors.Join(e, errors.New("backup file changed since preview"))}
		}
		t.plan.Request.Path = path
	}
	if progress == nil {
		progress = func(contract.BackupProgress) {}
	}
	if t.plan.Engine == "sqlite" {
		return t.executeSQLiteBackup(progress)
	}
	return t.executePostgresBackup(progress)
}
func backupDigest(ctx context.Context, path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	b := make([]byte, 64<<10)
	for {
		if e = ctx.Err(); e != nil {
			return "", e
		}
		n, e := f.Read(b)
		h.Write(b[:n])
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", e
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func sameBackupFile(a, b string) bool {
	aa, ea := os.Stat(a)
	bb, eb := os.Stat(b)
	if ea == nil && eb == nil && os.SameFile(aa, bb) {
		return true
	}
	a, _ = filepath.Abs(a)
	b, _ = filepath.Abs(b)
	return a == b
}

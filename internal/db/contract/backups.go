package contract

import (
	"context"
	"slices"
)

type BackupRequest struct {
	Target                                SchemaDesignTarget
	Action                                string // backup | restore
	Scope                                 string // database | schema | tables; SQLite always one file
	Tables                                []ObjectRef
	Path, Destination                     string
	Overwrite, PreserveOwner, PreserveACL bool
}

func (r BackupRequest) Clone() BackupRequest { r.Tables = slices.Clone(r.Tables); return r }

type BackupPlan struct {
	Affected                                                 []BackupSession
	Request                                                  BackupRequest
	Engine, Digest, Source, ToolVersion, ServerVersion, Seal string
	Objects, Warnings                                        []string
	// QuiesceFile is the single physical SQLite file authorized for overwrite.
	QuiesceFile     string
	TargetCatalogID int64 // PostgreSQL catalog identity; zero for a new target
}

func (p BackupPlan) Clone() BackupPlan {
	p.Affected = slices.Clone(p.Affected)
	p.Request = p.Request.Clone()
	p.Objects = slices.Clone(p.Objects)
	p.Warnings = slices.Clone(p.Warnings)
	return p
}

type BackupProgress struct {
	Phase            string
	Completed, Total int64
}
type BackupOutcome struct {
	State   string // not_executed | completed | failed | rolled_back | uncertain
	Path    string
	Error   error
	Drained []BackupSession
}
type BackupSession struct {
	ProfileID  int64
	Generation uint64
}
type BackupOperations struct {
	Engine       string
	Availability func(SchemaDesignTarget, string) Availability
	Plan         func(context.Context, BackupRequest) (BackupPlan, error)
	Execute      func(context.Context, BackupPlan, func(BackupProgress)) BackupOutcome
}
type BackupProvider interface{ Backups() BackupOperations }

// BackupTask owns an authorized operation independently of deliberately drained
// sessions. Only the runtime acquires it; closing it acknowledges cancellation.
type BackupTask interface {
	Execute(func(BackupProgress)) BackupOutcome
	Close()
}
type BackupTaskProvider interface {
	PrepareBackup(context.Context, BackupPlan) (BackupTask, error)
}

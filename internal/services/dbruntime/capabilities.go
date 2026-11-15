package dbruntime

import (
	"github.com/nyakang/nyadb/internal/db/contract"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func (s *session) managementCapabilities(c contract.Capabilities) contract.Capabilities {
	admin := false
	if p, err := contract.Require[interface {
		Administration() mysqlsvc.Administration
	}](s.raw); err == nil {
		admin = p.Administration() != nil
	}
	var design contract.SchemaDesignOperations
	if p, err := contract.Require[contract.SchemaDesignProvider](s.raw); err == nil {
		design = p.SchemaDesign()
	}
	var view contract.ViewDesignOperations
	if p, err := contract.Require[contract.ViewDesignProvider](s.raw); err == nil {
		view = p.ViewDesign()
	}
	documents := s.rawSchemaDocumentFeatures()
	c.DesignTables = c.DesignTables && (admin || design.Snapshot != nil && (design.Apply != nil && design.CanApply != nil || documents.CanApply != nil))
	c.DesignViews = c.DesignViews && (admin || view.Snapshot != nil && view.Apply != nil && view.CanApply != nil)
	c.Objects = c.Objects && (admin || design.Drop != nil || design.Copy != nil || design.Rename != nil || design.Clear != nil || view.Apply != nil || design.Snapshot != nil && (documents.CanDrop != nil || documents.CanCopy != nil || documents.CanRename != nil || documents.CanClear != nil))
	c.Accounts = c.Accounts && admin
	backup := false
	if p, e := contract.Require[contract.BackupProvider](s.raw); e == nil {
		v := p.Backups()
		backup = v.Availability != nil && v.Plan != nil && v.Execute != nil
	}
	c.Backup = c.Backup && (admin || backup)
	c.Restore = c.Restore && (admin || backup)
	maintenance, err := contract.Require[contract.MaintenanceProvider](s.raw)
	c.Maintenance = c.Maintenance && err == nil
	if c.Maintenance {
		v := maintenance.Maintenance()
		c.Maintenance = v.Actions != nil && v.Plan != nil && v.Execute != nil
	}
	return c
}

// Availability never performs I/O. Providers recheck permissions, server
// versions and the full original object on their execution workers.
func (m *Manager) Availability(id int64, operation string, target contract.SchemaDesignTarget) contract.Availability {
	s, err := m.Session(id)
	if err != nil {
		return contract.Availability{Reason: "disconnected"}
	}
	if target.Object.ProfileID != id || target.Generation != s.Generation() {
		return contract.Availability{Reason: "invalid_target"}
	}
	caps := s.Capabilities()
	documents := m.SchemaDesignDocumentFeatures(id)
	enabled := false
	switch operation {
	case "design_table":
		ops := m.SchemaDesign(id)
		enabled = caps.DesignTables && (ops.CanApply != nil && ops.CanApply(target) || documents.CanApply != nil && documents.CanApply(target))
	case "drop_table":
		ops := m.SchemaDesign(id)
		enabled = caps.Objects && (ops.CanDrop != nil && ops.CanDrop(target) || documents.CanDrop != nil && documents.CanDrop(target))
	case "copy_table":
		ops := m.SchemaDesign(id)
		enabled = caps.Objects && (ops.CanCopy != nil && ops.CanCopy(target) || documents.CanCopy != nil && documents.CanCopy(target))
	case "rename_table":
		ops := m.SchemaDesign(id)
		enabled = caps.Objects && (ops.CanRename != nil && ops.CanRename(target) || documents.CanRename != nil && documents.CanRename(target))
	case "clear_table":
		ops := m.SchemaDesign(id)
		enabled = caps.Objects && (ops.CanClear != nil && ops.CanClear(target) || documents.CanClear != nil && documents.CanClear(target))
	case "design_view":
		ops := m.ViewDesign(id)
		enabled = caps.DesignViews && ops.CanApply != nil && ops.CanApply(target)
	case "maintenance":
		ops := m.Maintenance(id)
		enabled = caps.Maintenance && ops.Actions != nil && len(ops.Actions(target)) != 0
	case "import":
		enabled = caps.Import
	case "backup":
		enabled = caps.Backup
	case "restore":
		enabled = caps.Restore
	case "accounts":
		enabled = caps.Accounts
	}
	if !enabled {
		return contract.Availability{Reason: "unsupported"}
	}
	return contract.Availability{Enabled: true}
}

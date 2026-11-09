package shell

import (
	"fmt"
	"github.com/nyakang/nyadb/locales"
	"strings"
)

func (s *State) workspaceTabCaption(t *tab) string {
	if t == nil {
		return ""
	}
	if t.query != nil {
		name := t.query.Name()
		if t.query.Dirty() {
			name += " *"
		}
		return name
	}
	key := t.identity()
	if key.name == "collection_management" && key.objectKind == "集合与别名管理" || key.name == "snapshot_management" && key.objectKind == "快照管理" {
		caption := locales.Text(key.objectKind)
		for _, other := range s.tabs {
			if other != t && other.identity().name == key.name && other.profileID() != key.profileID {
				caption = s.profileName(key.profileID) + " / " + caption
				break
			}
		}
		if t.object != nil && t.object.DesignDirty() {
			caption += " *"
		}
		return caption
	}
	suffix := ""
	if t.kind == tabStructure {
		suffix = " · " + locales.Text("设计")
		if t.object != nil && t.object.ObjectKind() == "索引设置" {
			suffix = " · " + locales.Text("设置")
		}
		if t.object != nil && t.object.ObjectKind() == "任务" {
			suffix = " · " + locales.Text("任务")
		}
		if t.object != nil && t.object.DesignDirty() {
			suffix += " *"
		}
	}
	if key.name == "" {
		return locales.Text(t.title)
	}
	prefix := ""
	for _, other := range s.tabs {
		if other == t {
			continue
		}
		o := other.identity()
		if o.name != key.name {
			continue
		}
		if o.profileID != key.profileID {
			prefix = s.profileName(key.profileID) + " / "
			break
		}
	}
	if prefix != "" {
		parts := []string{}
		if key.catalog != "" {
			parts = append(parts, key.catalog)
		}
		if key.schema != "" && key.schema != key.catalog {
			parts = append(parts, key.schema)
		}
		parts = append(parts, key.name)
		return prefix + strings.Join(parts, ".") + suffix
	}
	for _, other := range s.tabs {
		o := other.identity()
		if o.name == key.name && (o.catalog != key.catalog || o.schema != key.schema || o.database != key.database) {
			parts := []string{}
			if key.catalog != "" {
				parts = append(parts, key.catalog)
			}
			if key.schema != "" && key.schema != key.catalog {
				parts = append(parts, key.schema)
			}
			parts = append(parts, key.name)
			return strings.Join(parts, ".") + suffix
		}
	}
	return key.name + suffix
}
func (s *State) workspaceTabTooltip(t *tab) string {
	key := t.identity()
	if t.query != nil {
		n := t.query.Namespace()
		key.profileID, key.catalog, key.schema = n.ProfileID, n.Catalog, n.Schema
		key.database = t.query.Database()
		if t.query.NeedsRecovery() {
			key.profileID = t.query.RecoveryProfile()
			key.database = t.query.RecoverySchema()
		}
		key.objectKind, key.name = "SQL", t.query.Name()
	}
	if key.profileID == 0 {
		return t.title
	}
	return fmt.Sprintf("%s (#%d)\nCatalog: %s\nSchema: %s\n%s: %s", s.profileName(key.profileID), key.profileID, key.catalog, key.schema, key.objectKind, key.name)
}

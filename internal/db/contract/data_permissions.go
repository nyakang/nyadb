package contract

import "slices"

func DataColumnEditAllowed(p *DataPage, name, kind string) bool {
	return p != nil && (p.EditKinds == nil || slices.Contains(p.EditKinds, kind)) && (p.ColumnEditKinds == nil || slices.Contains(p.ColumnEditKinds[name], kind))
}

func CloneDataColumnEditKinds(kinds map[string][]string) map[string][]string {
	if kinds == nil {
		return nil
	}
	out := make(map[string][]string, len(kinds))
	for name, allowed := range kinds {
		out[name] = slices.Clone(allowed)
	}
	return out
}

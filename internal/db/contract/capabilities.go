package contract

import "slices"

// HasDataWriteExecutor accepts a direct saver or a mandatory confirmation path
// covering every advertised edit kind. Partial previews cannot advertise an
// editor whose remaining actions have no executor.
func HasDataWriteExecutor(r DataRuntime) bool {
	if r.Saver != nil {
		return true
	}
	if r.SubmitEdits == nil {
		return false
	}
	kinds := r.EditKinds
	if kinds == nil {
		kinds = []string{"insert", "update", "delete"}
	}
	if len(kinds) == 0 {
		return false
	}
	for _, kind := range kinds {
		switch kind {
		case "copy":
			if r.PreviewKeyCopy == nil {
				return false
			}
		case "insert", "update", "delete", "detach_delete":
			if r.PreviewEdits == nil || !slices.Contains(r.PreviewEditKinds, kind) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// BoundDataCapabilities removes advertised features whose document binding has
// no executor. It never reserves another lease or opens a server resource.
func BoundDataCapabilities(r DataRuntime) Capabilities {
	c := r.Capabilities
	c.EditableTables = c.EditableTables && HasDataWriteExecutor(r) && (!r.AsyncWrites || r.Verify != nil)
	c.Transactions = c.Transactions && r.Transactions.Begin != nil && r.Transactions.Commit != nil && r.Transactions.Rollback != nil
	c.Keyset = c.Keyset && r.AdvancedLoader != nil
	c.Count = c.Count && r.Count != nil
	c.FieldChunks = c.FieldChunks && r.Chunk != nil
	c.Analysis = c.Analysis && r.Analyzer != nil
	c.Import = c.Import && (r.Importer != nil || r.StreamImporter != nil || r.PreviewGraphImport != nil && r.SubmitGraphImport != nil) && (!r.DocumentImport || r.Importer != nil && r.AsyncWrites && r.Verify != nil)
	c.JSONL = c.JSONL && r.JSONL != nil
	c.Export = c.Export && (r.Exporter != nil || r.JSONL != nil || r.ExportGraph != nil)
	c.GraphExchange = c.GraphExchange && c.Import && c.Export && r.PreviewGraphImport != nil && r.SubmitGraphImport != nil && r.ExportGraph != nil
	return c
}

func IntersectCapabilities(a, b Capabilities) Capabilities {
	return Capabilities{
		GraphExchange: a.GraphExchange && b.GraphExchange,
		KeyValues:     a.KeyValues && b.KeyValues,
		Documents:     a.Documents && b.Documents,
		Objects:       a.Objects && b.Objects, Maintenance: a.Maintenance && b.Maintenance, Accounts: a.Accounts && b.Accounts, Restore: a.Restore && b.Restore, DesignViews: a.DesignViews && b.DesignViews,
		Schemas: a.Schemas && b.Schemas, Transactions: a.Transactions && b.Transactions, EditableTables: a.EditableTables && b.EditableTables, Explain: a.Explain && b.Explain, DesignTables: a.DesignTables && b.DesignTables, Import: a.Import && b.Import, Backup: a.Backup && b.Backup,
		Query: a.Query && b.Query, Metadata: a.Metadata && b.Metadata, Keyset: a.Keyset && b.Keyset, Count: a.Count && b.Count, FieldChunks: a.FieldChunks && b.FieldChunks, Analysis: a.Analysis && b.Analysis, Export: a.Export && b.Export, JSONL: a.JSONL && b.JSONL,
	}
}

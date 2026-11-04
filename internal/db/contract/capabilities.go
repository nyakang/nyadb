package contract

func IntersectCapabilities(a, b Capabilities) Capabilities {
	return Capabilities{
		Objects: a.Objects && b.Objects, Maintenance: a.Maintenance && b.Maintenance, Accounts: a.Accounts && b.Accounts, Restore: a.Restore && b.Restore, DesignViews: a.DesignViews && b.DesignViews,
		Schemas: a.Schemas && b.Schemas, Transactions: a.Transactions && b.Transactions, EditableTables: a.EditableTables && b.EditableTables, Explain: a.Explain && b.Explain, DesignTables: a.DesignTables && b.DesignTables, Import: a.Import && b.Import, Backup: a.Backup && b.Backup,
		Query: a.Query && b.Query, Metadata: a.Metadata && b.Metadata, Keyset: a.Keyset && b.Keyset, Count: a.Count && b.Count, FieldChunks: a.FieldChunks && b.FieldChunks, Analysis: a.Analysis && b.Analysis, Export: a.Export && b.Export, JSONL: a.JSONL && b.JSONL,
	}
}

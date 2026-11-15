package contract

import "slices"

const DataImportReceiptMaxRows = 1024

func CloneDataImportRows(rows []DataImportRowResult) []DataImportRowResult {
	rows = slices.Clone(rows)
	for i, row := range rows {
		if row.Outcome == nil {
			continue
		}
		o := *row.Outcome
		snapshots := CloneDataMutationSnapshot(DataMutationSnapshot{Edits: []DataEdit{{Original: o.Original}, {Original: o.Saved}}}).Edits
		o.Original, o.Saved = snapshots[0].Original, snapshots[1].Original
		o.PreviewColumns = slices.Clone(o.PreviewColumns)
		o.Previews = slices.Clone(o.Previews)
		for j, preview := range o.Previews {
			if preview.Value != nil {
				v := preview.Value.Clone()
				o.Previews[j].Value = &v
			}
		}
		rows[i].Outcome = &o
	}
	return rows
}

func CloneDataImportBatchReceipt(r DataImportBatchReceipt) DataImportBatchReceipt {
	r.Rows = CloneDataImportRows(r.Rows)
	return r
}

// Validate complete projections before publication or archival. Older
// providers may omit Rows; a supplied projection must describe its entire
// original range and agree with the native batch acknowledgement.
func ValidateDataImportBatchReceipt(r DataImportBatchReceipt) error {
	if !slices.Contains([]string{"committed", "rolled_back", "not_executed", "uncertain"}, r.State) || r.Sequence < 1 || r.FirstRow < 0 || r.LastRow < r.FirstRow || r.FirstRow == 0 && r.LastRow != 0 || r.CommittedRows < 0 || r.State != "committed" && (r.CommittedRows != 0 || r.Created || r.Changed) {
		return ErrDataPage
	}
	if r.Rows == nil {
		return nil
	}
	if len(r.Rows) > DataImportReceiptMaxRows || r.FirstRow == 0 && len(r.Rows) != 0 || r.FirstRow > 0 && int64(len(r.Rows)) != r.LastRow-r.FirstRow+1 {
		return ErrDataPage
	}
	committed := int64(0)
	for i, row := range r.Rows {
		if int64(row.Row) != r.FirstRow+int64(i) || row.Table != r.Object.Name {
			return ErrDataPage
		}
		switch r.State {
		case "committed":
			if row.Status == "committed" && slices.Contains([]string{"insert", "update", "delete"}, row.Kind) {
				committed++
			} else if row.Status != "skipped" || row.Kind != "skip" || row.Outcome != nil {
				return ErrDataPage
			}
		case "rolled_back":
			if !slices.Contains([]string{"rolled_back", "failed", "not_processed"}, row.Status) || row.Outcome != nil {
				return ErrDataPage
			}
		case "uncertain":
			if row.Status != "uncertain" || row.Outcome != nil {
				return ErrDataPage
			}
		case "not_executed":
			if row.Status != "not_processed" || row.Outcome != nil {
				return ErrDataPage
			}
		}
	}
	if committed != r.CommittedRows {
		return ErrDataPage
	}
	return nil
}

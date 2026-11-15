package rqlite

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func executeImportBatch(s *Session, ctx context.Context, t tableSnapshot, r contract.DataImportRequest, b importBatch, sequence int64) (receipt contract.DataImportBatchReceipt, rows []contract.DataImportRowResult, version int64, err error) {
	receipt = contract.DataImportBatchReceipt{Object: t.structure.Object, Generation: s.Generation(), Sequence: sequence, State: "not_executed"}
	if len(b.rows) > 0 {
		receipt.FirstRow = b.rows[0].number
		receipt.LastRow = b.rows[len(b.rows)-1].number
	}
	native, err := s.request(ctx, "/db/request", b.statements, true, true)
	receipt.NativeJSON = native.Raw
	for _, row := range b.rows {
		rows = append(rows, contract.DataImportRowResult{Database: "main", Table: t.structure.Name, Row: int(row.number), Kind: r.Mode, Status: "not_processed"})
	}
	unknown := func(cause error) (contract.DataImportBatchReceipt, []contract.DataImportRowResult, int64, error) {
		receipt.State = "uncertain"
		receipt.CommittedRows = 0
		receipt.Changed, receipt.Created = false, false
		for i := range rows {
			rows[i].Status = "uncertain"
			rows[i].Outcome = nil
		}
		return receipt, rows, 0, errors.Join(contract.ErrDataCommitUncertain, cause)
	}
	if native.Uncertain || errors.Is(err, errIncomplete) || errors.Is(err, errResponseLost) {
		return unknown(err)
	}
	if err != nil {
		failed := -1
		for i, n := range native.Results {
			if n.failed() {
				failed = i
				break
			}
		}
		if failed >= 0 {
			receipt.State = "rolled_back"
			for i, indices := range b.mutations {
				start := indices[0] - 1
				end := indices[len(indices)-1] + 1
				if failed >= start && failed <= end {
					rows[i].Kind, rows[i].Status, rows[i].Error = "error", "failed", "row validation or database constraint failed"
				} else if start < failed {
					rows[i].Status = "rolled_back"
				}
			}
			if failed == 2 {
				err = errors.Join(contract.ErrDataConflict, err)
			}
		}
		return receipt, rows, 0, err
	}
	if len(native.Results) != len(b.statements) {
		return unknown(errIncomplete)
	}
	for _, index := range b.guards {
		n, e := native.Results[index].Affected.Int64()
		if e != nil || n != 1 {
			return unknown(errIncomplete)
		}
	}
	states, e := decodedRows(native, b.states, 2)
	if e != nil || len(states) != len(b.rows) {
		return unknown(errIncomplete)
	}
	versions, e := decodedRows(native, b.version, 1)
	if e != nil || len(versions) != 1 {
		return unknown(errIncomplete)
	}
	version, e = cellInt(versions[0][0])
	if e != nil || version < 0 || !b.create && version != t.version || b.create && version <= t.version {
		return unknown(errIncomplete)
	}
	if b.clear >= 0 {
		n, e := importNativeCount(native.Results[b.clear].Affected)
		if e != nil || n < 0 {
			return unknown(errIncomplete)
		}
		receipt.Changed = n > 0
	}
	for i, indices := range b.mutations {
		slot, e := cellInt(states[i][0])
		if e != nil || slot != int64(i) {
			return unknown(errIncomplete)
		}
		found, e := cellInt(states[i][1])
		if e != nil || found < 0 || found > 1 {
			return unknown(errIncomplete)
		}
		counts := []int64{}
		for _, index := range indices {
			n, e := importNativeCount(native.Results[index].Affected)
			if e != nil || n < 0 || n > 1 {
				return unknown(errIncomplete)
			}
			counts = append(counts, n)
		}
		kind := "insert"
		expected := int64(1)
		switch r.Mode {
		case "update":
			kind = "update"
			expected = found
			if len(b.rows[i].updates) == 0 {
				expected = 0
			}
		case "delete":
			kind = "delete"
			expected = found
		case "insert_missing":
			expected = 1 - found
		case "upsert":
			update := found
			if len(b.rows[i].updates) == 0 {
				update = 0
			}
			if len(counts) != 2 || counts[0] != update || counts[1] != 1-found {
				return unknown(errIncomplete)
			}
			kind = "insert"
			if found == 1 {
				kind = "update"
			}
			expected = counts[0] + counts[1]
		}
		if r.Mode != "upsert" && (len(counts) != 1 || counts[0] != expected) {
			return unknown(errIncomplete)
		}
		rows[i].Kind, rows[i].Status = kind, "committed"
		if expected == 0 {
			rows[i].Kind, rows[i].Status = "skip", "skipped"
			continue
		}
		outcome := &contract.DataEditOutcome{Kind: kind}
		if kind == "insert" && t.rowIDKey != "" {
			outcome.GeneratedID, e = importNativeCount(native.Results[indices[len(indices)-1]].InsertID)
			if e != nil {
				return unknown(errIncomplete)
			}
		}
		rows[i].Outcome = outcome
		receipt.CommittedRows++
	}
	receipt.State = "committed"
	receipt.Created = b.create
	receipt.Changed = receipt.Changed || b.create || receipt.CommittedRows > 0
	return receipt, rows, version, nil
}

// Official versions omit zero-valued protobuf counts in their JSON response.
// Mutation counts are also checked by the following native changes() guard.
func importNativeCount(n json.Number) (int64, error) {
	if n == "" {
		return 0, nil
	}
	return n.Int64()
}

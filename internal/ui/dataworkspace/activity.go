package dataworkspace

import (
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (s *State) beginActivity(t *Document, source, text string) func(contract.ActivityResult) {
	if s.deps.RecordActivity == nil || t == nil {
		return nil
	}
	return s.deps.RecordActivity(contract.ActivityRequest{ProfileID: t.profileID, Generation: s.sessionFor(t).Generation, Database: t.database, Object: t.table, Source: source, SQL: text, Summary: true})
}

func dataActivitySQL(entries []mysqlDataHistoryEntry) string {
	var b strings.Builder
	for _, entry := range entries {
		if b.Len()+len(entry.SQL) > 64<<10 {
			b.WriteString("\n…")
			break
		}
		if b.Len() > 0 {
			b.WriteString(";\n")
		}
		b.WriteString(entry.SQL)
	}
	return b.String()
}

func dataActivityOutcome(err error, saved *contract.DataEditResult, expected int, pending bool) contract.ActivityResult {
	out := contract.ActivityOutcome(err)
	if saved != nil && saved.Sequential != nil {
		p, valid := contract.SequentialDataWriteProgress(saved, expected)
		if !valid {
			out.State, out.AffectedRows = "uncertain", 0
		} else {
			out.AffectedRows = p.AffectedRows
			if p.Unknown || saved.CommitUncertain {
				out.State = "uncertain"
			} else if !contract.DataWriteComplete(saved, expected) || err != nil {
				if p.Acknowledged > 0 || err == nil {
					out.State = "partial"
				}
			}
		}
		if out.State != "success" {
			out.Error = mysqlDataHistoryError(err, false, valid && !p.Complete, !valid || p.Unknown || saved.CommitUncertain)
		}
		return out
	}
	if err == nil && saved != nil && saved.Async != nil && !saved.CommitConfirmed {
		out.State, out.AffectedRows = "pending", 0
		return out
	}
	uncertain := saved != nil && saved.CommitUncertain
	incomplete := saved == nil || saved.Incomplete || len(saved.Outcomes) != expected
	if uncertain {
		out.State = "uncertain"
	} else if err == nil && incomplete {
		out.State = "partial"
	} else if err == nil && pending {
		out.State = "pending"
	}
	if err != nil || uncertain || incomplete {
		out.Error = mysqlDataHistoryError(err, false, incomplete, uncertain)
	}
	if out.State == "success" || out.State == "pending" {
		out.AffectedRows = int64(saved.Applied)
	}
	return out
}

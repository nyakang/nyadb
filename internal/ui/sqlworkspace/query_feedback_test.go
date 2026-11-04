package sqlworkspace

import (
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"testing"
)

func TestQueryDiagnosticMarksOnlyOriginalChineseExecution(t *testing.T) {
	s := New(Dependencies{})
	defer s.CloseAll()
	sql := "SELECT '中文🙂', broken"
	d := s.Open(2, sql, 1, "db", false)
	position := contract.QueryCharacterPosition(sql, 15)
	cause := errors.New("syntax error")
	err := &contract.QueryError{Cause: cause, Diagnostic: contract.QueryDiagnostic{SQL: sql, Statement: 1, Code: "42601", Position: position}}
	r := executionResult{statement: sql, version: d.Editor.Buffer.Version(), origin: 0, err: err}
	s.acceptQueryDiagnostic(d, r)
	if !errors.Is(d.queryError, cause) || d.diagnostic.Code != "42601" || len(d.Editor.Diagnostics()) != 1 || d.Editor.Diagnostics()[0].Start != *position {
		t.Fatal("diagnostic lost original offset or error chain")
	}
	d.Editor.Insert("-- changed\n")
	s.acceptQueryDiagnostic(d, r)
	if len(d.Editor.Diagnostics()) != 0 || d.diagnostic.SQL != sql {
		t.Fatal("late diagnostic marked modified text")
	}
}

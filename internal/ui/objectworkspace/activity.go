package objectworkspace

import (
	"github.com/nyakang/nyadb/internal/db/contract"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func (s *State) beginActivity(profile int64, generation uint64, database, object, source, summary string) func(contract.ActivityResult) {
	if s.deps.RecordActivity == nil {
		return nil
	}
	return s.deps.RecordActivity(contract.ActivityRequest{ProfileID: profile, Generation: generation, Database: database, Object: object, Source: source, SQL: summary, Summary: true})
}

// Management errors can echo passwords, imported values or private SQL. The
// global log keeps the outcome and directs users to the original operation.
func managementActivityOutcome(err error) contract.ActivityResult {
	out := contract.ActivityOutcome(err)
	if err != nil {
		out.Error = "操作失败；请在原操作界面查看详情。"
	}
	return out
}
func ddlActivityOutcome(outcome mysqlsvc.DDLOutcome) contract.ActivityResult {
	out := managementActivityOutcome(outcome.Error)
	switch outcome.State {
	case "partial", "uncertain":
		out.State = outcome.State
	case "not_executed":
		if outcome.Error == nil {
			out.State = "error"
			out.Error = "操作未执行。"
		}
	case "executed":
	default:
		if outcome.Error == nil {
			out.State = "uncertain"
		}
	}
	return out
}

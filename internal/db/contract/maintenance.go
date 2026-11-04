package contract

import "context"

type Availability struct {
	Enabled bool
	Reason  string // unsupported, disconnected, read_only, invalid_target
}

type MaintenanceAction struct {
	ID, Label     string
	Mutates       bool
	Transactional bool
}
type MaintenanceRequest struct {
	Target SchemaDesignTarget
	Action string
}
type MaintenancePlan struct {
	Request       MaintenanceRequest
	SQL           string
	Transactional bool
}
type MaintenanceMessage struct{ Table, Operation, Type, Message string }
type MaintenanceOutcome struct {
	State    string // not_executed, completed, failed, rolled_back, uncertain
	Messages []MaintenanceMessage
	Error    error
}
type MaintenanceOperations struct {
	Actions func(SchemaDesignTarget) []MaintenanceAction
	Plan    func(context.Context, MaintenanceRequest) (MaintenancePlan, error)
	Execute func(context.Context, MaintenancePlan) MaintenanceOutcome
}
type MaintenanceProvider interface{ Maintenance() MaintenanceOperations }

type MaintenanceProgress struct{ Phase string }

// Availability reads only published, target-bound actions. Plan checks the
// actual server permissions and catalog restrictions on its worker.
func (o MaintenanceOperations) Availability(target SchemaDesignTarget, action string) Availability {
	if o.Actions != nil && o.Plan != nil && o.Execute != nil {
		for _, candidate := range o.Actions(target) {
			if candidate.ID == action {
				return Availability{Enabled: true}
			}
		}
	}
	return Availability{Reason: "unsupported"}
}

// ExecuteWithProgress reports phases; the engines do not expose portable
// page/row totals for every maintenance command, so no percentage is invented.
func (o MaintenanceOperations) ExecuteWithProgress(ctx context.Context, plan MaintenancePlan, progress func(MaintenanceProgress)) MaintenanceOutcome {
	if err := ctx.Err(); err != nil {
		return MaintenanceOutcome{State: "not_executed", Error: err}
	}
	if o.Execute == nil {
		return MaintenanceOutcome{State: "not_executed", Error: ErrUnsupported}
	}
	if progress != nil {
		progress(MaintenanceProgress{Phase: "executing"})
	}
	out := o.Execute(ctx, plan)
	if progress != nil {
		progress(MaintenanceProgress{Phase: out.State})
	}
	return out
}

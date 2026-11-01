package contract

type CommitError struct {
	Result CommitResult
	Err    error
}

func (e *CommitError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "commit requires read-only verification; do not replay"
}
func (e *CommitError) Unwrap() error { return e.Err }

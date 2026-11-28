package contract

import "fmt"

// PartialMetadataError accompanies a complete, independently read subset of
// Browse objects. The remaining category was rejected by the server. Consumers
// may publish that subset only while keeping this notice visible; it is not a
// successful empty result for the failed category. Transport failures, canceled
// reads and incomplete rows must not be represented by this error.
type PartialMetadataError struct {
	Observation NativeMetadataObservation
}

func (e *PartialMetadataError) Error() string {
	o := e.Observation
	return fmt.Sprintf("%s [%d/%s]: %s", o.Command, o.ErrorCode, o.SQLState, o.Error)
}

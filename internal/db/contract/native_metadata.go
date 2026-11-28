package contract

// NativeMetadataObservation preserves a read-only native metadata response.
// It is not an executable SQL definition or a relational constraint assertion.
// A server rejection is distinct from a successful result with zero rows.
type NativeMetadataObservation struct {
	Command         string
	Result          ResultSet
	ErrorCode       uint16
	SQLState, Error string
}

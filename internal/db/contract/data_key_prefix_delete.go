package contract

// DataKeyPrefixDelete asks the original qualified data lease to observe a
// complete visible prefix and prepare one conditional deletion of that captured
// finite key set. It never promises native range absence or deletes later keys.
type DataKeyPrefixDelete struct {
	Object ObjectRef
	Prefix string
}

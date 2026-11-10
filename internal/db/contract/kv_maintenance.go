package contract

// KVMaintenance is an optional inventory on the original KV request lease.
// Each advertised method executes through that lease's ordinary one-use
// Prepare/Execute path. It does not select another endpoint or runtime.
// An inventory is not authorization: the original server decides each RPC.
type KVMaintenance interface {
	MaintenanceMethods() []string
}

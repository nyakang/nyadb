package manticore

import "github.com/nyakang/nyadb/internal/db/contract"

// Native receipts retain the original decoded driver acknowledgement. They do
// not claim transaction commit, row-state verification or raw packet archival.
type nativeSQLReceipt struct {
	Object     contract.NamespaceRef
	Statements []nativeSQLStatement
}
type nativeSQLStatement struct {
	SQL, State                 string
	Dispatched, Acknowledged   bool
	AffectedRows, LastInsertID *int64
	ErrorCode                  uint16
	SQLState, Error            string
}

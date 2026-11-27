package contract

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DatabaseCreateTarget identifies the original session and exact proposed
// namespace. Adapters decide whether it denotes a database, schema or catalog;
// supporting one does not advertise the others.
type DatabaseCreateTarget struct {
	Namespace  NamespaceRef
	Generation uint64
}

type DatabaseCreateDocumentRequest struct {
	DocumentID string
	Target     DatabaseCreateTarget
}

func (r DatabaseCreateDocumentRequest) Valid() bool {
	return r.DocumentID != "" && len(r.DocumentID) <= 256 &&
		utf8.ValidString(r.DocumentID) && strings.IndexFunc(r.DocumentID, unicode.IsControl) < 0 &&
		r.Target.Generation != 0 && r.Target.Namespace.ProfileID > 0 &&
		(r.Target.Namespace.Catalog != "" || r.Target.Namespace.Schema != "")
}

// Revision is a document-bound observation proof, not a server CAS token or
// concurrency lock. NativeID is populated only for an observed existing target.
type DatabaseCreateSnapshot struct {
	Target                           DatabaseCreateTarget
	Engine, NativeID, DDL, CreateSQL string
	Exists                           bool
	Revision                         string
}

// A receipt preserves the original executor response separately from later
// observations. Actual describes post-execution metadata; it cannot prove
// isolation from concurrent create/drop or repair a lost acknowledgement.
type DatabaseCreateReceipt struct {
	DocumentID string
	Sequence   uint64
	Original   DatabaseCreateSnapshot
	SQL        string
	NativeJSON string
	State      string
	Verified   bool
	Actual     *DatabaseCreateSnapshot
}

func (r DatabaseCreateReceipt) Clone() DatabaseCreateReceipt {
	if r.Actual != nil {
		actual := *r.Actual
		r.Actual = &actual
	}
	return r
}

type DatabaseCreateOutcome struct {
	State    string
	Verified bool
	Actual   *DatabaseCreateSnapshot
	Receipt  *DatabaseCreateReceipt
	Error    error
}

func (o DatabaseCreateOutcome) Clone() DatabaseCreateOutcome {
	if o.Actual != nil {
		actual := *o.Actual
		o.Actual = &actual
	}
	if o.Receipt != nil {
		receipt := o.Receipt.Clone()
		o.Receipt = &receipt
	}
	return o
}

var ErrDatabaseCreateConsumed = errors.New("original database creation already completed; this confirmation cannot be reused")

var ErrDatabaseCreateUnresolved = errors.New("original database creation is unresolved; retain its receipt without replay")

// Each lease owns a fixed name and original provider session. Snapshot never
// executes a write or changes OriginalOutcome. Close revokes admission, waits
// for dispatched execution to settle, and releases the retained evidence.
type DatabaseCreateDocumentLease interface {
	Snapshot(context.Context) (DatabaseCreateSnapshot, error)
	Create(context.Context, DatabaseCreateSnapshot) DatabaseCreateOutcome
	OriginalOutcome() *DatabaseCreateOutcome
	Close() error
}

type DatabaseCreateDocumentsProvider interface {
	CanCreateDatabase(DatabaseCreateTarget) bool
	ReserveDatabaseCreateDocument(DatabaseCreateDocumentRequest) (DatabaseCreateDocumentLease, error)
}

package contract

import (
	"context"
	"errors"
)

var (
	ErrKeyLeaseLost = errors.New("original API key connection lost; recover explicitly without replay")
	ErrKeyConflict  = errors.New("original API key changed or disappeared; explicitly read current keys")
	ErrKeyPlanUsed  = errors.New("API key preview changed, replaced or already submitted")
	ErrKeyNoChanges = errors.New("API key metadata has no changes")
)

type KeyRequestError struct {
	Raw   string
	Cause error
}

func (e *KeyRequestError) Error() string { return e.Cause.Error() }
func (e *KeyRequestError) Unwrap() error { return e.Cause }

// Records and response JSON never contain a secret. Fingerprint and Token bind
// the original complete native observation to its captured physical lease.
type KeyRecord struct {
	UID, Name, Description, ExpiresAt, CreatedAt, UpdatedAt string
	Actions, Indexes                                        []string
	Raw, Fingerprint, Token                                 string
}
type KeyPage struct {
	Results              []KeyRecord
	Offset, Limit, Total uint64
	Raw                  string
}
type KeyDraft struct {
	UID, Name, Description, ExpiresAt string
	Actions, Indexes                  []string
}
type KeyPlan struct {
	Object                ObjectRef
	Action, Request, Body string
	Original              KeyRecord
	Draft                 KeyDraft
	Sequence              uint64
	Token                 string
}

// Completed means a synchronous original native response, never task admission.
// Response is redacted; unknown submissions retain their concrete request.
type KeyResult struct {
	State, Action, UID, Request, Response string
	Status                                int
	Record                                KeyRecord
}
type KeyRecovery struct {
	Lease     KeysDocumentLease
	Page      KeyPage
	Last      KeyResult
	Uncertain bool
}
type KeysDocumentLease interface {
	List(context.Context, uint64, uint64) (KeyPage, error)
	Read(context.Context, KeyRecord) (KeyRecord, error)
	Prepare(string, KeyRecord, KeyDraft) (KeyPlan, error) // pure, bounded, one use.
	Apply(context.Context, KeyPlan) (KeyResult, error)
	Reveal(KeyRecord) (string, error) // explicit, pure, temporary original secret.
	Close() error
}
type KeysDocumentsProvider interface {
	SupportsKeysDocument(ObjectRef) bool // scope only; never a permission claim.
	ReserveKeysDocument(ObjectRef) (KeysDocumentLease, error)
	// Revoke the original executor, transfer settled evidence without I/O and
	// leave reads explicit. Unknown writes cannot become retries.
	RecoverKeysDocument(ObjectRef, KeysDocumentLease) (KeyRecovery, error)
}

package contract

import (
	"context"
	"errors"
	"time"
)

var (
	ErrSettingsLeaseLost = errors.New("original settings connection lost; recover explicitly without replay")
	ErrSettingsConflict  = errors.New("original index or settings changed; explicitly read current settings")
	ErrSettingsNoChanges = errors.New("settings have no changes")
	ErrSettingsPlanUsed  = errors.New("settings preview was changed, replaced or already submitted")
)

type SettingsRequestError struct {
	Raw   string
	Cause error
}

func (e *SettingsRequestError) Error() string { return e.Cause.Error() }
func (e *SettingsRequestError) Unwrap() error { return e.Cause }

// SettingsSnapshot retains complete native JSON and an adapter-owned original
// index/connection proof. Fingerprint preserves exact JSON number spellings.
type SettingsSnapshot struct {
	Object                                ObjectRef
	IndexCreated, Raw, Fingerprint, Token string
}

// Draft is the complete desired object; Patch follows the native merge rules.
// Omitted setting fields become null resets, unchanged fields are not sent,
// and dictionaries that the server replaces are submitted in full.
type SettingsPlan struct {
	Original     SettingsSnapshot
	Draft, Patch string
	Reset        bool
	Fields       []string
	Sequence     uint64
	Token        string
}

// A receipt proves admission only. Token binds the exact original native tuple
// and response to the owning lease; recovery transfers only private evidence.
type SettingsReceipt struct {
	Object                                          ObjectRef
	IndexCreated, UID, Type, EnqueuedAt, Raw, Token string
}
type SettingsResult struct {
	State    string
	Receipt  SettingsReceipt
	TaskRaw  string
	Snapshot SettingsSnapshot
}
type SettingsRecovery struct {
	Lease     SettingsDocumentLease
	Snapshot  SettingsSnapshot
	Receipt   SettingsReceipt
	Uncertain bool
}

// Prepare is pure and performs no I/O. Apply consumes its one-use preview,
// compares currently observed settings, and never replays a mutating request.
// Observe is explicit and bounded. HTTP admission is never task completion.
type SettingsDocumentLease interface {
	Read(context.Context) (SettingsSnapshot, error)
	Prepare(SettingsSnapshot, string, bool) (SettingsPlan, error)
	Apply(context.Context, SettingsPlan) (SettingsResult, error)
	Observe(context.Context, SettingsReceipt, time.Duration) (SettingsResult, error)
	Close() error
}
type SettingsDocumentsProvider interface {
	SupportsSettingsDocument(ObjectRef) bool
	ReserveSettingsDocument(ObjectRef) (SettingsDocumentLease, error)
	// Recovery performs no network I/O, retains the original index identity,
	// transfers proven receipts only, closes only the previous original lease,
	// and leaves reads/observations explicit. Uncertain writes remain blocked.
	RecoverSettingsDocument(ObjectRef, SettingsDocumentLease) (SettingsRecovery, error)
}

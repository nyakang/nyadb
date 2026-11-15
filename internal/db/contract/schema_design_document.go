package contract

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// SchemaDesignDocumentRequest reserves a stable workbench document in one
// original session. A table rename does not change DocumentID.
type SchemaDesignDocumentRequest struct {
	DocumentID string
	Target     SchemaDesignTarget
}

func (r SchemaDesignDocumentRequest) Valid() bool {
	return r.DocumentID != "" && len(r.DocumentID) <= 256 &&
		utf8.ValidString(r.DocumentID) && strings.IndexFunc(r.DocumentID, unicode.IsControl) < 0 &&
		r.Target.Generation != 0 && r.Target.Object.ProfileID > 0 &&
		r.Target.Object.Kind == "table" && r.Target.Object.Name != ""
}

// Optional document ownership does not change legacy session-level providers.
// Close revokes admission and waits for original dispatched acknowledgements.
type SchemaDesignDocumentLease interface {
	Operations() SchemaDesignOperations
	// OriginalOutcome returns an isolated copy of the last original write,
	// without reading the server, recovering a commit, or executing a request.
	// Reads never clear it. Close releases the retained document evidence.
	OriginalOutcome() *SchemaDesignOutcome
	Close() error
}

type SchemaDesignDocumentsProvider interface {
	ReserveSchemaDesignDocument(SchemaDesignDocumentRequest) (SchemaDesignDocumentLease, error)
}

// Document features describe original-lease operations without reservation or
// I/O. They do not expose session-level executors or transfer a document proof.
// Each executor still validates the original complete request after admission.
type SchemaDesignDocumentFeatures struct {
	CanApply  func(SchemaDesignTarget) bool
	CanDrop   func(SchemaDesignTarget) bool
	CanCopy   func(SchemaDesignTarget) bool
	CanRename func(SchemaDesignTarget) bool
	CanClear  func(SchemaDesignTarget) bool
}

type SchemaDesignDocumentFeaturesProvider interface {
	SchemaDesignDocumentFeatures() SchemaDesignDocumentFeatures
}

var ErrSchemaCommitUncertain = errors.New("original schema write remains unverified; replay is prohibited")

// SchemaDesignWriteReceipt is complete original evidence, not a fresh snapshot
// or a synthesized server response. NativeJSON is the original bounded response
// (or its complete transport envelope); it is empty when no response survived.
// State, Verified and Actual describe that original acknowledgement. A host may
// reject an outcome without altering any of these original receipt fields.
type SchemaDesignWriteReceipt struct {
	DocumentID           string
	Sequence             uint64
	Operation            string
	Original             SchemaDesignSnapshot
	Desired              *schemaddl.Table
	Destination          string
	AllowColumnDrop      bool
	WithData             bool
	RebindSelfReferences bool
	PreserveRowID        bool
	ResetIdentity        bool
	Statements           []string
	NativeJSON           string
	State                string
	Verified             bool
	Rows                 int64
	Actual               *SchemaDesignSnapshot
}

func (r SchemaDesignWriteReceipt) Clone() SchemaDesignWriteReceipt {
	r.Original = r.Original.Clone()
	if r.Desired != nil {
		desired := r.Desired.Clone()
		r.Desired = &desired
	}
	if r.Statements != nil {
		r.Statements = append([]string{}, r.Statements...)
	}
	if r.Actual != nil {
		actual := r.Actual.Clone()
		r.Actual = &actual
	}
	return r
}

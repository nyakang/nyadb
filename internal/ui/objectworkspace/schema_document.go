package objectworkspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Owned only by the UI thread. Workers capture the original bound operations
// and immutable request, never this owner or the current session binding.
type schemaDocumentOwner struct {
	id                string
	target            contract.SchemaDesignTarget
	lease             contract.SchemaDesignDocumentLease
	last              *contract.SchemaDesignOutcome
	sequence          uint64
	blocked           bool
	leaveAcknowledged bool
	showReceipt       bool
	receiptText       string
	receiptPreview    string
	receiptTruncated  bool
	transfer          *schemaReceiptTransfer
	transferError     string
}

func (s *State) reserveSchemaDocument(id string, target contract.SchemaDesignTarget) (*schemaDocumentOwner, error) {
	if s.deps.SchemaDocumentsFor == nil {
		return nil, nil // Legacy providers retain their existing session binding.
	}
	p, err := s.deps.SchemaDocumentsFor(target.Object.ProfileID)
	if errors.Is(err, contract.ErrUnsupported) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, contract.ErrUnsupported
	}
	l, err := p.ReserveSchemaDesignDocument(contract.SchemaDesignDocumentRequest{DocumentID: id, Target: target})
	if err != nil || l == nil {
		if l != nil {
			_ = l.Close()
		}
		if err == nil {
			err = contract.ErrUnsupported
		}
		return nil, err
	}
	return &schemaDocumentOwner{id: id, target: target, lease: l}, nil
}

func (s *State) nextSchemaDocumentID(operation string) string {
	s.schemaSequence++
	return fmt.Sprintf("schema-%s:%d", operation, s.schemaSequence)
}

func (s *State) schemaDocumentsAvailable(profile int64) bool {
	if s.deps.SchemaDocumentsFor == nil {
		return false
	}
	p, err := s.deps.SchemaDocumentsFor(profile)
	return err == nil && p != nil
}

func (x *schemaDocumentOwner) settle(expected contract.SchemaDesignWriteReceipt, out contract.SchemaDesignOutcome) contract.SchemaDesignOutcome {
	if x == nil {
		return out.Clone()
	}
	out = out.Clone()
	r := out.Receipt
	if r != nil {
		valid := r.DocumentID == x.id && r.Sequence > x.sequence && r.Operation == expected.Operation &&
			reflect.DeepEqual(r.Original, expected.Original) && reflect.DeepEqual(r.Desired, expected.Desired) &&
			r.Destination == expected.Destination && r.AllowColumnDrop == expected.AllowColumnDrop &&
			r.WithData == expected.WithData && r.RebindSelfReferences == expected.RebindSelfReferences &&
			r.PreserveRowID == expected.PreserveRowID && r.State == out.State && r.Verified == out.Verified &&
			r.Rows == out.Rows && reflect.DeepEqual(r.Actual, out.Actual)
		valid = valid && len(r.Statements) > 0 && out.Rows >= 0
		if r.State == "committed" || r.State == "rolled_back" {
			valid = valid && r.NativeJSON != "" && json.Valid([]byte(r.NativeJSON))
		}
		if out.Verified {
			target := expected.Original.Target
			target.Object.Name = expected.Destination
			valid = valid && out.State == "committed" && out.Error == nil && out.Actual != nil &&
				out.Actual.Target == target && out.Actual.Schema.Engine == expected.Original.Schema.Engine &&
				out.Actual.Schema.Exists == (expected.Operation != "drop") && out.Actual.Schema.Table.Name == expected.Destination
			if out.Actual != nil && out.Actual.Schema.SQLite != nil {
				valid = valid && out.Actual.Schema.SQLite.Validate() == nil
			}
		}
		if !valid {
			out.Verified = false
			if out.State != "committed" {
				out.State = "uncertain"
			}
			out.Error = errors.Join(out.Error, contract.ErrSchemaCommitUncertain)
		} else {
			x.sequence = r.Sequence
		}
	} else if out.State != "not_executed" {
		out.Verified = false
		if out.State != "committed" {
			out.State = "uncertain"
		}
		out.Error = errors.Join(out.Error, contract.ErrSchemaCommitUncertain)
	}
	if out.State == "uncertain" || out.State == "committed" && (!out.Verified || out.Error != nil) {
		x.blocked = true
		x.leaveAcknowledged = false
	}
	if r != nil || out.State != "not_executed" {
		last := out.Clone()
		x.last = &last
		x.receiptText, x.receiptPreview = "", ""
		x.receiptTruncated = false
	}
	return out
}

func (x *schemaDocumentOwner) original() *contract.SchemaDesignOutcome {
	if x == nil || x.last == nil {
		return nil
	}
	out := x.last.Clone()
	return &out
}

func (x *schemaDocumentOwner) close() {
	if x == nil {
		return
	}
	l := x.lease
	if x.transfer != nil {
		x.transfer.cancel()
		x.transfer = nil
	}
	x.lease, x.last = nil, nil
	x.receiptText, x.receiptPreview, x.transferError = "", "", ""
	x.id = ""
	if l != nil {
		_ = l.Close() // Wait for the original executor; never obtain another lease.
	}
}

func schemaSQLQualifier(snapshot contract.SchemaDesignSnapshot) string {
	if snapshot.Schema.Engine == "sqlite" && snapshot.Target.Object.Schema == "" {
		return snapshot.Target.Object.Catalog
	}
	return snapshot.Target.Object.Schema
}

func (s *State) SchemaTableOriginalOutcome(operation string) *contract.SchemaDesignOutcome {
	switch operation {
	case "drop":
		if s.schemaDrop != nil {
			return s.schemaDrop.owner.original()
		}
	case "copy":
		if s.schemaCopy != nil {
			return s.schemaCopy.owner.original()
		}
	case "rename":
		if s.schemaRename != nil {
			return s.schemaRename.owner.original()
		}
	}
	return nil
}

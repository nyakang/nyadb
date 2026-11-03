package objectworkspace

import "github.com/nyakang/nyadb/internal/db/contract"

// programDocument holds an immutable qualified identity and its current metadata.
// Creation, update, presentation and disposal belong to the UI thread.
type programDocument struct {
	profileID            int64
	database, name, kind string
	data                 contract.DBObject
	closed               bool
}

// Supports recognizes read-only routines, events and sequence definitions.
func Supports(kind string) bool {
	return kind == "函数" || kind == "存储过程" || kind == "事件" || kind == "序列"
}

// New copies metadata without activating a profile or opening a host tab.
func newProgram(profileID int64, database string, data contract.DBObject) *programDocument {
	if profileID <= 0 || database == "" || data.Name == "" || !Supports(data.Kind) {
		return nil
	}
	if data.Ref != (contract.ObjectRef{}) {
		namespace := data.Ref.Schema
		if namespace == "" {
			namespace = data.Ref.Catalog
		}
		if data.Ref.ProfileID != profileID || namespace != database || data.Ref.Name != data.Name || data.Ref.Kind != searchObjectKind(data.Kind) {
			return nil
		}
	}
	return &programDocument{profileID: profileID, database: database, name: data.Name, kind: data.Kind, data: copyMetadata(data)}
}

func (d *programDocument) ProfileID() int64 { return d.profileID }
func (d *programDocument) Database() string { return d.database }
func (d *programDocument) Name() string     { return d.name }
func (d *programDocument) Kind() string     { return d.kind }

// Data returns a copy for read-only host presentation.
func (d *programDocument) Data() contract.DBObject { return copyMetadata(d.data) }

// Update replaces a snapshot only for this document's original object identity.
// Reopening an existing tab supplies fresh cached metadata through this method.
func (d *programDocument) Update(data contract.DBObject) bool {
	if d == nil || d.closed || data.Name != d.name || data.Kind != d.kind || data.Ref != d.data.Ref {
		return false
	}
	d.data = copyMetadata(data)
	return true
}

// Close releases metadata, including large definitions. It is idempotent and
// leaves identity available for host cleanup while preventing subsequent reuse.
func (d *programDocument) Close() {
	if d == nil || d.closed {
		return
	}
	d.closed = true
	d.data = contract.DBObject{}
}

func copyMetadata(data contract.DBObject) contract.DBObject {
	copyNumber := func(n *int64) *int64 {
		if n == nil {
			return nil
		}
		value := *n
		return &value
	}
	data.AutoIncrement = copyNumber(data.AutoIncrement)
	data.DataLength = copyNumber(data.DataLength)
	data.RowsEstimate = copyNumber(data.RowsEstimate)
	if data.Sequence != nil {
		sequence := *data.Sequence
		data.Sequence = &sequence
	}
	return data
}

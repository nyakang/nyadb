// Package programviewer owns read-only function, procedure and event documents.
// The host supplies metadata snapshots and coordinates tabs and sessions; this
// package never requests metadata, executes SQL or changes a database object.
package programviewer

import mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"

// Document holds an immutable qualified identity and its current metadata.
// Creation, update, presentation and disposal belong to the UI thread.
type Document struct {
	profileID            int64
	database, name, kind string
	data                 mysqlsvc.MySQLDBObject
	closed               bool
}

// Supports recognizes program kinds in the existing MySQL metadata vocabulary.
func Supports(kind string) bool {
	return kind == "函数" || kind == "存储过程" || kind == "事件"
}

// New copies metadata without activating a profile or opening a host tab.
func New(profileID int64, database string, data mysqlsvc.MySQLDBObject) *Document {
	if profileID <= 0 || database == "" || data.Name == "" || !Supports(data.Kind) {
		return nil
	}
	return &Document{profileID: profileID, database: database, name: data.Name, kind: data.Kind, data: copyMetadata(data)}
}

func (d *Document) ProfileID() int64 { return d.profileID }
func (d *Document) Database() string { return d.database }
func (d *Document) Name() string     { return d.name }
func (d *Document) Kind() string     { return d.kind }

// Data returns a copy for read-only host presentation.
func (d *Document) Data() mysqlsvc.MySQLDBObject { return copyMetadata(d.data) }

// Update replaces a snapshot only for this document's original object identity.
// Reopening an existing tab supplies fresh cached metadata through this method.
func (d *Document) Update(data mysqlsvc.MySQLDBObject) bool {
	if d == nil || d.closed || data.Name != d.name || data.Kind != d.kind {
		return false
	}
	d.data = copyMetadata(data)
	return true
}

// Close releases metadata, including large definitions. It is idempotent and
// leaves identity available for host cleanup while preventing subsequent reuse.
func (d *Document) Close() {
	if d == nil || d.closed {
		return
	}
	d.closed = true
	d.data = mysqlsvc.MySQLDBObject{}
}

func copyMetadata(data mysqlsvc.MySQLDBObject) mysqlsvc.MySQLDBObject {
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
	return data
}

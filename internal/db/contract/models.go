// Package contract defines database values and requests without UI or drivers.
package contract

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"time"
)

type ObjectRef struct {
	ProfileID                   int64
	Catalog, Schema, Kind, Name string
}

type CellKind string

const (
	Null     CellKind = "null"
	Text     CellKind = "text"
	Integer  CellKind = "integer"
	Decimal  CellKind = "decimal"
	Floating CellKind = "floating"
	Boolean  CellKind = "boolean"
	Binary   CellKind = "binary"
	DateTime CellKind = "datetime"
	JSON     CellKind = "json"
)

type Cell struct {
	Kind      CellKind `json:"kind"`
	Text      string   `json:"text,omitempty"`
	Bytes     []byte   `json:"bytes,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

func (c Cell) Display() string {
	if c.Kind == Null {
		return "NULL"
	}
	if c.Kind == Binary {
		return "0x" + hex.EncodeToString(c.Bytes)
	}
	return c.Text
}
func (c Cell) Clone() Cell {
	if c.Bytes != nil {
		raw := make([]byte, len(c.Bytes))
		copy(raw, c.Bytes)
		c.Bytes = raw
	}
	return c
}

type Column struct {
	Name, DatabaseType string
	Nullable           bool
}
type ResultSet struct {
	AffectedRows int64
	Columns      []Column
	Rows         [][]Cell
	Truncation   []string
	// Presentation hints leave original source ordinals intact. User layouts
	// override default hidden columns; LayoutKey excludes volatile type hints.
	DefaultHiddenColumns []int `json:",omitempty"`
	// OriginColumns maps rendered columns to original native source ordinals.
	// An empty hint means the source ordinals already match.
	OriginColumns []int  `json:",omitempty"`
	LayoutKey     string `json:",omitempty"`
}
type SQLResult struct {
	Steps []StatementOutcome
	Sets  []ResultSet
	// Graph retains the original typed result and completion receipt. Sets is
	// the common grid projection; it cannot reconstruct graph identities/types.
	Graph *GraphResult
	// Columns and Rows are a presentation adapter for existing MySQL callers.
	// Sets is authoritative; adapters never infer NULL from display text.
	Columns      []string
	Rows         [][]string
	AffectedRows int64
	Duration     time.Duration
	Truncated    bool
}

// StatementEnqueued describes native asynchronous admission only. A successful
// HTTP receipt does not confirm completion or contribute affected SQL rows.
const StatementEnqueued = "enqueued"

type StatementOutcome struct {
	SQL, State, Error string
	AffectedRows      int64
}

func (r *SQLResult) SetLegacyView() {
	if len(r.Sets) == 0 {
		return
	}
	s := r.Sets[0]
	r.Columns = make([]string, len(s.Columns))
	for i, c := range s.Columns {
		r.Columns[i] = c.Name
	}
	r.Rows = make([][]string, len(s.Rows))
	for i, row := range s.Rows {
		r.Rows[i] = make([]string, len(row))
		for j, c := range row {
			r.Rows[i][j] = c.Display()
		}
	}
	for _, s := range r.Sets {
		if len(s.Truncation) > 0 {
			r.Truncated = true
		}
	}
}
func (r SQLResult) ResultSets() []ResultSet {
	if len(r.Sets) > 0 {
		return r.Sets
	}
	if len(r.Columns) == 0 {
		return nil
	}
	s := ResultSet{Columns: make([]Column, len(r.Columns)), Rows: make([][]Cell, len(r.Rows))}
	for i, n := range r.Columns {
		s.Columns[i].Name = n
	}
	for i, row := range r.Rows {
		s.Rows[i] = make([]Cell, len(row))
		for j, v := range row {
			s.Rows[i][j] = Cell{Kind: Text, Text: v}
		}
	}
	if r.Truncated {
		s.Truncation = []string{"legacy"}
	}
	return []ResultSet{s}
}

type Capabilities struct {
	KeyValues                                                                    bool
	Documents                                                                    bool
	Objects, Maintenance, Accounts, Restore, DesignViews                         bool
	Schemas, Transactions, EditableTables, Explain, DesignTables, Import, Backup bool
	Query, Metadata, Keyset, Count, FieldChunks, Analysis, Export, JSONL         bool
}
type PageRequest struct {
	Condition *FilterExpression[Filter]
	Object    ObjectRef
	Offset    int64
	Limit     int
	Filters   []Filter
	Sorts     []Sort
}
type Filter struct {
	Column, Operator string
	Value            Cell
}
type Sort struct {
	Column     string
	Descending bool
}
type RowIdentity struct {
	Columns []string
	Values  []Cell
}
type Edit struct {
	Kind     string
	Identity RowIdentity
	Original []Cell
	Values   map[string]Cell
}
type CommitResult struct {
	CommitConfirmed bool
	Applied         int
	Uncertain       bool
	Incomplete      bool
	Verified        bool
	Saved           []Edit
}
type Lease interface {
	Preconnect(context.Context) error
	Execute(context.Context, string, string) (SQLResult, error)
	Release()
}

// MarshalJSON retains empty binary payloads as bytes:"", while nil remains absent.
// This preserves raw row evidence without conflating it with a SQL NULL cell.
func (c Cell) MarshalJSON() ([]byte, error) {
	type encoded struct {
		Kind      CellKind `json:"kind"`
		Text      string   `json:"text,omitempty"`
		Bytes     *[]byte  `json:"bytes,omitempty"`
		Truncated bool     `json:"truncated,omitempty"`
	}
	value := encoded{Kind: c.Kind, Text: c.Text, Truncated: c.Truncated}
	if c.Bytes != nil {
		value.Bytes = &c.Bytes
	}
	return json.Marshal(value)
}

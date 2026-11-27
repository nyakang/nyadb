package prestosql

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

const dataDigestColumn = "__nyadb_row_fingerprint"
const dataGrantColumn = "__nyadb_prestosql_observation"

type dataReadGrant struct{ digest, metadata string }

func (p *dataProjection) decodeCaptured(result queryResult) ([][]contract.DataCell, []string, error) {
	n := len(p.columns)
	if len(result.columns) != n+1 || result.columns[n] != dataDigestColumn {
		return nil, nil, ErrResponse
	}
	result.columns = result.columns[:n]
	rows := make([][]json.RawMessage, len(result.rows))
	digests := make([]string, len(rows))
	for i, row := range result.rows {
		if len(row) != n+1 || json.Unmarshal(row[n], &digests[i]) != nil || len(digests[i]) != 64 {
			return nil, nil, ErrResponse
		}
		for _, ch := range digests[i] {
			if !(ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'F') {
				return nil, nil, ErrResponse
			}
		}
		rows[i] = row[:n]
	}
	result.rows = rows
	values, err := p.decode(result)
	return values, digests, err
}
func (l *dataLease) publishGrants(ctx context.Context, sequence uint64, m *contract.TableStructure, page *contract.DataPage, digests []string) error {
	if len(digests) < len(page.Rows) {
		return ErrResponse
	}
	grants := map[string]*dataReadGrant{}
	signature := dataMetadataSignature(m)
	for i := range page.Rows {
		token := rand.Text()
		grants[token] = &dataReadGrant{digest: digests[i], metadata: signature}
		page.RowRefs = append(page.RowRefs, &contract.RowRef{Object: l.object, Database: page.Database, Table: page.Table, Columns: []string{dataGrantColumn}, Values: [][]byte{[]byte(token)}})
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return contract.ErrRevoked
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if sequence != l.pageSequence {
		return contract.ErrDataConflict
	}
	l.grants = grants
	return nil
}
func (l *dataLease) readGrant(ref contract.RowRef) (string, *dataReadGrant, error) {
	if ref.Object != l.object || ref.Database != namespaceLabel(l.object.Namespace()) || ref.Table != l.object.Name || len(ref.Columns) != 1 || ref.Columns[0] != dataGrantColumn || len(ref.Values) != 1 || len(ref.Values[0]) > 128 || len(ref.TypedValues) != 0 {
		return "", nil, connections.ErrInvalid
	}
	token := string(ref.Values[0])
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return "", nil, contract.ErrRevoked
	}
	grant := l.grants[token]
	if grant == nil {
		return "", nil, contract.ErrDataConflict
	}
	return token, grant, nil
}
func (l *dataLease) checkGrant(token string, grant *dataReadGrant) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return contract.ErrRevoked
	}
	if l.grants[token] != grant {
		return contract.ErrDataConflict
	}
	return nil
}

// A field reader validates one complete original value observation. It cannot
// turn an ordinal, a duplicate row, or a changed row into a physical identity.
func (l *dataLease) chunk(ctx context.Context, id int64, db, table, column string, ref contract.RowRef, offset int64, limit int) (*contract.FieldChunk, error) {
	if err := l.identity(id, db, table); err != nil {
		return nil, err
	}
	if offset < 0 || offset > math.MaxInt64-1025 || limit < 1 || limit > dataPreviewUnits {
		return nil, connections.ErrInvalid
	}
	token, grant, err := l.readGrant(ref)
	if err != nil {
		return nil, err
	}
	child, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	m, err := l.session.TableMetadata(child, l.object)
	if err != nil {
		return nil, err
	}
	if dataMetadataSignature(m) != grant.metadata {
		return nil, contract.ErrDataConflict
	}
	p, err := newDataProjection(m, dataPreviewUnits)
	if err != nil {
		return nil, err
	}
	index := -1
	for i, c := range m.Columns {
		if c.Name == column {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, connections.ErrInvalid
	}
	b := projectionBuilder{used: map[string]bool{}}
	for _, c := range m.Columns {
		b.used[strings.ToLower(c.Name)] = true
	}
	value := quoteName("__nyadb_data") + "." + quoteName(column)
	text, err := b.text(p.types[index], value)
	if err != nil {
		return nil, err
	}
	length := "length(" + text + ")"
	fragment := fmt.Sprintf("substr(%s,%d,%d)", text, offset+1, limit)
	binary := p.types[index].kind == contract.Binary
	if binary {
		length = "length(" + value + ")"
		fragment = fmt.Sprintf("to_base64(substr(%s,%d,%d))", value, offset+1, limit)
	}
	envelope := "json_format(CAST(ARRAY[CAST(" + fragment + " AS JSON),CAST(COALESCE(" + length + ",0) AS JSON)] AS JSON))"
	source := "SELECT " + envelope + " AS " + quoteName("__nyadb_field") + " FROM " + qualified(l.object) + " AS " + quoteName("__nyadb_data") + " WHERE " + p.rowDigest + "=" + quoteText(grant.digest) + " LIMIT 2"
	l.session.mu.Lock()
	owner, err := l.session.dataOwnerLocked(l.ctx, "data-field", l.session.auxSlots)
	l.session.mu.Unlock()
	if err != nil {
		return nil, err
	}
	defer owner.Release()
	r, _, err := owner.executeNative(child, jdbcplugin.Query{SQL: source, MaxRows: 2, TimeoutSeconds: l.session.config.QueryTimeoutSeconds})
	if err != nil {
		return nil, err
	}
	if !r.completed || r.truncated || len(r.columns) != 1 || r.columns[0] != "__nyadb_field" {
		return nil, ErrResponse
	}
	if len(r.rows) != 1 {
		return nil, contract.ErrDataConflict
	}
	if len(r.rows[0]) != 1 {
		return nil, ErrResponse
	}
	chunk, err := decodeField(r.rows[0][0], binary, offset, limit)
	if err != nil {
		return nil, err
	}
	after, err := l.session.TableMetadata(child, l.object)
	if err != nil {
		return nil, err
	}
	if dataMetadataSignature(after) != grant.metadata {
		return nil, contract.ErrDataConflict
	}
	if err = l.checkGrant(token, grant); err != nil {
		return nil, err
	}
	if err = child.Err(); err != nil {
		return nil, err
	}
	return chunk, nil
}
func decodeField(raw json.RawMessage, binary bool, offset int64, limit int) (*contract.FieldChunk, error) {
	var encoded string
	if json.Unmarshal(raw, &encoded) != nil {
		return nil, ErrResponse
	}
	var fields []json.RawMessage
	if json.Unmarshal([]byte(encoded), &fields) != nil || len(fields) != 2 {
		return nil, ErrResponse
	}
	total, err := strconv.ParseInt(string(fields[1]), 10, 64)
	if err != nil || total < 0 {
		return nil, ErrResponse
	}
	if offset > total {
		return nil, connections.ErrInvalid
	}
	result := &contract.FieldChunk{Offset: offset, NextOffset: offset, TotalUnits: total, Binary: binary}
	if string(fields[0]) == "null" {
		if total != 0 || offset != 0 {
			return nil, ErrResponse
		}
		result.Null = true
		return result, nil
	}
	var value string
	if json.Unmarshal(fields[0], &value) != nil || !utf8.ValidString(value) {
		return nil, ErrResponse
	}
	units := int64(utf8.RuneCountInString(value))
	result.Text = value
	if binary {
		data, e := base64.StdEncoding.Strict().DecodeString(value)
		if e != nil {
			return nil, ErrResponse
		}
		units = int64(len(data))
		result.Text = "0x" + hex.EncodeToString(data)
	}
	expected := min(int64(limit), total-offset)
	if units != expected {
		return nil, ErrResponse
	}
	result.NextOffset = offset + units
	result.HasMore = result.NextOffset < total
	return result, nil
}

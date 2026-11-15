package h2

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) DataEdits() contract.DataEdits {
	if s.profile.H2Options.ReadOnly {
		return contract.DataEdits{}
	}
	return contract.DataEdits{Saver: s.save, Verify: s.verify}
}

var numberLiteral = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// The original H2 agent accepts native SQL strings for these operations. Only
// validated typed literals and quoted metadata identifiers enter generated SQL.
func valueLiteral(c contract.StructureColumn, v contract.DataValue) (string, error) {
	if !readable(c) || v.Default {
		return "", contract.ErrUnsupported
	}
	if len(v.Text)+len(v.Bytes) > contract.DataEditMaxBytes || !utf8.ValidString(v.Text) || strings.ContainsRune(v.Text, 0) {
		return "", contract.ErrDataEditLimit
	}
	if v.Null {
		if !c.Nullable {
			return "", connections.ErrInvalid
		}
		return "NULL", nil
	}
	if v.Binary || v.Kind == contract.Binary {
		if c.ValueKind != contract.Binary {
			return "", connections.ErrInvalid
		}
		return "X'" + hex.EncodeToString(v.Bytes) + "'", nil
	}
	if c.ValueKind == contract.Binary {
		return "", connections.ErrInvalid
	}
	literal := textLiteral(v.Text)
	switch c.ValueKind {
	case contract.Integer:
		n, ok := new(big.Int).SetString(v.Text, 10)
		bits := map[string]uint{"TINYINT": 8, "SMALLINT": 16, "INT": 32, "INTEGER": 32, "BIGINT": 64}[strings.ToUpper(c.DataType)]
		if !ok || bits == 0 {
			return "", connections.ErrInvalid
		}
		bound := new(big.Int).Lsh(big.NewInt(1), bits-1)
		if n.Cmp(bound) >= 0 || n.Cmp(new(big.Int).Neg(bound)) < 0 {
			return "", connections.ErrInvalid
		}
		literal = n.String()
	case contract.Decimal, contract.Floating:
		if !numberLiteral.MatchString(v.Text) {
			return "", connections.ErrInvalid
		}
		if c.ValueKind == contract.Decimal && (c.DataType == "DECIMAL" || c.DataType == "NUMERIC") {
			if c.NumericPrecision == nil || c.NumericScale == nil || *c.NumericPrecision < 1 || *c.NumericPrecision > 100000 || *c.NumericScale < 0 || *c.NumericScale > *c.NumericPrecision || len(v.Text) > 100000 {
				return "", contract.ErrUnsupported
			}
			n, ok := new(big.Rat).SetString(v.Text)
			if !ok || strings.ContainsAny(v.Text, "eE") {
				return "", connections.ErrInvalid
			}
			factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(*c.NumericScale), nil)
			scaled := new(big.Rat).Mul(n, new(big.Rat).SetInt(factor))
			bound := new(big.Int).Exp(big.NewInt(10), big.NewInt(*c.NumericPrecision), nil)
			if !scaled.IsInt() || new(big.Int).Abs(scaled.Num()).Cmp(bound) >= 0 {
				return "", connections.ErrInvalid
			}
		}
	case contract.Boolean:
		b, err := strconv.ParseBool(v.Text)
		if err != nil {
			return "", connections.ErrInvalid
		}
		if b {
			literal = "TRUE"
		} else {
			literal = "FALSE"
		}
	case contract.JSON:
		if !json.Valid([]byte(v.Text)) {
			return "", connections.ErrInvalid
		}
		return "JSON " + literal, nil
	case contract.DateTime:
		return strings.ToUpper(c.DataType) + " " + literal, nil
	}
	// Type is never interpolated: select the admitted canonical type and add
	// numeric modifiers only after validating their native metadata values.
	typ := strings.ToUpper(c.DataType)
	if (typ == "DECIMAL" || typ == "NUMERIC") && c.NumericPrecision != nil && c.NumericScale != nil {
		typ += "(" + strconv.FormatInt(*c.NumericPrecision, 10) + "," + strconv.FormatInt(*c.NumericScale, 10) + ")"
	}
	if typ == "CHAR" || typ == "CHARACTER" {
		if c.CharacterMaxLength == nil || *c.CharacterMaxLength < 1 || *c.CharacterMaxLength > contract.DataEditMaxBytes {
			return "", contract.ErrUnsupported
		}
		typ += "(" + strconv.FormatInt(*c.CharacterMaxLength, 10) + ")"
	}
	return "CAST(" + literal + " AS " + typ + ")", nil
}

func cellLiteral(c contract.StructureColumn, v contract.Cell) (string, error) {
	if v.Truncated || v.Kind != contract.Null && v.Kind != c.ValueKind {
		return "", connections.ErrInvalid
	}
	return valueLiteral(c, contract.DataValue{Kind: v.Kind, Text: v.Text, Bytes: v.Bytes, Null: v.Kind == contract.Null, Binary: v.Kind == contract.Binary})
}

func literalFingerprint(c contract.StructureColumn, literal string) string {
	value := literal
	if c.ValueKind == contract.Binary {
		// Fixed BINARY keys are padded by H2, unlike VARBINARY literals.
		if c.DataType == "BINARY" && c.CharacterMaxLength != nil && *c.CharacterMaxLength > 0 && *c.CharacterMaxLength <= contract.DataEditMaxBytes {
			value = "CAST(" + value + " AS BINARY(" + strconv.FormatInt(*c.CharacterMaxLength, 10) + "))"
		}
	} else {
		value = "STRINGTOUTF8(CAST(" + value + " AS VARCHAR))"
	}
	return "RAWTOHEX(HASH('SHA256'," + value + "))"
}

func keyWhere(t *contract.TableStructure, ref contract.RowRef) (string, error) {
	keys := keyColumns(t)
	if ref.Object != t.Object || ref.Database != t.Object.Schema || ref.Table != t.Name || len(keys) == 0 || len(ref.Columns) != len(keys) || len(ref.TypedValues) != len(keys) || len(ref.Values) != 0 && len(ref.Values) != len(keys) {
		return "", connections.ErrInvalid
	}
	var parts []string
	for i, key := range keys {
		v := ref.TypedValues[i]
		if ref.Columns[i] != key || v.Kind == contract.Null || v.Truncated {
			return "", connections.ErrInvalid
		}
		data := []byte(v.Text)
		if v.Kind == contract.Binary {
			data = v.Bytes
		}
		if len(ref.Values) > 0 && !bytes.Equal(ref.Values[i], data) {
			return "", connections.ErrInvalid
		}
		for _, c := range t.Columns {
			if c.Name == key {
				literal, err := cellLiteral(c, v)
				if err != nil {
					return "", err
				}
				parts = append(parts, fingerprint(c)+"="+literalFingerprint(c, literal))
			}
		}
	}
	return strings.Join(parts, " AND "), nil
}

func (s *Session) originalWhere(t *contract.TableStructure, snap *contract.RowSnapshot) (string, error) {
	if snap == nil || snap.Metadata != s.metadataSeal(t) || len(snap.Columns) != len(t.Columns) {
		return "", connections.ErrInvalid
	}
	where, err := keyWhere(t, snap.Ref)
	if err != nil {
		return "", err
	}
	if snap.Mode == "fingerprints" && len(snap.Fingerprints) != len(t.Columns) || snap.Mode == "values" && len(snap.Original) != len(t.Columns) {
		return "", connections.ErrInvalid
	}
	for i, c := range t.Columns {
		if snap.Columns[i] != c.Name {
			return "", connections.ErrInvalid
		}
		switch snap.Mode {
		case "fingerprints":
			if snap.Fingerprints[i] == nil {
				where += " AND " + quote(c.Name) + " IS NULL"
				continue
			}
			hash := *snap.Fingerprints[i]
			if len(hash) != 64 {
				return "", connections.ErrInvalid
			}
			if _, err := hex.DecodeString(hash); err != nil {
				return "", connections.ErrInvalid
			}
			where += " AND " + fingerprint(c) + "=" + textLiteral(strings.ToLower(hash))
		case "values":
			v := snap.Original[i]
			if v.Kind == contract.Null {
				where += " AND " + quote(c.Name) + " IS NULL"
				continue
			}
			literal, err := cellLiteral(c, v)
			if err != nil {
				return "", err
			}
			where += " AND " + fingerprint(c) + "=" + literalFingerprint(c, literal)
		default:
			return "", connections.ErrInvalid
		}
	}
	return where, nil
}

type transactionReceipt struct {
	Begin, Commit, Rollback agentbridge.Receipt
	Statements              []agentbridge.Receipt
}

func controlOK(r agentbridge.Receipt) bool {
	var result struct {
		OK bool `json:"ok"`
	}
	return r.Dispatched && r.ExecutorAcknowledged && r.RemoteError == nil && json.Unmarshal(r.Result, &result) == nil && result.OK
}

func commitAcknowledged(r agentbridge.Receipt, err error) error {
	// An original successful COMMIT response is authoritative even when the
	// caller canceled while that response was in flight. A cancellation or
	// process-exit acknowledgement alone cannot establish the commit result.
	if controlOK(r) {
		return nil
	}
	return errors.Join(contract.ErrDataCommitUncertain, safeError(err))
}

func (s *Session) save(ctx context.Context, id int64, schema, name string, edits []contract.DataEdit) (out *contract.DataEditResult, err error) {
	out = &contract.DataEditResult{RefreshRequired: true}
	if len(edits) == 0 || len(edits) > 500 {
		return out, contract.ErrDataEditLimit
	}
	err = s.withFeature(ctx, func(ctx context.Context, session *agentbridge.Session) (err error) {
		t, err := s.resolve(ctx, session, id, schema, name, contract.ObjectRef{})
		if err != nil {
			return err
		}
		if !t.Editable {
			return contract.ErrDataReadOnly
		}
		var statements []string
		budget := 0
		for _, edit := range edits {
			statement, err := s.editStatement(t, edit)
			if err != nil {
				return err
			}
			budget += len(statement)
			if budget > 8<<20 {
				return contract.ErrDataEditLimit
			}
			statements = append(statements, statement)
		}
		var native transactionReceipt
		begun, commitAttempted := false, false
		defer func() {
			if begun && !out.CommitConfirmed && !commitAttempted {
				rollbackCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				var rollbackErr error
				native.Rollback, rollbackErr = session.Call(rollbackCtx, "rollback_manual_transaction", map[string]any{})
				cancel()
				if rollbackErr == nil && controlOK(native.Rollback) {
					out.Applied, out.Outcomes = 0, nil
				} else {
					out.CommitUncertain = true
					err = errors.Join(contract.ErrDataCommitUncertain, err, safeError(rollbackErr))
				}
			}
			data, _ := json.Marshal(native)
			out.NativeJSON = string(data)
		}()
		native.Begin, err = session.Call(ctx, "begin_manual_transaction", map[string]string{"schema": schema})
		if err != nil || !controlOK(native.Begin) {
			if err == nil {
				err = contract.ErrUnsupported
			}
			return err
		}
		begun = true
		for i, statement := range statements {
			r, callErr := session.Call(ctx, "execute_query", map[string]any{"sql": statement, "schema": schema, "maxRows": 1, "timeoutSecs": s.profile.H2Options.QueryTimeoutSeconds})
			native.Statements = append(native.Statements, r)
			if callErr != nil {
				return callErr
			}
			q, decodeErr := agentbridge.DecodeQuery(r, decodeValue)
			if decodeErr != nil {
				return decodeErr
			}
			if q.SQL.AffectedRows != 1 || q.SQL.Truncated {
				return contract.ErrDataConflict
			}
			out.Applied++
			out.Outcomes = append(out.Outcomes, contract.DataEditOutcome{Kind: edits[i].Kind, Original: edits[i].Original})
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		commitAttempted = true
		native.Commit, err = session.Call(ctx, "commit_manual_transaction", map[string]any{})
		if err = commitAcknowledged(native.Commit, err); err != nil {
			out.CommitUncertain = true
			return err
		}
		out.CommitConfirmed = true
		return nil
	})
	return
}

func (s *Session) editStatement(t *contract.TableStructure, edit contract.DataEdit) (string, error) {
	if edit.GraphEndpoints != nil || edit.Kind != "insert" && edit.Kind != "update" && edit.Kind != "delete" || edit.Kind == "insert" && edit.Original != nil || edit.Kind == "delete" && len(edit.Values) > 0 {
		return "", connections.ErrInvalid
	}
	for name := range edit.Values {
		found := false
		for _, c := range t.Columns {
			found = found || c.Name == name
		}
		if !found {
			return "", connections.ErrInvalid
		}
	}
	var names, values, assignments []string
	budget := 0
	for _, c := range t.Columns {
		v, ok := edit.Values[c.Name]
		if !ok {
			continue
		}
		if c.Generated {
			return "", contract.ErrUnsupported
		}
		budget += len(v.Text) + len(v.Bytes)
		if budget > contract.DataEditMaxBytes {
			return "", contract.ErrDataEditLimit
		}
		literal := "DEFAULT"
		if !v.Default {
			var err error
			literal, err = valueLiteral(c, v)
			if err != nil {
				return "", err
			}
		}
		names = append(names, quote(c.Name))
		values = append(values, literal)
		assignments = append(assignments, quote(c.Name)+"="+literal)
	}
	if edit.Kind == "insert" {
		if len(names) == 0 {
			return "INSERT INTO " + qualified(t.Object) + " DEFAULT VALUES", nil
		}
		return "INSERT INTO " + qualified(t.Object) + "(" + strings.Join(names, ",") + ") VALUES(" + strings.Join(values, ",") + ")", nil
	}
	where, err := s.originalWhere(t, edit.Original)
	if err != nil {
		return "", err
	}
	if edit.Kind == "delete" {
		return "DELETE FROM " + qualified(t.Object) + " WHERE " + where, nil
	}
	if len(assignments) == 0 {
		return "", connections.ErrInvalid
	}
	return "UPDATE " + qualified(t.Object) + " SET " + strings.Join(assignments, ",") + " WHERE " + where, nil
}

func (s *Session) verify(ctx context.Context, id int64, schema, name string, result *contract.DataEditResult) (*contract.DataVerification, error) {
	if result == nil {
		return nil, connections.ErrInvalid
	}
	if _, err := s.tableMetadata(ctx, id, schema, name); err != nil {
		return nil, err
	}
	// A later read cannot prove the original transaction or its triggers did
	// not commit. It never grants replay authority for an unknown receipt.
	return &contract.DataVerification{Status: "indeterminate"}, nil
}

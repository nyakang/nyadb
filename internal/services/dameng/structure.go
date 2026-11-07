package dameng

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) structure(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	var typ string
	err := s.db.QueryRowContext(ctx, `SELECT OBJECT_TYPE FROM SYS.ALL_OBJECTS WHERE OWNER=? AND OBJECT_NAME=? AND SUBOBJECT_NAME IS NULL AND OBJECT_TYPE IN ('TABLE','VIEW')`, o.Schema, o.Name).Scan(&typ)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, connections.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if strings.ToLower(typ) != o.Kind {
		return nil, connections.ErrInvalid
	}
	t := &contract.TableStructure{Object: o, Database: o.Schema, Name: o.Name, Kind: objectLabels[typ], Engine: "Dameng"}
	var comment sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT COMMENTS FROM SYS.ALL_TAB_COMMENTS WHERE OWNER=? AND TABLE_NAME=?`, o.Schema, o.Name).Scan(&comment)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	t.Comment = comment.String
	t.Definition, err = s.definition(ctx, o)
	if err != nil {
		return nil, err
	}
	var declared map[string]sqltext.DamengDDLColumn
	if o.Kind == "table" {
		declared, err = sqltext.DamengDDLColumns(t.Definition)
		if err != nil {
			return nil, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT C.COLUMN_NAME,C.DATA_TYPE,C.COLUMN_ID,C.NULLABLE,C.DATA_DEFAULT,C.DATA_LENGTH,C.DATA_PRECISION,C.DATA_SCALE,C.CHAR_LENGTH,M.COMMENTS FROM SYS.ALL_TAB_COLS C LEFT JOIN SYS.ALL_COL_COMMENTS M ON M.SCHEMA_NAME=C.OWNER AND M.TABLE_NAME=C.TABLE_NAME AND M.COLUMN_NAME=C.COLUMN_NAME WHERE C.OWNER=? AND C.TABLE_NAME=? AND C.HIDDEN_COLUMN='NO' ORDER BY C.COLUMN_ID`, o.Schema, o.Name)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c contract.StructureColumn
		var nullable string
		var def, comment sql.NullString
		var length, precision, scale, chars sql.NullInt64
		if err = rows.Scan(&c.Name, &c.DataType, &c.Position, &nullable, &def, &length, &precision, &scale, &chars, &comment); err != nil {
			break
		}
		if !validName(c.Name) || c.Position != int64(len(t.Columns)) || len(t.Columns) >= 4096 || nullable != "Y" && nullable != "N" {
			err = contract.ErrUnsupported
			break
		}
		c.Position++ // Native DM8 catalog positions are zero-based.
		c.Nullable, c.Comment, c.ValueKind = nullable == "Y", comment.String, typeKind(c.DataType)
		c.Type = c.DataType
		if precision.Valid {
			v := precision.Int64
			c.NumericPrecision = &v
		}
		if scale.Valid && c.ValueKind == contract.Decimal {
			v := scale.Int64
			c.NumericScale = &v
		}
		if chars.Valid && (c.DataType == "CHAR" || c.DataType == "VARCHAR") {
			v := chars.Int64
			c.CharacterMaxLength = &v
			c.Type += fmt.Sprintf("(%d)", v)
		}
		if c.ValueKind == contract.Decimal && precision.Valid && scale.Valid {
			c.Type += fmt.Sprintf("(%d,%d)", precision.Int64, scale.Int64)
		}
		if c.DataType == "BINARY" || c.DataType == "VARBINARY" {
			c.Type += fmt.Sprintf("(%d)", length.Int64)
		}
		if c.DataType == "TIMESTAMP" || c.DataType == "TIME" || strings.Contains(c.DataType, "TIME ZONE") {
			if scale.Valid {
				v := scale.Int64
				c.DatetimePrecision = &v
			}
		}
		if def.Valid {
			v := def.String
			c.Default = &v
		}
		if o.Kind == "table" {
			d, ok := declared[c.Name]
			if !ok {
				err = contract.ErrUnsupported
				break
			}
			c.Type, c.Generated = d.Type, d.Generated
			if d.Identity {
				c.Extra = "IDENTITY"
			} else if d.Generated {
				c.Extra = "GENERATED"
			}
		}
		t.Columns = append(t.Columns, c)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	if len(t.Columns) == 0 || o.Kind == "table" && len(declared) != len(t.Columns) {
		return nil, contract.ErrUnsupported
	}
	rows, err = s.db.QueryContext(ctx, `SELECT I.INDEX_NAME,I.INDEX_TYPE,I.UNIQUENESS,IC.COLUMN_NAME,IC.COLUMN_POSITION,IC.DESCEND,CASE WHEN PC.CONSTRAINT_TYPE='P' AND PC.STATUS='ENABLED' AND PC.VALIDATED='VALIDATED' AND PC.DEFERRABLE='NOT DEFERRABLE' THEN 1 ELSE 0 END FROM SYS.ALL_INDEXES I JOIN SYS.ALL_IND_COLUMNS IC ON IC.INDEX_OWNER=I.OWNER AND IC.INDEX_NAME=I.INDEX_NAME LEFT JOIN SYS.ALL_CONSTRAINTS PC ON PC.INDEX_OWNER=I.OWNER AND PC.INDEX_NAME=I.INDEX_NAME AND PC.CONSTRAINT_TYPE='P' WHERE I.TABLE_OWNER=? AND I.TABLE_NAME=? ORDER BY I.INDEX_NAME,IC.COLUMN_POSITION`, o.Schema, o.Name)
	if err != nil {
		return nil, err
	}
	indexes := make(map[string]int)
	for rows.Next() {
		var name, kind, unique, col, direction string
		var position, primary int64
		if err = rows.Scan(&name, &kind, &unique, &col, &position, &direction, &primary); err != nil {
			break
		}
		i, ok := indexes[name]
		if !ok {
			i = len(t.Indexes)
			indexes[name] = i
			t.Indexes = append(t.Indexes, contract.StructureIndex{Name: name, Type: kind, Unique: unique == "UNIQUE"})
		}
		t.Indexes[i].Columns = append(t.Indexes[i].Columns, contract.StructureIndexColumn{Name: col, Position: position, Direction: direction})
		if primary == 1 {
			for j := range t.Columns {
				if t.Columns[j].Name == col {
					t.Columns[j].Key = "PRI"
				}
			}
		}
		if len(t.Indexes) > 4096 || len(t.Indexes[i].Columns) > len(t.Columns) {
			err = contract.ErrUnsupported
			break
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT F.CONSTRAINT_NAME,R.OWNER,R.TABLE_NAME,F.DELETE_RULE,FC.COLUMN_NAME,RC.COLUMN_NAME,FC.POSITION FROM SYS.ALL_CONSTRAINTS F JOIN SYS.ALL_CONSTRAINTS R ON R.OWNER=F.R_OWNER AND R.CONSTRAINT_NAME=F.R_CONSTRAINT_NAME JOIN SYS.ALL_CONS_COLUMNS FC ON FC.OWNER=F.OWNER AND FC.CONSTRAINT_NAME=F.CONSTRAINT_NAME JOIN SYS.ALL_CONS_COLUMNS RC ON RC.OWNER=R.OWNER AND RC.CONSTRAINT_NAME=R.CONSTRAINT_NAME AND RC.POSITION=FC.POSITION WHERE F.OWNER=? AND F.TABLE_NAME=? AND F.CONSTRAINT_TYPE='R' ORDER BY F.CONSTRAINT_NAME,FC.POSITION`, o.Schema, o.Name)
	if err != nil {
		return nil, err
	}
	fks := make(map[string]int)
	for rows.Next() {
		var f contract.StructureForeignKey
		var c contract.StructureForeignKeyColumn
		if err = rows.Scan(&f.Name, &f.ReferencedDatabase, &f.ReferencedTable, &f.OnDelete, &c.Name, &c.ReferencedName, &c.Position); err != nil {
			break
		}
		f.OnUpdate = "NO ACTION"
		i, ok := fks[f.Name]
		if !ok {
			i = len(t.ForeignKeys)
			fks[f.Name] = i
			t.ForeignKeys = append(t.ForeignKeys, f)
		}
		t.ForeignKeys[i].Columns = append(t.ForeignKeys[i].Columns, c)
		if len(t.ForeignKeys) > 4096 || len(t.ForeignKeys[i].Columns) > len(t.Columns) {
			err = contract.ErrUnsupported
			break
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close(), ctx.Err())
	if err != nil {
		return nil, err
	}
	return t, nil // Data editing is enabled only by the subsequent native data adapter.
}

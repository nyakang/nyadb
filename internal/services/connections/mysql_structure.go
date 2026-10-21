package connections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrMySQLTableNotFound is returned when the requested table/view is not visible
// in information_schema. Lack of metadata privileges may have the same effect.
var ErrMySQLTableNotFound = errors.New("MySQL table or view not found")

// MySQLTableStructure contains only read-only server metadata, never row data.
type MySQLTableStructure struct {
	Database, Name, Kind, Engine, Comment, Collation string
	EstimatedRows                                    *int64 // TABLE_ROWS is approximate for InnoDB and NULL for some views.
	Columns                                          []MySQLStructureColumn
	Indexes                                          []MySQLStructureIndex
	ForeignKeys                                      []MySQLStructureForeignKey
}

type MySQLStructureColumn struct {
	Name, Type, DataType, Key, Extra, Comment          string
	Position                                           int64
	Nullable                                           bool
	Default                                            *string // nil distinguishes SQL NULL from empty string.
	CharacterMaxLength, NumericPrecision, NumericScale *int64
}

type MySQLStructureIndexColumn struct {
	Name, Direction string
	Position        int64
	SubPart         *int64
}

type MySQLStructureIndex struct {
	Name, Type string
	Unique     bool
	Columns    []MySQLStructureIndexColumn
}

type MySQLStructureForeignKeyColumn struct {
	Name, ReferencedName string
	Position             int64
}

type MySQLStructureForeignKey struct {
	Name, ReferencedDatabase, ReferencedTable, OnUpdate, OnDelete string
	Columns                                                       []MySQLStructureForeignKeyColumn
}

func validMySQLMetadataName(name string) bool {
	return name != "" && len(name) <= 256 && !strings.ContainsRune(name, 0)
}

func mysqlNullableInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}
func mysqlNullableString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	n := v.String
	return &n
}

// TableStructure reads INFORMATION_SCHEMA on an already authenticated, managed
// session. Every schema/table value is a bound predicate, never SQL text.
// Disconnect, Close, caller cancellation and the per-request timeout all
// interrupt in-flight queries. It does not issue USE, SHOW CREATE, DDL or DML.
func (m *MySQLSessionManager) TableStructure(ctx context.Context, id int64, database, table string) (*MySQLTableStructure, error) {
	if !validMySQLMetadataName(database) || !validMySQLMetadataName(table) {
		return nil, fmt.Errorf("%w: invalid database or table name", ErrInvalid)
	}
	m.mu.Lock()
	session := m.sessions[id]
	m.mu.Unlock()
	if session == nil {
		return nil, ErrMySQLNotConnected
	}

	queryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	stop := context.AfterFunc(session.ctx, cancel)
	defer func() { stop(); cancel() }()

	result := &MySQLTableStructure{Database: database, Name: table}
	var engine, comment, collation sql.NullString
	var rowsEstimate sql.NullInt64
	const tableSQL = `SELECT TABLE_TYPE, ENGINE, TABLE_COMMENT, TABLE_COLLATION, TABLE_ROWS
FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
AND TABLE_TYPE IN ('BASE TABLE', 'VIEW')`
	err := session.db.QueryRowContext(queryCtx, tableSQL, database, table).
		Scan(&result.Kind, &engine, &comment, &collation, &rowsEstimate)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMySQLTableNotFound
	}
	if err != nil {
		return nil, err
	}
	result.Engine, result.Comment, result.Collation = engine.String, comment.String, collation.String
	result.EstimatedRows = mysqlNullableInt(rowsEstimate)

	if err := readMySQLStructureColumns(queryCtx, session.db, database, table, result); err != nil {
		return nil, err
	}
	if err := readMySQLStructureIndexes(queryCtx, session.db, database, table, result); err != nil {
		return nil, err
	}
	if err := readMySQLStructureForeignKeys(queryCtx, session.db, database, table, result); err != nil {
		return nil, err
	}
	if err := queryCtx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func readMySQLStructureColumns(ctx context.Context, db *sql.DB, database, table string, target *MySQLTableStructure) error {
	const query = `SELECT COLUMN_NAME, COLUMN_TYPE, DATA_TYPE, ORDINAL_POSITION, IS_NULLABLE,
COLUMN_DEFAULT, COLUMN_KEY, EXTRA, COALESCE(COLUMN_COMMENT, ''),
CHARACTER_MAXIMUM_LENGTH, NUMERIC_PRECISION, NUMERIC_SCALE
FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
ORDER BY ORDINAL_POSITION`
	rows, err := db.QueryContext(ctx, query, database, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if len(target.Columns) >= maxMySQLMetadataObjects {
			return ErrMySQLMetadataLimit
		}
		var c MySQLStructureColumn
		var nullable string
		var def sql.NullString
		var charLen, precision, scale sql.NullInt64
		if err := rows.Scan(&c.Name, &c.Type, &c.DataType, &c.Position, &nullable,
			&def, &c.Key, &c.Extra, &c.Comment, &charLen, &precision, &scale); err != nil {
			return err
		}
		c.Nullable = nullable == "YES"
		c.Default = mysqlNullableString(def)
		c.CharacterMaxLength = mysqlNullableInt(charLen)
		c.NumericPrecision = mysqlNullableInt(precision)
		c.NumericScale = mysqlNullableInt(scale)
		target.Columns = append(target.Columns, c)
	}
	return rows.Err()
}

func readMySQLStructureIndexes(ctx context.Context, db *sql.DB, database, table string, target *MySQLTableStructure) error {
	const query = `SELECT INDEX_NAME, NON_UNIQUE, INDEX_TYPE, SEQ_IN_INDEX, COLUMN_NAME, SUB_PART, COLLATION
FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
ORDER BY INDEX_NAME, SEQ_IN_INDEX`
	rows, err := db.QueryContext(ctx, query, database, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	indexByName := make(map[string]int)
	count := 0
	for rows.Next() {
		if count >= maxMySQLMetadataObjects {
			return ErrMySQLMetadataLimit
		}
		var name, kind string
		var nonUnique int64
		var column, direction sql.NullString
		var seq int64
		var sub sql.NullInt64
		if err := rows.Scan(&name, &nonUnique, &kind, &seq, &column, &sub, &direction); err != nil {
			return err
		}
		i, ok := indexByName[name]
		if !ok {
			i = len(target.Indexes)
			indexByName[name] = i
			target.Indexes = append(target.Indexes, MySQLStructureIndex{Name: name, Unique: nonUnique == 0, Type: kind})
		}
		target.Indexes[i].Columns = append(target.Indexes[i].Columns,
			MySQLStructureIndexColumn{Name: column.String, Position: seq, SubPart: mysqlNullableInt(sub), Direction: direction.String})
		count++
	}
	return rows.Err()
}

func readMySQLStructureForeignKeys(ctx context.Context, db *sql.DB, database, table string, target *MySQLTableStructure) error {
	const query = `SELECT k.CONSTRAINT_NAME, k.ORDINAL_POSITION, k.COLUMN_NAME,
k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME,
r.UPDATE_RULE, r.DELETE_RULE
FROM information_schema.KEY_COLUMN_USAGE AS k
JOIN information_schema.REFERENTIAL_CONSTRAINTS AS r
ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA
AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME AND r.TABLE_NAME = k.TABLE_NAME
WHERE k.TABLE_SCHEMA = ? AND k.TABLE_NAME = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL
ORDER BY k.CONSTRAINT_NAME, k.ORDINAL_POSITION`
	rows, err := db.QueryContext(ctx, query, database, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	keyByName := make(map[string]int)
	count := 0
	for rows.Next() {
		if count >= maxMySQLMetadataObjects {
			return ErrMySQLMetadataLimit
		}
		var name, column, refSchema, refTable, refColumn, update, del string
		var position int64
		if err := rows.Scan(&name, &position, &column, &refSchema, &refTable, &refColumn, &update, &del); err != nil {
			return err
		}
		i, ok := keyByName[name]
		if !ok {
			i = len(target.ForeignKeys)
			keyByName[name] = i
			target.ForeignKeys = append(target.ForeignKeys, MySQLStructureForeignKey{
				Name: name, ReferencedDatabase: refSchema, ReferencedTable: refTable, OnUpdate: update, OnDelete: del,
			})
		}
		target.ForeignKeys[i].Columns = append(target.ForeignKeys[i].Columns,
			MySQLStructureForeignKeyColumn{Name: column, ReferencedName: refColumn, Position: position})
		count++
	}
	return rows.Err()
}

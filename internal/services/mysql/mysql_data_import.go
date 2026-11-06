package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/mysqlspec"
	"strings"
	"time"
	"unicode/utf8"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// MySQLDataImportColumn is a reviewed new-table field definition. Type accepts
// only the supported ordinary types, never expressions, defaults or raw SQL.
type MySQLDataImportColumn = contract.DataImportColumn

// CREATE TABLE is not transactional in MySQL. A confirmed new table remains
// available for inspection after a rolled-back row batch. An acknowledgement
// failure is reported separately and must not lead to automatic replay/drop.
type MySQLDataNewTableImportResult = contract.DataNewTableImportResult

func mysqlDataImportInvalid(reason string) error { return mysqlspec.ImportInvalid(reason) }

func mysqlDataImportType(col MySQLDataImportColumn, position int) (MySQLStructureColumn, error) {
	return mysqlspec.ImportColumnType(col, position)
}

// MySQLDataImportColumnMetadata presents the exact validated schema to mapping
// and value validation without modifying the caller's reviewed definitions.
func MySQLDataImportColumnMetadata(columns []MySQLDataImportColumn) ([]MySQLStructureColumn, error) {
	return mysqlspec.ImportColumnMetadata(columns)
}

func ValidateMySQLDataImportColumns(columns []MySQLDataImportColumn) error {
	return mysqlspec.ValidateImportColumns(columns)
}

// ValidateMySQLDataNewTableImport shares the executor's whole-batch preflight
// with the import review. It does not connect or create the destination table.
func ValidateMySQLDataNewTableImport(columns []MySQLDataImportColumn, edits []MySQLDataEdit) error {
	return mysqlspec.ValidateNewTableImport(columns, edits)
}

func mysqlDataImportCreateSQL(database, table string, columns []MySQLDataImportColumn, metadata []MySQLStructureColumn) (string, error) {
	target, err := mysqlEditTable(database, table)
	if err != nil {
		return "", err
	}
	if utf8.RuneCountInString(database) > 64 || utf8.RuneCountInString(table) > 64 || strings.HasSuffix(database, " ") || strings.HasSuffix(table, " ") || !utf8.ValidString(database) || !utf8.ValidString(table) {
		return "", mysqlDataImportInvalid("database or table name")
	}
	definitions := make([]string, 0, len(columns)+1)
	var primary []string
	for i, col := range columns {
		name, _ := quotedMySQLIdentifier(col.Name)
		definition := name + " " + metadata[i].Type
		if col.Nullable {
			definition += " NULL"
		} else {
			definition += " NOT NULL"
		}
		definitions = append(definitions, definition)
		if col.PrimaryKey {
			primary = append(primary, name)
		}
	}
	if len(primary) > 0 {
		definitions = append(definitions, "PRIMARY KEY ("+strings.Join(primary, ",")+")")
	}
	return "CREATE TABLE " + target + " (" + strings.Join(definitions, ",") + ") ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci", nil
}

func mysqlDataImportDecimalFits(text string, precision, scale int64) bool {
	return mysqlspec.DecimalFits(text, precision, scale)
}

func mysqlDataImportValueValid(col MySQLStructureColumn, value MySQLDataValue) bool {
	return mysqlspec.ImportValueValid(col, value)
}

func mysqlDataImportValidatedEdits(metadata []MySQLStructureColumn, edits []MySQLDataEdit) ([]MySQLDataEdit, error) {
	return mysqlspec.ValidatedImportEdits(metadata, edits)
}

// ImportMySQLDataIntoNewTable creates only the explicitly reviewed destination.
// It pins the original managed metadata connection across DDL and the row
// transaction. It neither replaces an existing table nor replays an operation
// after reconnection. The caller must await the driver's cancellation reply.
func (m *MySQLSessionManager) ImportMySQLDataIntoNewTable(ctx context.Context, id int64, database, table string, columns []MySQLDataImportColumn, edits []MySQLDataEdit) (*MySQLDataNewTableImportResult, error) {
	result := &MySQLDataNewTableImportResult{}
	metadata, err := MySQLDataImportColumnMetadata(columns)
	if err != nil {
		return result, err
	}
	createSQL, err := mysqlDataImportCreateSQL(database, table, columns, metadata)
	if err != nil {
		return result, err
	}
	ownedEdits, err := mysqlDataImportValidatedEdits(metadata, edits)
	if err != nil {
		return result, err
	}
	session, qctx, cancel, err := m.requestContext(ctx, id, 45*time.Second)
	if err != nil {
		return result, err
	}
	defer cancel()
	if err = qctx.Err(); err != nil {
		return result, err
	}
	conn, err := session.db.Conn(qctx)
	if err != nil {
		return result, err
	}
	defer conn.Close()
	if err = mysqlPrepareWrite(qctx, conn); err != nil {
		return result, err
	}
	var originalMode string
	if err = conn.QueryRowContext(qctx, "SELECT @@SESSION.sql_mode").Scan(&originalMode); err != nil {
		return result, err
	}
	if _, err = conn.ExecContext(qctx, "SET SESSION sql_mode = ?", originalMode+",STRICT_ALL_TABLES"); err != nil {
		return result, err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if _, restoreErr := conn.ExecContext(cleanup, "SET SESSION sql_mode = ?", originalMode); restoreErr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if err = qctx.Err(); err != nil {
		return result, err
	}
	if _, err = conn.ExecContext(qctx, createSQL); err != nil {
		var serverError *mysqldriver.MySQLError
		result.CreationUncertain = !errors.As(err, &serverError)
		return result, err
	}
	result.Created = true
	tx, err := conn.BeginTx(qctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	target, _ := mysqlEditTable(database, table)
	result.Saved, err = applyMySQLDataEditsInTransaction(qctx, tx, database, table, target, ownedEdits)
	if err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		result.Saved.CommitUncertain = true
		return result, fmt.Errorf("%w: %v", ErrMySQLDataCommitUncertain, err)
	}
	result.Saved.CommitConfirmed = true
	return result, nil
}

package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Data documents own physical transactions independently of metadata reads and
// SQL-tab leases. Tokens never reconnect or replay a write after state loss.
var ErrMySQLDataTransactionLost = errors.New("MySQL data transaction lost; refresh the table before starting another transaction")

type MySQLDataTransaction struct {
	mu              sync.Mutex
	session         *mysqlManagedSession
	profile         int64
	database, table string
	ctx             context.Context
	cancel          context.CancelFunc
	txCancel        context.CancelFunc
	stop            func() bool
	conn            *sql.Conn
	tx              *sql.Tx
	closed          bool
}

// BeginMySQLDataTransaction reserves a dedicated connection for one qualified
// table. Its lifetime is the session's lifetime, not the begin request's deadline.
func (m *MySQLSessionManager) BeginMySQLDataTransaction(ctx context.Context, profile int64, database, table string) (*MySQLDataTransaction, error) {
	target, err := mysqlEditTable(database, table)
	if err != nil {
		return nil, err
	}
	session, initCtx, stopInit, err := m.requestContext(ctx, profile, 30*time.Second)
	if err != nil {
		return nil, err
	}
	defer stopInit()
	leaseCtx, cancel := context.WithCancel(session.ctx)
	// Request cancellation revokes the lease and cancels its current query.
	// Keep database/sql's transaction context alive until an explicit rollback
	// acknowledges completion; its automatic rollback marks Tx done early.
	txCtx, txCancel := context.WithCancel(context.Background())
	t := &MySQLDataTransaction{session: session, profile: profile, database: database, table: table, ctx: leaseCtx, cancel: cancel, txCancel: txCancel}
	stopCaller := context.AfterFunc(initCtx, func() { cancel(); txCancel() })
	defer stopCaller()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.conn, err = session.db.Conn(initCtx)
	if err == nil {
		var mode string
		err = t.conn.QueryRowContext(initCtx, "SELECT @@SESSION.sql_mode").Scan(&mode)
		if err == nil {
			_, err = t.conn.ExecContext(initCtx, "SET SESSION sql_mode = ?", mode+",STRICT_ALL_TABLES")
		}
	}
	if err == nil {
		t.tx, err = t.conn.BeginTx(txCtx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	}
	if err == nil {
		var rows *sql.Rows
		rows, err = t.tx.QueryContext(initCtx, "SELECT 1 FROM "+target+" LIMIT 0")
		if err == nil {
			err = rows.Close()
		}
	}
	if err == nil {
		var engine, kind string
		err = t.tx.QueryRowContext(initCtx, `SELECT COALESCE(ENGINE,''),TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?`, database, table).Scan(&engine, &kind)
		if err == nil && (!strings.EqualFold(engine, "InnoDB") || kind != "BASE TABLE") {
			err = ErrMySQLDataReadOnly
		}
	}
	if err == nil {
		err = initCtx.Err()
	}
	if err != nil {
		t.closeLocked()
		return nil, err
	}
	t.stop = context.AfterFunc(leaseCtx, func() { t.Close() })
	return t, nil
}

func (t *MySQLDataTransaction) closeLocked() {
	if t.closed {
		return
	}
	t.closed = true
	if t.stop != nil {
		t.stop()
		t.stop = nil
	}
	t.cancel()
	if t.tx != nil {
		_ = t.tx.Rollback()
		t.tx = nil
	}
	if t.txCancel != nil {
		t.txCancel()
	}
	if t.conn != nil {
		// Never return strict-mode or uncertain transactional session state to
		// the metadata pool. The driver closes the physical connection.
		_ = t.conn.Raw(func(any) error { return driver.ErrBadConn })
		_ = t.conn.Close()
		t.conn = nil
	}
}

// Close revokes the token immediately; cleanup waits for driver acknowledgement.
// UI callers run this in a worker so a canceled query cannot block rendering.
func (t *MySQLDataTransaction) Close() {
	if t == nil {
		return
	}
	t.cancel()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closeLocked()
}

func (m *MySQLSessionManager) dataTransactionContext(ctx context.Context, t *MySQLDataTransaction, profile int64, database, table string) (context.Context, context.CancelFunc, error) {
	if t == nil || t.closed || t.tx == nil || t.ctx.Err() != nil || profile != t.profile || database != t.database || table != t.table {
		return nil, nil, ErrMySQLDataTransactionLost
	}
	m.mu.Lock()
	current := m.sessions[profile]
	m.mu.Unlock()
	if current != t.session {
		return nil, nil, ErrMySQLDataTransactionLost
	}
	qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	stop := context.AfterFunc(t.ctx, cancel)
	return qctx, func() { stop(); cancel() }, nil
}

func (t *MySQLDataTransaction) settleRequest(ctx context.Context, err error) error {
	if ctx.Err() != nil || mysqlSQLSessionBroken(err) {
		t.closeLocked()
		return errors.Join(ErrMySQLDataTransactionLost, err, ctx.Err())
	}
	return err
}

func (m *MySQLSessionManager) TableDataPageInTransaction(ctx context.Context, t *MySQLDataTransaction, profile int64, database, table string, options MySQLDataOptions) (*MySQLDataPage, error) {
	if t == nil {
		return nil, ErrMySQLDataTransactionLost
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	qctx, cancel, err := m.dataTransactionContext(ctx, t, profile, database, table)
	if err != nil {
		return nil, err
	}
	defer cancel()
	page, err := mysqlDataPageWithOptions(qctx, t.tx, database, table, options)
	return page, t.settleRequest(qctx, err)
}

func (m *MySQLSessionManager) ReadMySQLDataRowInTransaction(ctx context.Context, t *MySQLDataTransaction, profile int64, database, table string, ref MySQLRowRef) (*MySQLDataRow, error) {
	if t == nil {
		return nil, ErrMySQLDataTransactionLost
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	qctx, cancel, err := m.dataTransactionContext(ctx, t, profile, database, table)
	if err != nil {
		return nil, err
	}
	defer cancel()
	target, err := mysqlEditTable(database, table)
	if err != nil {
		return nil, err
	}
	columns, err := readMySQLColumnMetadata(qctx, t.tx, database, table, 128)
	if err == nil {
		err = mysqlEditValidateRef(qctx, t.tx, database, table, ref)
	}
	if err != nil {
		return nil, t.settleRequest(qctx, err)
	}
	row, err := readMySQLDataRowInTransaction(qctx, t.tx, database, table, target, ref, columns)
	return row, t.settleRequest(qctx, err)
}

// ApplyMySQLDataEditsInTransaction saves an atomic batch without committing the
// document transaction. A failed batch rolls back to its savepoint; earlier
// successful batches remain pending and visible through this token.
func (m *MySQLSessionManager) ApplyMySQLDataEditsInTransaction(ctx context.Context, t *MySQLDataTransaction, profile int64, database, table string, edits []MySQLDataEdit) (*MySQLDataEditResult, error) {
	if len(edits) == 0 || len(edits) > MySQLDataMaxPageSize {
		return nil, ErrMySQLDataPage
	}
	if t == nil {
		return nil, ErrMySQLDataTransactionLost
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	qctx, cancel, err := m.dataTransactionContext(ctx, t, profile, database, table)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if _, err = t.tx.ExecContext(qctx, "SAVEPOINT nyadb_data_batch"); err != nil {
		return nil, t.settleRequest(qctx, err)
	}
	target, _ := mysqlEditTable(database, table)
	result, err := applyMySQLDataEditsInTransaction(qctx, t.tx, database, table, target, edits)
	if err != nil {
		cleanup, stop := context.WithTimeout(t.ctx, 2*time.Second)
		_, rollbackErr := t.tx.ExecContext(cleanup, "ROLLBACK TO SAVEPOINT nyadb_data_batch")
		if rollbackErr == nil {
			_, rollbackErr = t.tx.ExecContext(cleanup, "RELEASE SAVEPOINT nyadb_data_batch")
		}
		stop()
		if rollbackErr != nil {
			t.closeLocked()
			return nil, errors.Join(ErrMySQLDataTransactionLost, err, rollbackErr)
		}
		return nil, t.settleRequest(qctx, err)
	}
	_, err = t.tx.ExecContext(qctx, "RELEASE SAVEPOINT nyadb_data_batch")
	if err != nil {
		t.closeLocked()
		return nil, errors.Join(ErrMySQLDataTransactionLost, err)
	}
	return result, t.settleRequest(qctx, nil)
}

func (m *MySQLSessionManager) CommitMySQLDataTransaction(ctx context.Context, t *MySQLDataTransaction) error {
	if t == nil {
		return ErrMySQLDataTransactionLost
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	qctx, cancel, err := m.dataTransactionContext(ctx, t, t.profile, t.database, t.table)
	if err != nil {
		return err
	}
	defer cancel()
	if err = qctx.Err(); err != nil {
		return err
	}
	// database/sql Commit waits for the driver's reply. Once it begins, any
	// failed acknowledgement is uncertain, and this token is never reused.
	err = t.tx.Commit()
	t.tx = nil
	t.closeLocked()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMySQLDataCommitUncertain, err)
	}
	return nil
}

func (m *MySQLSessionManager) RollbackMySQLDataTransaction(ctx context.Context, t *MySQLDataTransaction) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := t.tx.Rollback()
	t.tx = nil
	t.closeLocked()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}

// ReadMySQLFieldChunkInTransaction keeps explicit inspector reads on the same
// original physical connection as pending table writes.
func (m *MySQLSessionManager) ReadMySQLFieldChunkInTransaction(ctx context.Context, t *MySQLDataTransaction, profile int64, database, table, column string, ref MySQLRowRef, offset int64, units int) (*MySQLFieldChunk, error) {
	if t == nil {
		return nil, ErrMySQLDataTransactionLost
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	qctx, cancel, err := m.dataTransactionContext(ctx, t, profile, database, table)
	if err != nil {
		return nil, err
	}
	defer cancel()
	chunk, err := readMySQLFieldChunk(qctx, t.tx, database, table, column, ref, offset, units)
	return chunk, t.settleRequest(qctx, err)
}

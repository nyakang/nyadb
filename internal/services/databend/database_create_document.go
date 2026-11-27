package databend

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type databaseCreateDocument struct {
	s               *Session
	r               contract.DatabaseCreateDocumentRequest
	mu              sync.Mutex
	closed, blocked bool
	ctx             context.Context
	cancel          context.CancelFunc
	active          sync.WaitGroup
	serial          chan struct{}
	done            chan struct{}
	key             [32]byte
	sequence        uint64
	original        *contract.DatabaseCreateOutcome
}

func (s *Session) CanCreateDatabase(t contract.DatabaseCreateTarget) bool {
	n := t.Namespace
	return t.Generation != 0 && t.Generation == s.Generation() && n.ProfileID == s.ProfileID() && n.Catalog == "" &&
		sqltext.ValidName(n.Schema) && n.Schema != "system" && n.Schema != "information_schema" && s.Alive()
}

func (s *Session) ReserveDatabaseCreateDocument(r contract.DatabaseCreateDocumentRequest) (contract.DatabaseCreateDocumentLease, error) {
	if !r.Valid() {
		return nil, connections.ErrInvalid
	}
	if !s.CanCreateDatabase(r.Target) {
		return nil, contract.ErrUnsupported
	}
	d := &databaseCreateDocument{s: s, r: r, serial: make(chan struct{}, 1), done: make(chan struct{})}
	if _, err := rand.Read(d.key[:]); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, contract.ErrRevoked
	}
	if s.databaseCreateDocuments[r.DocumentID] != nil {
		return nil, connections.ErrInvalid
	}
	if len(s.databaseCreateDocuments) >= 32 {
		return nil, contract.ErrSQLSessionLimit
	}
	if s.databaseCreateDocuments == nil {
		s.databaseCreateDocuments = make(map[string]*databaseCreateDocument)
	}
	d.ctx, d.cancel = context.WithCancel(s.ctx)
	s.databaseCreateDocuments[r.DocumentID] = d
	return d, nil
}

func (d *databaseCreateDocument) admission(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, connections.ErrInvalid
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, nil, contract.ErrRevoked
	}
	d.active.Add(1)
	d.mu.Unlock()
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(d.ctx, cancel)
	finish := func() { stop(); cancel(); d.active.Done() }
	select {
	case <-child.Done():
		finish()
		return nil, nil, child.Err()
	case d.serial <- struct{}{}:
	}
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed || child.Err() != nil || !d.s.CanCreateDatabase(d.r.Target) {
		<-d.serial
		finish()
		return nil, nil, contract.ErrRevoked
	}
	return child, func() { <-d.serial; finish() }, nil
}

func (d *databaseCreateDocument) seal(v contract.DatabaseCreateSnapshot) string {
	v.Revision = ""
	raw, _ := json.Marshal(v)
	mac := hmac.New(sha256.New, d.key[:])
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

func (d *databaseCreateDocument) read(ctx context.Context, original *ManagedSession) (contract.DatabaseCreateSnapshot, error) {
	out := contract.DatabaseCreateSnapshot{Target: d.r.Target, Engine: "databend"}
	name := d.r.Target.Namespace.Schema
	out.CreateSQL = "CREATE DATABASE " + quoteName(name)
	q, err := d.s.query(ctx, original, "SELECT to_string(database_id) AS __nyadb_database_id FROM system.databases WHERE catalog='default' AND name="+quoteText(name)+" AND dropped_on IS NULL", 2)
	if err != nil {
		return out, err
	}
	if q.Truncated || len(q.Sets) != 1 || len(q.Sets[0].Columns) != 1 || q.Sets[0].Columns[0].Name != "__nyadb_database_id" || len(q.Sets[0].Rows) > 1 {
		return out, contract.ErrUnsupported
	}
	if len(q.Sets[0].Rows) == 1 {
		row := q.Sets[0].Rows[0]
		if len(row) != 1 || row[0].Kind != contract.Text || row[0].Truncated {
			return out, contract.ErrUnsupported
		}
		id, err := strconv.ParseUint(row[0].Text, 10, 64)
		if err != nil || id == 0 {
			return out, contract.ErrUnsupported
		}
		out.Exists, out.NativeID = true, row[0].Text
		q, err = d.s.query(ctx, original, "SHOW CREATE DATABASE "+quoteName(name), 2)
		if err != nil {
			return out, err
		}
		if q.Truncated || len(q.Sets) != 1 || len(q.Sets[0].Columns) != 2 || q.Sets[0].Columns[0].Name != "Database" || q.Sets[0].Columns[1].Name != "Create Database" || len(q.Sets[0].Rows) != 1 || len(q.Sets[0].Rows[0]) != 2 {
			return out, contract.ErrUnsupported
		}
		row = q.Sets[0].Rows[0]
		if row[0].Kind != contract.Text || row[0].Truncated || row[0].Text != name || row[1].Kind != contract.Text || row[1].Truncated || len(row[1].Text) > 1<<20 {
			return out, contract.ErrUnsupported
		}
		out.DDL = row[1].Text
		// This native version interpolates the name verbatim inside backticks,
		// including embedded backticks. Preserve those exact observation bytes;
		// never reuse SHOW CREATE as executable SQL. Input uses quoteName instead.
		if out.DDL != "CREATE DATABASE `"+name+"` ENGINE=DEFAULT" {
			return out, contract.ErrUnsupported
		}
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if !d.s.CanCreateDatabase(d.r.Target) {
		return out, contract.ErrRevoked
	}
	out.Revision = d.seal(out)
	return out, nil
}

func (d *databaseCreateDocument) Snapshot(ctx context.Context) (contract.DatabaseCreateSnapshot, error) {
	child, done, err := d.admission(ctx)
	if err != nil {
		return contract.DatabaseCreateSnapshot{}, err
	}
	defer done()
	var out contract.DatabaseCreateSnapshot
	err = d.s.withMetadata(child, func(ctx context.Context, original *ManagedSession) error {
		out, err = d.read(ctx, original)
		return err
	})
	if child.Err() != nil {
		return contract.DatabaseCreateSnapshot{}, child.Err()
	}
	return out, err
}

func (d *databaseCreateDocument) Create(ctx context.Context, before contract.DatabaseCreateSnapshot) (out contract.DatabaseCreateOutcome) {
	out.State = "not_executed"
	child, done, err := d.admission(ctx)
	if err != nil {
		out.Error = err
		return
	}
	defer done()
	d.mu.Lock()
	blocked := d.blocked
	settled := d.original != nil && d.original.Verified
	d.mu.Unlock()
	if blocked {
		out.Error = contract.ErrDatabaseCreateUnresolved
		if settled {
			out.Error = contract.ErrDatabaseCreateConsumed
		}
		return
	}
	if before.Target != d.r.Target || before.Engine != "databend" || before.Exists || before.NativeID != "" || before.DDL != "" || !hmac.Equal([]byte(before.Revision), []byte(d.seal(before))) {
		out.Error = contract.ErrNamespaceChanged
		return
	}
	original, receipts, err := d.s.process.OpenSession(child, false)
	for _, receipt := range receipts {
		d.s.observed("database-create-open", receipt)
	}
	if err != nil {
		out.Error = err
		return
	}
	defer func() {
		receipt, err := original.Close()
		d.s.observed("database-create-close", receipt)
		if out.Receipt == nil {
			out.Error = errors.Join(out.Error, err)
		}
	}()
	current, err := d.read(child, original)
	if err != nil {
		out.Error = err
		return
	}
	if current != before {
		out.Error = contract.ErrNamespaceChanged
		return
	}
	if child.Err() != nil {
		out.Error = child.Err()
		return
	}
	if !d.s.CanCreateDatabase(d.r.Target) {
		out.Error = contract.ErrRevoked
		return
	}
	sql := before.CreateSQL
	r, callErr := original.Call(child, "execute_query", map[string]any{"sql": sql, "maxRows": 1, "timeoutSecs": d.s.profile.DatabendOptions.QueryTimeoutSeconds})
	d.s.observed("database-create", r)
	if !r.Dispatched {
		out.Error = errors.Join(callErr, agentbridge.ErrNativeResult)
		return
	}
	out.State, out.Error = "uncertain", callErr
	if acknowledgedDDL(r) {
		out.State = "committed"
	} else if r.ExecutorAcknowledged && r.RemoteError != nil && r.RemoteError.OperationOutcome == "not_started" {
		out.State = "not_executed"
	}
	if out.State == "committed" {
		actual, readErr := d.read(child, original)
		if readErr == nil && !actual.Exists {
			readErr = contract.ErrNamespaceChanged
		}
		if readErr == nil {
			last, lastErr := d.read(child, original)
			readErr = lastErr
			if lastErr == nil && last != actual {
				readErr = contract.ErrNamespaceChanged
			}
		}
		out.Error = errors.Join(out.Error, readErr, child.Err())
		if out.Error == nil {
			out.Actual, out.Verified = &actual, true
		}
	}
	if out.State == "uncertain" || out.State == "committed" && !out.Verified {
		out.Error = errors.Join(out.Error, contract.ErrDatabaseCreateUnresolved)
	}
	raw, _ := json.Marshal(struct {
		Receipt  agentbridge.Receipt
		Original json.RawMessage
	}{r, r.OriginalResponse()})
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sequence++
	out.Receipt = &contract.DatabaseCreateReceipt{DocumentID: d.r.DocumentID, Sequence: d.sequence, Original: before, SQL: sql, NativeJSON: string(raw), State: out.State, Verified: out.Verified, Actual: out.Actual}
	retained := out.Clone()
	d.original = &retained
	// A settled creation belongs to this fixed draft forever. A fresh read
	// must not let the same confirmation create a later same-name database.
	d.blocked = out.State != "not_executed"
	return out.Clone()
}

func (d *databaseCreateDocument) OriginalOutcome() *contract.DatabaseCreateOutcome {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.original == nil {
		return nil
	}
	out := d.original.Clone()
	return &out
}

func (d *databaseCreateDocument) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		<-d.done
		return nil
	}
	d.closed = true
	d.cancel()
	d.mu.Unlock()
	d.active.Wait()
	d.mu.Lock()
	clear(d.key[:])
	d.original = nil
	d.mu.Unlock()
	d.s.mu.Lock()
	delete(d.s.databaseCreateDocuments, d.r.DocumentID)
	d.s.mu.Unlock()
	close(d.done)
	return nil
}

var _ contract.DatabaseCreateDocumentsProvider = (*Session)(nil)

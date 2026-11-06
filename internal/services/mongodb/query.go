package mongodb

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type queryLease struct {
	mu           sync.Mutex
	session      *Session
	ctx          context.Context
	cancel       context.CancelFunc
	closed, busy bool
	lost         bool
}

func (s *Session) ReserveQuery(_ uint64) (contract.QueryLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, contract.ErrRevoked
	}
	if len(s.leases) >= s.profile.MongoDBOptions.QueryMaxConnections {
		return nil, contract.ErrSQLSessionLimit
	}
	ctx, cancel := context.WithCancel(s.ctx)
	l := &queryLease{session: s, ctx: ctx, cancel: cancel}
	s.leases[l] = true
	return l, nil
}
func (l *queryLease) Preconnect(ctx context.Context) error {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed || !l.session.Alive() {
		return contract.ErrRevoked
	}
	return ctx.Err()
}
func (l *queryLease) Release() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	l.cancel()
	l.mu.Unlock()
	s := l.session
	s.mu.Lock()
	delete(s.leases, l)
	s.mu.Unlock()
}
func (l *queryLease) Replace() (contract.QueryLease, error) {
	s := l.session
	l.Release()
	return s.ReserveQuery(0)
}

func commandDocument(text string) (bson.Raw, string, bool, error) {
	raw, err := parseDocument(text, contract.DataEditMaxBytes)
	if err != nil {
		return nil, "", false, err
	}
	elements, err := raw.Elements()
	if err != nil || len(elements) == 0 {
		return nil, "", false, errors.New("MongoDB command is empty")
	}
	name := strings.ToLower(elements[0].Key())
	for _, key := range []string{"$db", "lsid", "txnNumber", "autocommit", "startTransaction"} {
		if raw.Lookup(key).Type != 0 {
			return nil, "", false, contract.ErrUnsupported
		}
	}
	if name == "committransaction" || name == "aborttransaction" || name == "getmore" || name == "killcursors" {
		return nil, "", false, contract.ErrUnsupported
	}
	if concern := raw.Lookup("writeConcern"); concern.Type == bson.TypeEmbeddedDocument {
		w := concern.Document().Lookup("w")
		if w.Type == bson.TypeInt32 && w.Int32() == 0 || w.Type == bson.TypeInt64 && w.Int64() == 0 || w.Type == bson.TypeDouble && w.Double() == 0 {
			return nil, "", false, errors.New("MongoDB commands require acknowledged writes")
		}
	}
	cursor := name == "find" || name == "aggregate" || name == "listcollections" || name == "listindexes"
	if e := raw.Lookup("explain"); e.Type == bson.TypeBoolean && e.Boolean() {
		cursor = false
	}
	return raw, name, cursor, nil
}
func (l *queryLease) Execute(ctx context.Context, database, text string) (contract.SQLResult, error) {
	return l.ExecuteStream(ctx, database, text, nil)
}
func (l *queryLease) ExecuteStream(ctx context.Context, database, text string, sink func(int, []contract.Column, []contract.Cell) error) (result contract.SQLResult, err error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return result, contract.ErrRevoked
	}
	if l.lost {
		l.mu.Unlock()
		return result, contract.ErrSQLSessionLost
	}
	if l.busy {
		l.mu.Unlock()
		return result, errors.New("MongoDB query is already running")
	}
	l.busy = true
	l.mu.Unlock()
	defer func() { l.mu.Lock(); l.busy = false; l.mu.Unlock() }()
	if database == "" || strings.ContainsRune(database, 0) {
		return result, errors.New("select a MongoDB database before executing a command")
	}
	raw, _, cursor, err := commandDocument(text)
	if err != nil {
		return result, err
	}
	ctx, cancel, err := l.session.request(ctx)
	if err != nil {
		return result, err
	}
	defer cancel()
	stop := context.AfterFunc(l.ctx, cancel)
	defer stop()
	start := time.Now()
	defer func() { result.Duration = time.Since(start) }()
	set := contract.ResultSet{Columns: []contract.Column{{Name: documentField, DatabaseType: "Extended JSON"}}}
	appendRow := func(raw bson.Raw) error {
		value, e := extendedJSON(raw)
		if e != nil {
			return e
		}
		cell := preview(value)
		if cell.Truncated {
			set.Truncation = append(set.Truncation, "document preview limited to 1024 characters")
		}
		row := []contract.Cell{cell}
		set.Rows = append(set.Rows, row)
		if sink != nil {
			return sink(0, set.Columns, row)
		}
		return nil
	}
	db := l.session.client.Database(database)
	if cursor {
		var cur *mongo.Cursor
		cur, err = db.RunCommandCursor(ctx, raw)
		if err == nil {
			defer func() {
				cleanup, c := context.WithTimeout(context.Background(), 5*time.Second)
				defer c()
				cur.Close(cleanup)
			}()
			for cur.Next(ctx) {
				if len(set.Rows) >= 1000 {
					set.Truncation = append(set.Truncation, "result limited to 1000 documents")
					break
				}
				if err = appendRow(cur.Current); err != nil {
					break
				}
			}
			if err == nil {
				err = cur.Err()
			}
		}
	} else {
		var response bson.Raw
		err = db.RunCommand(ctx, raw).Decode(&response)
		if err == nil {
			err = appendRow(response)
		}
	}
	result.Sets = []contract.ResultSet{set}
	result.SetLegacyView()
	if err != nil {
		if mongo.IsNetworkError(err) && ctx.Err() == nil {
			l.mu.Lock()
			l.lost = true
			l.mu.Unlock()
			return result, errors.Join(contract.ErrSQLSessionLost, safeError(err))
		}
		return result, safeError(err)
	}
	return result, ctx.Err()
}

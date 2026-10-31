package sqlworkspace

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"io"
)

type fileLease interface {
	ExecuteFile(context.Context, string, io.Reader) (contract.SQLResult, error)
}

func (l *mySQLLease) ExecuteFile(ctx context.Context, db string, reader io.Reader) (contract.SQLResult, error) {
	return l.manager.ExecuteSQLTabTokenFile(ctx, l.token, db, reader)
}

// RunFile is an explicit operation; it never replaces the document text or
// acquires a replacement physical session. Cancellation waits for its receipt.
func (s *State) RunFile(d *Document) {
	if d == nil || d.closed || d.loading || d.recoveryNeeded || !s.connected(d.profileID) {
		return
	}
	lease, ok := d.lease.(fileLease)
	if !ok {
		d.err = "当前会话不支持 SQL 文件执行"
		return
	}
	files := s.persistence.files
	if files.OpenPath == nil || files.OpenReader == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.request++
	request, id, db := d.request, d.profileID, d.database
	ch := make(chan executionResult, 1)
	d.results = ch
	d.cancel = cancel
	d.loading = true
	d.cancelRequested = false
	d.err = ""
	invalidate := s.Invalidate
	go func() {
		var out contract.SQLResult
		path, e := files.OpenPath()
		if e == nil && path != "" {
			var reader io.ReadCloser
			reader, e = files.OpenReader(path)
			if e == nil {
				out, e = lease.ExecuteFile(ctx, db, reader)
				ce := reader.Close()
				if e == nil {
					e = ce
				}
			}
		} else if e == nil {
			e = context.Canceled
		}
		ch <- executionResult{request: request, profileID: id, data: out, err: e}
		if invalidate != nil {
			invalidate()
		}
	}()
}

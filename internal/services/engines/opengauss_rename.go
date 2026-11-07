package engines

import (
	"context"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func (s *Session) openGaussRenameTable(ctx context.Context, request contract.SchemaRenameRequest) (out contract.SchemaDesignOutcome) {
	out.State = "not_executed"
	r := request.Clone()
	target := r.Original.Target
	if err := s.checkOpenGaussTable(target); err != nil {
		out.Error = err
		return
	}
	statement, err := schemaddl.PlanOpenGaussRename(r.Original.Schema, r.Destination)
	if err != nil || r.Original.Schema.OpenGauss.Database != target.Object.Catalog || r.Original.Schema.OpenGauss.Namespace != target.Object.Schema || r.Original.Schema.Table.Name != target.Object.Name {
		out.Error = errors.Join(err, schemaddl.ErrConflict)
		return
	}
	ctx, cancel := s.requestContext(ctx)
	defer cancel()
	c, err := s.db.Conn(ctx)
	if err != nil {
		out.Error = err
		return
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN"); err != nil {
		discardDesign(c)
		out.Error = errors.Join(err, ctx.Err())
		return
	}
	settled, commit := false, false
	defer func() {
		if settled {
			return
		}
		out.Error = errors.Join(out.Error, ctx.Err())
		rollback := rollbackDesign(c)
		if commit || rollback != nil {
			discardDesign(c)
			out.State = "uncertain"
			out.Error = errors.Join(out.Error, ErrUncertain, rollback, s.Close())
		} else {
			out.State = "rolled_back"
		}
	}()
	if _, err = c.ExecContext(ctx, "SET LOCAL search_path=pg_catalog; SET LOCAL standard_conforming_strings=on"); err == nil {
		_, err = c.ExecContext(ctx, "LOCK TABLE "+qualified(target.Object)+" IN ACCESS EXCLUSIVE MODE")
	}
	if err != nil {
		out.Error = err
		return
	}
	before, err := openGaussReadTable(ctx, c, target)
	if err != nil || !before.Schema.Exists || before.Schema.Revision != r.Original.Schema.Revision {
		out.Error = errors.Join(err, schemaddl.ErrConflict)
		return
	}
	if _, err = schemaddl.PlanOpenGaussRename(before.Schema, r.Destination); err != nil {
		out.Error = err
		return
	}
	// Capture the native namespace before DDL, including the old array name.
	// Concurrent name allocation may make verification fail; it never supplies
	// a replacement expected name from the result being verified.
	rows, err := c.QueryContext(ctx, "SELECT typname FROM pg_type WHERE typnamespace=$1 ORDER BY typname LIMIT 50001", before.Schema.OpenGauss.NamespaceOID)
	if err != nil {
		out.Error = err
		return
	}
	occupied := map[string]bool{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		occupied[name] = true
		if len(occupied) > 50000 {
			err = errors.New("native type namespace exceeds rename limit")
			break
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil || occupied[r.Destination] {
		out.Error = errors.Join(err, schemaddl.ErrConflict)
		return
	}
	arrayName, err := schemaddl.OpenGaussRenameArrayName(r.Destination, occupied)
	if err != nil {
		out.Error = err
		return
	}
	if _, err = c.ExecContext(ctx, statement); err != nil {
		out.Error = err
		return
	}
	destination := target
	destination.Object.Name = r.Destination
	actual, err := openGaussReadTable(ctx, c, destination)
	if err != nil {
		out.Error = err
		return
	}
	if err = schemaddl.VerifyOpenGaussRename(before.Schema, r.Destination, arrayName, actual.Schema); err != nil {
		out.Error = err
		return
	}
	missing, err := openGaussReadTable(ctx, c, target)
	if err != nil || missing.Schema.Exists {
		out.Error = errors.Join(err, schemaddl.ErrConflict)
		return
	}
	if err = s.checkOpenGaussTable(target); err != nil {
		out.Error = err
		return
	}
	commit = true
	if _, err = c.ExecContext(ctx, "COMMIT"); err != nil {
		out.Error = err
		return
	}
	settled = true
	out.State, out.Verified, out.Actual = "committed", true, &actual
	return
}

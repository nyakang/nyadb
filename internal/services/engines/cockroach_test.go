package engines

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestCockroachProductValidationAndNativeSequenceSettings(t *testing.T) {
	for _, v := range []string{"PostgreSQL 17.5", "CockroachDB CCL v25.1.9 (fixture)", "CockroachDB CCL v9999999999999999999999.2.0 (fixture)", "CockroachDB CCL v25.2.4evil"} {
		if validateCockroachVersion(v) == nil {
			t.Fatal("wrong product/version accepted")
		}
	}
	if err := validateCockroachVersion("CockroachDB CCL v25.2.4 (fixture)"); err != nil {
		t.Fatal(err)
	}
	s, err := cockroachSequence(`CREATE SEQUENCE "START 7"."CACHE 9" MINVALUE -9223372036854775808 MAXVALUE -1 INCREMENT -5 START -123 CACHE 7`)
	if err != nil || s.Cache != 7 || s.Start != -123 || s.Increment != -5 || s.Min != -9223372036854775808 || !s.DefinitionFromServer {
		t.Fatal("native sequence settings", err, s)
	}
	for _, ddl := range []string{"CREATE SEQUENCE s START 1", "CREATE SEQUENCE s MINVALUE 1 MAXVALUE 9 INCREMENT 1 START 1 CACHE 99999999999999999999", "CREATE SEQUENCE s MINVALUE 1 MAXVALUE 9 INCREMENT 1 START 1 CACHE 7 CACHE 3"} {
		if _, err = cockroachSequence(ddl); err == nil {
			t.Fatal("invalid sequence accepted")
		}
	}
}
func TestCockroachRetryDoesNotReplayUncertainConfirmedOrExplicitWrites(t *testing.T) {
	retry := &pgconn.PgError{Code: "40001", Message: "restart transaction"}
	for _, result := range []*contract.DataEditResult{{CommitConfirmed: true}, {CommitUncertain: true}, {Incomplete: true}} {
		called := 0
		_, err := retryCockroachSave(context.Background(), func() (*contract.DataEditResult, error) { called++; return result, retry })
		if err == nil || called != 1 {
			t.Fatal("unsafe replay")
		}
	}
	called := 0
	_, err := retryCockroachSave(context.Background(), func() (*contract.DataEditResult, error) {
		called++
		if called == 1 {
			return &contract.DataEditResult{}, retry
		}
		return &contract.DataEditResult{CommitConfirmed: true}, nil
	})
	if err != nil || called != 2 {
		t.Fatal("acknowledged rollback not retried", err)
	}
	called = 0
	_, err = retryCockroachSave(context.Background(), func() (*contract.DataEditResult, error) { called++; return nil, &pgconn.PgError{Code: "40003"} })
	if err == nil || called != 1 {
		t.Fatal("ambiguous commit retried")
	}
	for _, uncertain := range []error{ErrUncertain, contract.ErrDataCommitUncertain, context.Canceled, context.DeadlineExceeded} {
		called = 0
		_, err = retryCockroachSave(context.Background(), func() (*contract.DataEditResult, error) {
			called++
			return nil, errors.Join(retry, uncertain)
		})
		if !errors.Is(err, uncertain) || called != 1 {
			t.Fatal("rollback uncertainty or cancellation replayed a batch", called, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	called = 0
	_, err = retryCockroachSave(ctx, func() (*contract.DataEditResult, error) { called++; cancel(); return nil, retry })
	if !errors.Is(err, context.Canceled) || called != 1 {
		t.Fatal("cancel replayed a batch")
	}
	if failureState(connections.CockroachDB, "COMMIT", &pgconn.PgError{Code: "40003"}) != "uncertain" {
		t.Fatal("ambiguous SQL acknowledgement presented as rejected")
	}
}
func TestCockroachConnectionDisablesDDLPrecommit(t *testing.T) {
	p, _ := connections.Defaults(connections.CockroachDB)
	p.TLSMode = "disable"
	cfg, err := postgresConfig(p, "", nil)
	if err != nil || cfg.RuntimeParams["autocommit_before_ddl"] != "off" {
		t.Fatal("DDL precommit enabled", err)
	}
	p.TLSMode = ""
	cfg, err = postgresConfig(p, "", nil)
	if err != nil || cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify {
		t.Fatal("default CockroachDB TLS lost host verification", err)
	}
	p.TLSMode = "disable"
	p.Kind = connections.PostgreSQL
	cfg, err = postgresConfig(p, "", nil)
	if err != nil || cfg.RuntimeParams["autocommit_before_ddl"] != "" {
		t.Fatal("PostgreSQL configuration changed", err)
	}
}

type cockroachCommitResponse struct {
	contract.Transaction
	result contract.CommitResult
	err    error
}

func (t cockroachCommitResponse) Commit(context.Context) (contract.CommitResult, error) {
	return t.result, t.err
}

func TestCockroachCommitRetryDiscardsRolledBackRowSnapshots(t *testing.T) {
	s := &Session{kind: connections.CockroachDB}
	retry := &pgconn.PgError{Code: "40001"}
	for _, failure := range []contract.CommitResult{{}, {Uncertain: true}, {CommitConfirmed: true, Applied: 1, Incomplete: true}} {
		attempts := 0
		out, err := retryCockroachSave(context.Background(), func() (*contract.DataEditResult, error) {
			attempts++
			beforeCommit := &contract.DataEditResult{Applied: 1, Saved: []contract.Edit{{Kind: "insert"}}, Outcomes: []contract.DataEditOutcome{{Kind: "insert", Saved: &contract.RowSnapshot{Columns: []string{"id"}}}}}
			return s.commitDataResult(context.Background(), cockroachCommitResponse{result: failure, err: retry}, beforeCommit)
		})
		if !cockroachRetryable(err) {
			t.Fatal("retry error lost", err)
		}
		if !failure.Uncertain && !failure.CommitConfirmed {
			if attempts != 5 || out.Applied != 0 || len(out.Saved) != 0 || len(out.Outcomes) != 0 {
				t.Fatal("exhausted retries exposed rolled-back rows", attempts, out)
			}
		} else if attempts != 1 || len(out.Outcomes) != 1 {
			t.Fatal("uncertain or confirmed result replayed or lost its verification snapshot", attempts, out)
		}
	}
}

func TestCockroachCollatedSortCannotPublishKeysetCursor(t *testing.T) {
	s := &Session{kind: connections.CockroachDB}
	meta := contract.Table{Key: []string{"id"}, Columns: []contract.Field{{Name: "id", DatabaseType: "bigint"}, {Name: "label", DatabaseType: "STRING COLLATE en"}}}
	_, stable, _, err := s.cockroachDataOrder(meta, contract.PageRequest{Sorts: []contract.Sort{{Column: "label"}}}, "identity")
	if err != nil || stable {
		t.Fatal("collated ordering published a portable cursor", err)
	}
}

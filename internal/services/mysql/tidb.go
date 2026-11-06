package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func validateMySQLFamilyServer(ctx context.Context, db *sql.DB, kind connections.Kind) error {
	if kind == connections.MariaDB {
		return validateMariaDBServer(ctx, db)
	}
	if kind != connections.TiDB {
		return nil
	}
	var version string
	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return err
	}
	return validateTiDBVersion(version)
}

var tidbVersionPattern = regexp.MustCompile(`(?i)(?:^|-)tidb-v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:$|[-+])`)

func validateProductObject(kind connections.Kind, object string) error {
	if kind != connections.TiDB {
		return nil
	}
	switch strings.ToLower(object) {
	case "database", "数据库", "view", "视图", "sequence", "序列", "table", "表":
		return nil
	}
	return fmt.Errorf("%w: TiDB does not support this object kind", contract.ErrUnsupported)
}

// Check on the executing connection before any mutation. TiDB otherwise accepts
// CHECK syntax while silently discarding it when the feature is disabled.
func guardTiDBChecks(ctx context.Context, c *sql.Conn, kind connections.Kind, statements []string) error {
	if kind != connections.TiDB {
		return nil
	}
	needed := false
	for _, statement := range statements {
		tokens, err := sqltext.Tokens(statement)
		if err != nil {
			return err
		}
		for i, token := range tokens {
			if token.Comment && (strings.HasPrefix(token.Text, "/*!") || strings.HasPrefix(token.Text, "/*T!")) && strings.Contains(strings.ToUpper(token.Text), "CHECK") {
				needed = true
			}
			if sqltext.Word(token, "CHECK") || sqltext.Word(token, "CONSTRAINT") && i > 0 && sqltext.Word(tokens[i-1], "DROP") {
				needed = true
			}
		}
	}
	if !needed {
		return nil
	}
	var enabled int
	if err := c.QueryRowContext(ctx, "SELECT @@GLOBAL.tidb_enable_check_constraint").Scan(&enabled); err != nil {
		return err
	}
	if enabled != 1 {
		return fmt.Errorf("%w: TiDB CHECK execution requires tidb_enable_check_constraint=ON; draft retained", contract.ErrUnsupported)
	}
	return nil
}

// TiDB 8.5 LTS is the supported baseline. The MySQL protocol version prefix
// does not describe the TiDB product release and must never grant capabilities.
func validateTiDBVersion(version string) error {
	match := tidbVersionPattern.FindStringSubmatch(strings.TrimSpace(version))
	if len(match) != 4 {
		return fmt.Errorf("%w: TiDB connection requires a recognized TiDB server version", contract.ErrUnsupported)
	}
	major, e1 := strconv.Atoi(match[1])
	minor, e2 := strconv.Atoi(match[2])
	if e1 != nil || e2 != nil || major < 8 || major == 8 && minor < 5 {
		return fmt.Errorf("%w: TiDB 8.5 or later is required", contract.ErrUnsupported)
	}
	return nil
}

func readTiDBSequences(ctx context.Context, db *sql.DB, database string, objects []MySQLDBObject) ([]MySQLDBObject, error) {
	index := map[string]int{}
	for i, o := range objects {
		if o.Kind == "序列" {
			index[o.Name] = i
		}
	}
	if len(index) == 0 {
		return objects, nil
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	rows, err := conn.QueryContext(ctx, "SELECT SEQUENCE_NAME, START, INCREMENT, MIN_VALUE, MAX_VALUE, CACHE_VALUE, CYCLE FROM information_schema.SEQUENCES WHERE SEQUENCE_SCHEMA=? ORDER BY SEQUENCE_NAME", database)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name string
		sequence := contract.SequenceInfo{DataType: "bigint", DefinitionFromServer: true}
		var cycle int
		if err = rows.Scan(&name, &sequence.Start, &sequence.Increment, &sequence.Min, &sequence.Max, &sequence.Cache, &cycle); err != nil {
			rows.Close()
			return nil, err
		}
		sequence.Cycle = cycle != 0
		if i, ok := index[name]; ok {
			objects[i].Sequence = &sequence
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for name, i := range index {
		if objects[i].Sequence == nil {
			return nil, errors.New("TiDB sequence metadata changed during browsing")
		}
		objects[i].Definition, err = readCreate(ctx, conn, "SEQUENCE", database, name)
		if err != nil {
			return nil, err
		}
	}
	return objects, nil
}

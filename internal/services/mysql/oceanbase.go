package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// The MySQL protocol prefix is independent of the OceanBase product release.
var oceanBaseVersionPattern = regexp.MustCompile(`(?i)(?:^|-)OceanBase(?:_CE)?-v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:\.|$|[-+])`)

func validateOceanBaseVersion(version, mode string) error {
	match := oceanBaseVersionPattern.FindStringSubmatch(strings.TrimSpace(version))
	if match == nil || !strings.EqualFold(strings.TrimSpace(mode), "MYSQL") {
		return fmt.Errorf("%w: OceanBase connection requires an OceanBase MySQL tenant", contract.ErrUnsupported)
	}
	major, e1 := strconv.Atoi(match[1])
	minor, e2 := strconv.Atoi(match[2])
	patch, e3 := strconv.Atoi(match[3])
	if e1 != nil || e2 != nil || e3 != nil || major < 4 || major == 4 && (minor < 3 || minor == 3 && patch < 5) {
		return fmt.Errorf("%w: OceanBase 4.3.5 or later is required", contract.ErrUnsupported)
	}
	return nil
}

func validateOceanBaseServer(ctx context.Context, db *sql.DB) error {
	var version, mode string
	if err := db.QueryRowContext(ctx, "SELECT VERSION(), @@ob_compatibility_mode").Scan(&version, &mode); err != nil {
		return err
	}
	return validateOceanBaseVersion(version, mode)
}

// SHOW CREATE SEQUENCE is unavailable in the MySQL tenant. Reconstruct the
// definition from stored attributes, never from cached/allocated LAST_NUMBER.
func readOceanBaseSequences(ctx context.Context, db *sql.DB, database string, objects []MySQLDBObject) ([]MySQLDBObject, error) {
	const query = `SELECT s.SEQUENCE_NAME, s.START_WITH, s.INCREMENT_BY,
s.MIN_VALUE, s.MAX_VALUE, s.CACHE_SIZE, s.CYCLE_FLAG, s.ORDER_FLAG
FROM oceanbase.DBA_OB_SEQUENCE_OBJECTS s
JOIN oceanbase.__all_database d ON d.database_id=s.DATABASE_ID
WHERE d.database_name=? AND s.IS_SYSTEM_GENERATED=0 ORDER BY s.SEQUENCE_NAME`
	rows, err := db.QueryContext(ctx, query, database)
	if err != nil {
		var denied *mysqldriver.MySQLError
		if errors.As(err, &denied) && (denied.Number == 1142 || denied.Number == 1143) {
			// These sequences are not visible to this account. Permission to
			// read ordinary tables must not require access to tenant DBA views.
			return objects, nil
		}
		return nil, err
	}
	defer rows.Close()
	dbName, err := quotedMySQLIdentifier(database)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name string
		var cycle, ordered int
		info := &contract.SequenceInfo{DataType: "bigint"}
		if err = rows.Scan(&name, &info.Start, &info.Increment, &info.Min, &info.Max, &info.Cache, &cycle, &ordered); err != nil {
			return nil, err
		}
		if len(objects) >= maxMySQLMetadataObjects {
			return nil, ErrMySQLMetadataLimit
		}
		identifier, err := quotedMySQLIdentifier(name)
		if err != nil {
			return nil, err
		}
		info.Cycle = cycle != 0
		cacheSQL, cycleSQL, orderSQL := fmt.Sprintf("CACHE %d", info.Cache), "NOCYCLE", "NOORDER"
		if info.Cache == 0 {
			cacheSQL = "NOCACHE"
		}
		if info.Cycle {
			cycleSQL = "CYCLE"
		}
		if ordered != 0 {
			orderSQL = "ORDER"
		}
		definition := fmt.Sprintf("CREATE SEQUENCE %s.%s START WITH %d INCREMENT BY %d MINVALUE %d MAXVALUE %d %s %s %s", dbName, identifier, info.Start, info.Increment, info.Min, info.Max, cacheSQL, cycleSQL, orderSQL)
		objects = append(objects, MySQLDBObject{Name: name, Kind: "序列", Sequence: info, Definition: definition})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(objects, func(i, j int) bool {
		if objects[i].Kind != objects[j].Kind {
			return objects[i].Kind < objects[j].Kind
		}
		return objects[i].Name < objects[j].Name
	})
	return objects, nil
}

// OceanBase omits definer/security headers from SHOW CREATE VIEW and reports
// SECURITY_TYPE=NONE. Retain the metadata owner in the optimistic fingerprint.
func readOceanBaseViewOwner(ctx context.Context, c *sql.Conn, r *ObjectDefinition) error {
	if ctx.Value(mysqlProductKey{}) != connections.OceanBase || r.Kind != "view" && r.Kind != "视图" {
		return nil
	}
	var security, check string
	if err := c.QueryRowContext(ctx, "SELECT DEFINER, SECURITY_TYPE, CHECK_OPTION FROM information_schema.VIEWS WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", r.Database, r.Name).Scan(&r.Definer, &security, &check); err != nil {
		return err
	}
	if security != "NONE" || check != "NONE" || r.Definer == "" {
		return fmt.Errorf("%w: unrecognized OceanBase view security metadata", contract.ErrUnsupported)
	}
	r.Product = "oceanbase"
	return nil
}

func guardOceanBaseViewPlan(ctx context.Context, c *sql.Conn, p ObjectPlan, kind string) error {
	if ctx.Value(mysqlProductKey{}) != connections.OceanBase {
		return nil
	}
	if kind == "DATABASE" && p.Action != "drop" {
		return fmt.Errorf("%w: OceanBase database attributes require native SQL", contract.ErrUnsupported)
	}
	if kind != "VIEW" || p.Action == "drop" {
		return nil
	}
	want, err := parseVerifiedObject(p.SQL, kind, p.Original.ObjectIdentity)
	if err != nil {
		return err
	}
	if want.definer != "" || want.options["algorithm"] != "" || want.options["security"] != "" {
		return fmt.Errorf("%w: OceanBase view definer, algorithm and SQL SECURITY changes are unavailable", contract.ErrUnsupported)
	}
	for i, token := range want.body {
		if sqltext.Word(token, "CHECK") && i+1 < len(want.body) && sqltext.Word(want.body[i+1], "OPTION") {
			return fmt.Errorf("%w: OceanBase view CHECK OPTION changes are unavailable", contract.ErrUnsupported)
		}
	}
	if p.Action != "create" {
		var owner string
		if err = c.QueryRowContext(ctx, "SELECT CURRENT_USER()").Scan(&owner); err != nil {
			return err
		}
		if owner != p.Original.Definer || p.Original.Product != "oceanbase" {
			return fmt.Errorf("%w: OceanBase view changes require the original owner", contract.ErrUnsupported)
		}
	}
	return nil
}

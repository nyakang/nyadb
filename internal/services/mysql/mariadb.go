package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// MariaDB 10.11 is the oldest supported LTS baseline for the shared workbench
// tools. Verify the product before publishing the session, including tests.
func validateMariaDBServer(ctx context.Context, db *sql.DB) error {
	var version string
	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return err
	}
	return validateMariaDBVersion(version)
}

func validateMariaDBVersion(version string) error {
	if !strings.Contains(strings.ToLower(version), "mariadb") {
		return fmt.Errorf("%w: MariaDB connection requires a MariaDB server", contract.ErrUnsupported)
	}
	version = strings.TrimPrefix(version, "5.5.5-")
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 3 {
		return fmt.Errorf("%w: unrecognized MariaDB server version", contract.ErrUnsupported)
	}
	major, e1 := strconv.Atoi(parts[0])
	minor, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil || major < 10 || major == 10 && minor < 11 {
		return fmt.Errorf("%w: MariaDB 10.11 or later is required", contract.ErrUnsupported)
	}
	return nil
}

#!/usr/bin/env bash
set -euo pipefail
mysql_fixture="nyadb-unified-mysql-$RANDOM-$$"
postgres_fixture="nyadb-unified-pg-$RANDOM-$$"
cleanup() {
 docker rm -f "$mysql_fixture" "$postgres_fixture" >/dev/null 2>&1 || true
}
trap cleanup EXIT
for fixture in "$mysql_fixture" "$postgres_fixture"; do
 if docker container inspect "$fixture" >/dev/null 2>&1; then
  echo 'Refusing to reuse an existing fixture' >&2
  exit 1
 fi
done
docker run --rm -d --name "$mysql_fixture" -p 127.0.0.1::3306 -e MYSQL_ALLOW_EMPTY_PASSWORD=yes mysql:8.4 --log-bin-trust-function-creators=1 >/dev/null
docker run --rm -d --name "$postgres_fixture" -p 127.0.0.1::5432 -e POSTGRES_USER=nyadb_it -e POSTGRES_DB=nyadb_it -e POSTGRES_HOST_AUTH_METHOD=trust postgres:17-alpine >/dev/null
for attempt in {1..65}; do
 if docker exec "$mysql_fixture" mysqladmin ping --protocol=tcp -h127.0.0.1 -uroot --silent >/dev/null 2>&1 && docker exec "$postgres_fixture" pg_isready -U nyadb_it -d nyadb_it >/dev/null 2>&1; then break; fi
 sleep 1
done
docker exec -i "$mysql_fixture" mysql -uroot <<'SQL'
CREATE DATABASE nyadb_core_it;
CREATE USER 'nyadb_core'@'%' IDENTIFIED BY '';
GRANT ALL PRIVILEGES ON nyadb_core_it.* TO 'nyadb_core'@'%';
GRANT ALL PRIVILEGES ON nyadb_object_database.* TO 'nyadb_core'@'%';
CREATE DATABASE table_edit_it;
CREATE USER 'data_probe'@'%' IDENTIFIED BY '';
CREATE USER 'data_readonly'@'%' IDENTIFIED BY '';
GRANT ALL PRIVILEGES ON table_edit_it.* TO 'data_probe'@'%';
GRANT SELECT ON table_edit_it.* TO 'data_readonly'@'%';
CREATE DATABASE nyadb_toolbar_tx_it;
CREATE USER 'nyadb_toolbar_tx'@'%' IDENTIFIED BY '';
GRANT ALL PRIVILEGES ON nyadb_toolbar_tx_it.* TO 'nyadb_toolbar_tx'@'%';
SQL
mysql_port=$(docker port "$mysql_fixture" 3306/tcp)
mysql_port=${mysql_port##*:}
postgres_port=$(docker port "$postgres_fixture" 5432/tcp)
postgres_port=${postgres_port##*:}
export CGO_ENABLED=0 GOWORK=off NYADB_MYSQL_CORE_IT=1 NYADB_MYSQL_CORE_PORT="$mysql_port" NYADB_POSTGRES_IT=1 NYADB_POSTGRES_PORT="$postgres_port"
export NYADB_MYSQL_DATA_EDIT_IT=1 NYADB_MYSQL_DATA_EDIT_PORT="$mysql_port" NYADB_MYSQL_DATA_EDIT_DATABASE=table_edit_it
export NYADB_MYSQL_DATA_TX_IT=1 NYADB_MYSQL_DATA_TX_PORT="$mysql_port"
export NYADB_UNIFIED_GRID_CAPTURE_DIR="${NYADB_UNIFIED_GRID_CAPTURE_DIR:-$PWD/build/unified-grid}"
go test ./internal/services/mysql -run '^(TestTableDesignDisposableMySQLRoundTrip|TestSQLPipelineDisposableTypedMultiResultsAndFullStream|TestMySQLObjectManagementCoreFixture|TestMySQLBackupRestoreCoreFixture|TestNontransactionalRestoreCoreFixture|TestMySQLDataEditingIntegration|TestMySQLDataWholeCellEditingIntegration|TestMySQLDataUnknownCommitNetworkIntegration|TestMySQLDataDocumentTransactionIntegration)$' -count=1 -v
go test ./internal/services/engines -run '^TestPostgreSQL17Fixture$' -count=1 -v
go test ./internal/ui/dataworkspace -run '^TestUnifiedGridRepresentative$' -count=1 -v

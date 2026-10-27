#!/usr/bin/env bash
# Disposable, opt-in MySQL 8.4 fixture for the two-profile NyaDB UI integration test.
# Runs only on localhost, never touches an existing container or production database.
set -euo pipefail

container="nyadb-multi-asset-it-20261027"
if docker container inspect "$container" >/dev/null 2>&1; then
  echo "Refusing to reuse existing container: $container" >&2
  exit 1
fi

docker run --detach --rm --name "$container" \
  --publish 127.0.0.1::3306 \
  --env MYSQL_ALLOW_EMPTY_PASSWORD=yes \
  mysql:8.4 >/dev/null
trap 'docker rm -f "$container" >/dev/null 2>&1 || true' EXIT

ready=0
for _ in $(seq 1 65); do
  if docker exec "$container" mysqladmin ping -uroot --silent >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
if [ "$ready" != 1 ]; then
  echo "Disposable MySQL failed to become ready" >&2
  exit 1
fi

docker exec -i "$container" mysql -uroot <<'SQL'
CREATE DATABASE nyadb_multi_shared;
CREATE TABLE nyadb_multi_shared.a_orders (id INT PRIMARY KEY, note VARCHAR(30));
CREATE TABLE nyadb_multi_shared.b_orders (id INT PRIMARY KEY, note VARCHAR(30));
INSERT INTO nyadb_multi_shared.a_orders VALUES (1,'a');
INSERT INTO nyadb_multi_shared.b_orders VALUES (2,'b');
CREATE USER 'nyadb_multi_a'@'%' IDENTIFIED BY '';
CREATE USER 'nyadb_multi_b'@'%' IDENTIFIED BY '';
GRANT SELECT ON nyadb_multi_shared.a_orders TO 'nyadb_multi_a'@'%';
GRANT SELECT ON nyadb_multi_shared.b_orders TO 'nyadb_multi_b'@'%';
SQL

port="$(docker port "$container" 3306/tcp | awk -F: 'NR==1 { print $NF }')"
if ! [[ "$port" =~ ^[0-9]+$ ]] || (( port < 1024 || port > 65535 )); then
  echo "Invalid temporary loopback port" >&2
  exit 1
fi

echo "Testing two isolated localhost-only profiles on a disposable MySQL 8.4 fixture"
NYADB_MYSQL_MULTI_IT=1 NYADB_MYSQL_MULTI_PORT="$port" \
  go test ./internal/ui/shell -run '^TestMySQLTwoRealProfilesIsolateSameSchemaMetadata$' -count=1 -v

module github.com/nyakang/nyadb

go 1.27.3

require (
	gitcode.com/opengauss/openGauss-connector-go-pq v1.0.9
	gitea.com/kingbase/gokb v0.0.0-20201021123113-29bd62a876c3
	github.com/ClickHouse/clickhouse-go/v2 v2.51.0
	github.com/HuaweiCloudDeveloper/gaussdb-go v1.0.0-rc1
	github.com/ebitengine/purego v0.11.3
	github.com/egoist/mygo v0.8.3
	github.com/go-sql-driver/mysql v1.10.1
	github.com/golang-sql/sqlexp v0.1.0
	github.com/jackc/pgx/v5 v5.12.0
	github.com/microsoft/go-mssqldb v1.11.2
	github.com/neo4j/neo4j-go-driver/v6 v6.4.0
	github.com/parquet-go/parquet-go v0.32.0
	github.com/redis/go-redis/v9 v9.23.0
	github.com/shopspring/decimal v1.6.0
	github.com/sijms/go-ora/v2 v2.9.0
	github.com/zalando/go-keyring v0.2.8
	go.etcd.io/etcd/api/v3 v3.7.3
	go.mongodb.org/mongo-driver/v2 v2.10.0
	golang.org/x/crypto v0.58.0
	golang.org/x/net v0.62.0
	golang.org/x/sys v0.50.0
	golang.org/x/text v0.44.0
	google.golang.org/grpc v1.85.0
	google.golang.org/protobuf v1.36.12
	modernc.org/sqlite v1.62.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/ClickHouse/ch-go v0.75.0 // indirect
	github.com/andybalholm/brotli v1.3.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/go-faster/city v1.0.1 // indirect
	github.com/go-faster/errors v0.8.0 // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	github.com/godbus/dbus/v5 v5.2.4 // indirect
	github.com/golang-sql/civil v0.0.0-20220223132316-b832511892a9 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.33.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/klauspost/compress v1.20.4 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/parquet-go/bitpack v1.1.0 // indirect
	github.com/parquet-go/jsonlite v1.5.5 // indirect
	github.com/paulmach/orb v0.14.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.33 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/tjfoc/gmsm v1.4.1 // indirect
	github.com/twpayne/go-geom v1.7.0 // indirect
	github.com/xdg-go/pbkdf2 v1.0.0 // indirect
	github.com/xdg-go/scram v1.4.0 // indirect
	github.com/xdg-go/stringprep v1.0.4 // indirect
	github.com/youmark/pkcs8 v0.0.0-20240726163527-a2c0da244d78 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/image v0.48.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/xerrors v0.0.0-20240903120638-7835f813f4da // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20261110000943-09e21b237f2c // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261110000943-09e21b237f2c // indirect
	modernc.org/libc v1.80.2 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

tool github.com/egoist/mygo/cmd/mygo

replace github.com/egoist/mygo => github.com/nyakang/mygo v0.8.1-nyadb.1

replace gitcode.com/opengauss/openGauss-connector-go-pq => ./third_party/opengauss_pq

replace gitea.com/kingbase/gokb => ./third_party/kingbase_gokb

module github.com/nyakang/nyadb

go 1.27.3

require (
	gitcode.com/opengauss/openGauss-connector-go-pq v1.0.9
	github.com/ClickHouse/clickhouse-go/v2 v2.50.0
	github.com/HuaweiCloudDeveloper/gaussdb-go v1.0.0-rc1
	github.com/ebitengine/purego v0.11.3
	github.com/egoist/mygo v0.7.4
	github.com/go-sql-driver/mysql v1.10.1
	github.com/golang-sql/sqlexp v0.1.0
	github.com/jackc/pgx/v5 v5.12.0
	github.com/microsoft/go-mssqldb v1.11.2
	github.com/parquet-go/parquet-go v0.32.0
	github.com/redis/go-redis/v9 v9.23.0
	github.com/shopspring/decimal v1.6.0
	github.com/sijms/go-ora/v2 v2.9.0
	github.com/zalando/go-keyring v0.2.8
	go.mongodb.org/mongo-driver/v2 v2.9.3
	golang.org/x/crypto v0.58.0
	golang.org/x/net v0.62.0
	golang.org/x/sys v0.49.0
	golang.org/x/text v0.43.0
	modernc.org/sqlite v1.61.1
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/ClickHouse/ch-go v0.75.0 // indirect
	github.com/andybalholm/brotli v1.2.8 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/go-faster/city v1.0.1 // indirect
	github.com/go-faster/errors v0.8.0 // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	github.com/godbus/dbus/v5 v5.2.4 // indirect
	github.com/golang-sql/civil v0.0.0-20220223132316-b832511892a9 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
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
	github.com/xdg-go/scram v1.2.0 // indirect
	github.com/xdg-go/stringprep v1.0.4 // indirect
	github.com/youmark/pkcs8 v0.0.0-20240726163527-a2c0da244d78 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/image v0.47.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/xerrors v0.0.0-20200804184101-5ec99f83aff1 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	modernc.org/libc v1.80.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

tool github.com/egoist/mygo/cmd/mygo

replace github.com/egoist/mygo => github.com/nyakang/mygo v0.7.4-nyadb.1

replace gitcode.com/opengauss/openGauss-connector-go-pq => ./third_party/opengauss_pq

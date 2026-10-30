module github.com/nyakang/nyadb

go 1.27.3

require (
	github.com/ebitengine/purego v0.11.1
	github.com/egoist/mygo v0.7.3
	github.com/go-sql-driver/mysql v1.10.1
	github.com/zalando/go-keyring v0.2.6
	modernc.org/sqlite v1.60.1
)

require (
	al.essio.dev/pkg/shellescape v1.5.1 // indirect
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/andybalholm/brotli v1.1.1 // indirect
	github.com/danieljoos/wincred v1.2.2 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/parquet-go/bitpack v1.0.0 // indirect
	github.com/parquet-go/jsonlite v1.0.0 // indirect
	github.com/parquet-go/parquet-go v0.32.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.21 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/twpayne/go-geom v1.6.1 // indirect
	golang.org/x/image v0.46.0 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/protobuf v1.34.2 // indirect
	modernc.org/libc v1.77.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

tool github.com/egoist/mygo/cmd/mygo

replace github.com/egoist/mygo => github.com/nyakang/mygo v0.7.3-nyadb.1

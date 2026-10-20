# NyaDB

NyaDB is a lightweight, cross-platform database workbench written in Go and
[MyGo Native UI](https://github.com/egoist/mygo). M0 is complete, and
the UI-1/UI-2 workbench layout prototype is implemented with mock data.
There are **no live database connections yet**.

![NyaDB native workbench preview](docs/ui-preview.png)

## Implemented features (M0 + UI-1/UI-2)

- Compact dark desktop workbench with a toolbar, menu row and status bar.
- Proportional, draggable explorer/workspace/inspector panes and an inspector toggle.
- Expandable connection tree with Chinese-label search and mock metadata.
- Multiple SQL tabs, an SQL-highlighting editor and safe mock query button.
- Filterable object list and virtualized `ui.Table` preview of **100,000
  synthetic rows and 50 columns**, generated on demand.
- Headless UI tests of connections, tabs, search, inspector and resize behavior.
- Windows, Linux and macOS build and test CI.

**Important:** Demo results are synthetic. No SQL is executed against a database
until later milestones (M2–M5).

## Requirements

- Go **1.27+**; the repository pins MyGo **v0.5.5**.
- Linux GUI runtime requires GTK 3; on Ubuntu/Debian install
  `libgtk-3-0`. A graphical session is required to display the window.
- Windows and macOS use their system-native libraries.
- No Bun/Node toolchain is required.

## Run

```bash
go mod download
go tool mygo doctor
go tool mygo dev
```

To run without the watcher:

```bash
go run ./cmd/nyadb
```

## Test and build

```bash
go test ./...
go vet ./...
go tool mygo vet
go build ./cmd/nyadb
```

Build native application packages (see MyGo's platform-specific requirements):

```bash
go tool mygo build
```

To cross-compile a Windows binary from Linux without cgo:

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o build/nyadb.exe ./cmd/nyadb
```

## Custom Go cache under /data

The repository does not hard-code personal paths. To keep the Go cache and
downloaded modules outside the system disk, configure your shell or CI executor:

```bash
export GOPATH=/data/.cache/golang/gopath
export GOMODCACHE=/data/.cache/golang/mod
export GOCACHE=/data/.cache/golang/build
export GOTMPDIR=/data/.cache/golang/tmp
export GOENV=/data/.cache/golang/go.env
export PATH="/usr/local/go/bin:$GOPATH/bin:$PATH"
```

The Repo Code execution service may not inherit `~/.bashrc`; it must receive
these environment variables separately. Ensure the service account has write
access to the target directories.

## Project organization

```text
cmd/nyadb/            desktop entrypoint
internal/ui/shell/    native desktop shell and headless UI tests
resources/icon.png    generated placeholder application icon
tools/generate_icon.py reproducible icon generator
docs/                 design, MVP goal, and implementation status
```

## Project roadmap

See [the technical design](docs/NYADB_TECHNICAL_DESIGN.md),
[MVP implementation goal](docs/NYADB_MVP_GOAL.md), and
[UI layout and interactions](docs/UI_LAYOUT.md) and
[implementation status](docs/implementation-status.md). Database adapters,
credentials, SQL execution and paged real results are explicitly outside M0.

# SStorytime-KV — Project Notes for Claude

## What This Project Is

SStorytime-KV is a port of SStorytime that replaces PostgreSQL with BadgerDB (an embedded key-value store). It implements the same N4L (Narrative for Learning) language parser and graph database, but with no external database server required.

## Repository Layout

```
SStorytime-KV/
├── go.mod                      # Module root: "SSTorytimeKV" (Go 1.24.4)
├── Makefile                    # Root build orchestrator
├── pkg/
│   ├── SSTorytime/
│   │   ├── SSTorytime.go       # Core library (192KB) — main logic
│   │   ├── backend_interface.go# Storage backend abstraction
│   │   ├── kv_badger.go        # BadgerDB implementation
│   │   ├── store_crud.go       # CRUD operations
│   │   ├── store_indexes.go    # Index layer
│   │   └── store_paths.go      # Path management
│   └── store/                  # Store interface
├── src/
│   ├── Makefile                # Delegates to root (go.mod is at root)
│   ├── N4L.go                  # N4L parser/compiler binary
│   ├── searchN4L.go            # Search binary
│   └── pathsolve.go            # Path solving binary
├── tests/
│   ├── Makefile
│   └── run_tests               # Shell script: 31 pass, 12 fail, 1 warn tests
├── examples/
│   ├── Makefile
│   └── sst_data/               # Pre-built BadgerDB data files
└── SSTconfig/                  # .sst configuration files
```

## Build System

**Critical:** The Go module (`module SSTorytimeKV`) is declared in the **root** `go.mod`. All `go build` commands must run from the project root, not from within `src/`. This differs from SStorytime-main, which has a separate `src/go.mod`.

```bash
make              # builds N4L, searchN4L, pathsolve (Linux) or N4L.exe etc. (Windows)
make test         # runs tests via tests/run_tests
make clean        # removes built binaries
cd src && make    # delegates to root — same result
```

Cross-platform: Makefiles detect `$(OS) == Windows_NT` and append `.exe` to binary names.

## Key Differences from SStorytime-main

| Aspect | SStorytime-main | SStorytime-KV |
|--------|-----------------|----------------|
| Database | PostgreSQL (lib/pq) | BadgerDB (embedded) |
| go.mod | root + src/go.mod | root only |
| Binaries | 10+ (N4L, searchN4L, text2N4L, removeN4L, notes, graph_report, http_server, API examples) | 3 (N4L, searchN4L, pathsolve) |
| Binary output dir | src/ | root |
| Setup required | PostgreSQL server + credentials | none (DB in sst_data/) |
| pkg/store | not present | backend interface + BadgerDB impl |

## Root-Level Utility Files (NOT in Standard Build)

`cleaner.go`, `cleanup.go`, `find_stubs.go`, `strip_funcs.go`, `stub_db.go` are `package main` migration tools used during the PostgreSQL→BadgerDB porting process. They manipulate `pkg/SSTorytime/SSTorytime.go` via regex to strip SQL code and inject BadgerDB calls. Run individually with `go run <file>.go` if needed. They are excluded from `make` because multiple `package main` files cannot coexist in the same directory build.

## Dependencies

Notable packages in go.mod:
- `github.com/dgraph-io/badger/v4` — embedded KV store
- `github.com/lib/pq` — listed but should be vestigial (PostgreSQL driver, not used)
- OpenTelemetry packages — pulled in transitively by BadgerDB

## Data Storage

BadgerDB stores data in `sst_data/` directory (relative to where binaries are run). The `examples/sst_data/` contains pre-built example data.

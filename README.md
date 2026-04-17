# SStorytime-KV

A port of Mark Burgess's [SStorytime](https://github.com/markburgess/SSTorytime/) that replaces the PostgreSQL backend
with [BadgerDB](https://github.com/dgraph-io/badger) — an embedded, serverless
key-value store.  No PostgreSQL installation, no Docker, no credentials required.

---

## Prerequisites

| Tool | Version |
|------|---------|
| Go   | ≥ 1.24.4 |
| make | any |

No external database is needed.

---

## Building

```bash
cd SStorytime-KV

# Build all binaries (N4L, searchN4L, pathsolve) into the project root
make

# On Windows (MinGW/MSYS) the same command produces N4L.exe etc.
```

The root `Makefile` drives everything.  Because the Go module (`module SSTorytimeKV`)
is declared at the project root, all `go build` commands must originate there.
The `src/Makefile` simply delegates to the root.

### Individual binaries

```bash
go build -o N4L       ./src/N4L.go
go build -o searchN4L ./src/searchN4L.go
go build -o pathsolve ./src/pathsolve.go
```

### Clean

```bash
make clean
```

---

## Running

N4L reads its arrow/type configuration from `SSTconfig/` (relative to the
working directory) and stores graph data in `sst_data/` (also relative to cwd).
Always run the binary from the project root:

```bash
# Parse and upload an N4L file
./N4L -u examples/doors.n4l

# Load a set of example files (same as examples/Makefile `all` target)
make -C examples

# Search
./searchN4L <query>

# Path solving
./pathsolve
```

The `sst_data/` directory is created automatically on first run.
To start fresh, delete it:

```bash
rm -rf sst_data/
```

---

## Note Update / Re-submit Workflow

When you edit a note file and want to re-import it, use the `-force` flag:

```bash
./N4L -u -force examples/my_notes.n4l
```

`-force` now performs a **safe delete-then-reinsert**:

1. Detects which chapters in the file already exist in the database.
2. Calls `DeleteChapterFromDB` for each conflicting chapter — removing all nodes
   that belong exclusively to that chapter and cleaning up dangling links in the
   rest of the graph.
3. Shared nodes (belonging to multiple chapters) have the deleted chapter removed
   from their chapter list and are preserved.
4. Re-imports the edited file from scratch.

Without `-force` the tool still warns you and refuses to overwrite.

You can also delete a chapter manually before re-importing:

```bash
./removeN4L -chapter my_notes
./N4L -u examples/my_notes.n4l
```

---

## Running Tests

### Unit and correctness tests

```bash
cd tests
go test -v ./...
```

This runs all 52 tests across four files:

| File | Tests | What is covered |
|------|-------|----------------|
| `algorithms_test.go` | 14 | Empty-graph contracts — no panic on boundary input |
| `correctness_test.go` | 13 | Real inserted data — verifies correct results |
| `chapter_update_test.go` | 20 | Chapter management, idempotency, re-submit workflow |
| `stress_test.go` | 2 | 1 000-node synthetic graph + MobyDick integration (9 649 nodes) |

The chapter update tests cover:

| Area | Tests |
|------|-------|
| Idempotent `AddNode` (text: index) | Same text → same pointer; distinct texts → distinct pointers |
| Shared-node handling | Second insert on same text adds chapter to comma-separated `Chap` |
| Chapter index (`chap:`) | Written on insert; multi-token `Chap` → one entry per token |
| `DeleteChapter` — exclusive nodes | Removes nodes, their `text:` and `chap:` index entries |
| `DeleteChapter` — shared nodes | Preserves shared nodes; updates `Chap` field and index |
| `DeleteChapter` — link cleanup | Removes dangling links in survivors; keeps survivor-to-survivor links |
| `SearchNodesByName` | Exact, substring, no-match, and limit cases |
| End-to-end re-submit | `delete + re-insert` produces the correct final state with no orphans |

### Benchmarks

```bash
cd tests
go test -run=NONE -bench=Benchmark -benchmem ./...
```

To compare with the PostgreSQL baseline, build and run the companion tool:

```bash
cd ../SStorytime-main/src
go build -o pg_benchmark pg_benchmark.go
./pg_benchmark          # requires a running PostgreSQL instance
```

### Run only the correctness tests

```bash
go test -v -run "TestIterateNodes|TestGetDB|TestAddLink|TestGetFwd|TestGetEntire|TestGetConstraint" ./...
```

### Run the MobyDick integration test

```bash
go test -v -run TestStressMobyDick -timeout 5m ./...
```

This builds the `N4L` binary, ingests `examples/MobyDickNotes.n4l` (32 K lines,
~9 650 nodes), and then runs `GetDBSingletonBySTType` and `GetConstrainedFwdLinks`
against the resulting BadgerDB.

---

## Chapter Management: BadgerDB vs PostgreSQL

The table below compares the chapter-management features of the two backends.

| Feature | PostgreSQL (SStorytime-main) | BadgerDB (SStorytime-KV) |
|---------|------------------------------|--------------------------|
| **Chapter tracking** | `Chap` text column, comma-separated | `Chap` field + `chap:<chapter>:<class>:<cptr>` index key |
| **Find nodes in chapter** | `WHERE Chap LIKE '%chapter%'` (full-table scan unless indexed) | Prefix scan on `chap:<chapter>:` — O(log n) |
| **Idempotent insert** | `IF NOT EXISTS` check in PL/pgSQL stored procedure | `text:<lowercase>` index key — O(log n) lookup before insert |
| **Shared-node detection** | `array_length(string_to_array(Chap,','),1) > 1` | `len(splitChap(n.Chap)) > 1` in Go |
| **DeleteChapter implementation** | ~100-line PL/pgSQL string built at runtime, sent to server | Pure Go `DeleteChapter()` method — type-safe, debuggable, testable |
| **Atomicity** | Postgres transaction (network round-trips per UPDATE) | Single `WriteBatch` — all deletes and updates in one local flush |
| **Link cleanup** | Per-channel `UPDATE Node SET <col> = array_remove(…)` | In-memory filter over `n.I[st]` slices, written back in batch |
| **Text search** | `WHERE lower(s) LIKE lower('%query%')` | Scan `text:` index keys — substring match on sorted key space |
| **List all chapters** | `SELECT DISTINCT Chap FROM Node` (requires parsing comma lists) | Prefix scan on `chap:` — one entry per (chapter, node) pair |

### Why BadgerDB can be better

1. **No stored procedures** — the Postgres implementation builds a multi-hundred-line
   PL/pgSQL string at runtime and ships it to the server for re-parsing on every call.
   The BadgerDB version is plain Go: fully typed, easy to test, and debuggable with
   standard Go tooling.

2. **Single-transaction delete** — `DeleteChapter` reads all affected nodes, filters
   their link arrays in memory, and flushes everything in one `WriteBatch` call.
   No row-level locks, no multiple round-trips.

3. **O(log n) chapter lookup** — the `chap:` secondary index turns a full-table LIKE
   scan into a B-tree prefix scan.  For a 10 000-node graph with 50 chapters this is
   roughly 3–4 I/O operations vs ~10 000 row reads.

4. **Idempotent insert with stable pointers** — the `text:` index lets `AddNode`
   find and reuse an existing NodePtr in O(log n).  In Postgres the stored procedure
   does a `SELECT … WHERE lower(s) = lower(?)` against the full Node table.

---

## Performance: BadgerDB vs PostgreSQL

The table below compares the two backends on identical operations, measured on
an Intel Core Ultra 5 135U (WSL2, Linux 5.15).  PostgreSQL figures come from
the `pg_benchmark` companion tool (see `SStorytime-main/src/pg_benchmark.go`).

| Operation | PostgreSQL | BadgerDB | Speed-up |
|-----------|-----------|----------|---------|
| Node ingestion | ~5 245 µs/op | ~27 µs/op | **~190×** |
| Node retrieval | ~2 325 µs/op | <1 µs/op | **>2 000×** |
| Context upload/retrieve | ~432 µs/op | <1 µs/op | **>400×** |
| Link creation | ~5 000 µs/op (est.) | ~28 µs/op | **~180×** |
| Cone traversal (BFS) | plpgsql CTE + network | pure Go in-memory | unmeasurable vs ~8 µs |

### MobyDick ingestion estimate

The MobyDick test file (`examples/MobyDickNotes.n4l`) has 32 657 lines
and produces **9 650 nodes**.

| Backend | Estimated ingestion time | Basis |
|---------|--------------------------|-------|
| **BadgerDB** | **~1.45 s** (measured) | `TestStressMobyDick` |
| **PostgreSQL** | **~50–120 s** (estimated) | 9 650 nodes × ~5–12 ms/node |

The PostgreSQL estimate accounts for the cost of each node insertion
(`IdempAppendNode` stored procedure call ≈ 5.2 ms) plus link-append
stored procedures (`array_append` + `NOT … = ANY(…)` idempotency check),
context uploads, and the associated network round-trips.  In practice the
total is dominated by link operations, which can run 2–3× the node cost
when a node has several outgoing arrows.

**Why BadgerDB is faster:**

- **Zero network**: all I/O is local file I/O via memory-mapped files.
- **No SQL parsing**: stored procedures are re-parsed by plpgsql on every call.
- **No row-level locking**: BadgerDB uses optimistic MVCC; PostgreSQL acquires
  row locks for every `UPDATE Node SET … array_append(…)`.
- **In-memory hot path**: cone traversals (`GetFwdConeAsNodes`, `enumeratePaths`)
  read directly from in-memory `NODE_DIRECTORY` slices — zero deserialization cost.

---

## Architecture Notes

See [`CLAUDE.md`](CLAUDE.md) for a detailed description of the module layout,
build system constraints, in-memory vs BadgerDB data paths, and known stubs.

### Key architectural point

The library maintains two parallel data layers:

| Layer | Storage | Used by |
|-------|---------|---------|
| In-memory `NODE_DIRECTORY` | Process RAM | Cone traversals (`GetFwdConeAsNodes`, `enumeratePaths`, …) |
| BadgerDB `node:` keys | Embedded KV file | `IterateNodes`, `GetNode`, `GetDBSingletonBySTType`, `GetConstrainedFwdLinks` |

Links added via `AddLink()` are stored as `edge:` keys in BadgerDB and are
separate from the `I[]` arrays embedded in each Node struct.  Query functions
that use `IterateNodes` read `node.I[]` from the stored struct — so the node
must be stored with its links pre-populated (see `nodeWithLinks()` in
`tests/correctness_test.go`).

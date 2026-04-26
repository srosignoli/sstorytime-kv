# Changelog

All notable changes to SSTorytime-KV are documented here.
The project follows [Semantic Versioning](https://semver.org/).

## [v0.4.0] — Unreleased

### Added — Bleve full-text search overlay

- New exported types under `pkg/SSTorytime`:
  - `Index` — wraps a Bleve index handle. Concurrent-safe for reads;
    writes are serialised by the underlying Bleve store.
  - `IndexBatch` — batched-write accumulator returned by `Index.NewBatch`.
  - `ParseError` — structured error returned by query-grammar parse failures
    (`Pos`, `Message`); satisfies `error` and works with `errors.As`.
- New constructors and methods:
  - `NewBleveIndex(path) (*Index, error)` — create a fresh index. Used by
    `Build graph`.
  - `OpenBleveIndex(path) (*Index, error)` — open an existing index for
    reading. Returns `ErrIndexNotFound` if the directory does not exist,
    `ErrIndexCorrupt` if the contents are unreadable.
  - `(*Index).AddNode(ptr, text, chapter, sourceFile) error` — index a single
    node; replaces prior document on duplicate `NodePtr`.
  - `(*Index).NewBatch() *IndexBatch` and `(*IndexBatch).Add(...)` —
    accumulate writes without holding the index lock.
  - `(*Index).ApplyBatch(*IndexBatch) error` — atomic commit; partial
    progress is not visible per Bleve's transaction guarantees.
  - `(*Index).SearchByQuery(queryText, limit) ([]NodePtr, error)` — parse
    the operator grammar, lower to a Bleve query, return results sorted by
    score-desc with NodePtr-asc tie-break (FR-023a).
  - `(*Index).Close() error` — idempotent.
- New error sentinels: `ErrIndexNotFound`, `ErrIndexCorrupt`, `ErrIndexClosed`,
  `ErrReadOnly`. Wrapped with `%w` so callers can use `errors.Is`.
- Custom analyzers configured for the default index mapping:
  - `text_en_analyzer` — English stemming + lowercase + ASCII-folding.
  - `text_cjk` — built-in CJK bigram tokenisation for Chinese / Japanese /
    Korean text.
  - `text_raw_analyzer` — single-tokenizer + ASCII-folding + lowercase, used
    for the `!exact!` operator.
- Operator grammar (Pratt-style parser): plain tokens, `&` (AND, default-OR
  for whitespace separators), `|` (OR), unary `!` (exact), parentheses,
  `*` wildcard, quoted phrases. Precedence: `|` < `&` < unary `!`.

### Deprecated

- `SearchNodesByName(name string, limit int) []NodePtr` (in `kv_badger.go`).
  Replaced by `(*Index).SearchByQuery`. Emits a one-shot `log.Println`
  deprecation warning the first time it runs in a given process.
- `GetDBNodePtrMatchingNCCS(...)` (in `store_indexes.go`). Replaced by
  `SearchByQuery` plus caller-side chapter / context / arrow / sequence
  filtering. Same one-shot warning.

Both deprecated functions still work in v0.4.0 and produce the same results
as before. They are scheduled for **removal in v0.5.0**.

### Migration notes

- Build-graph callers must now also create the Bleve index alongside
  BadgerDB. The recommended pattern is a two-pass build: Pass 1 batches all
  chapter-root nodes into one `IndexBatch` and commits with `ApplyBatch`;
  Pass 2 issues incremental `AddNode` calls for arrow targets discovered
  during link processing.
- Search callers should treat `ErrIndexNotFound` and `ErrIndexCorrupt` as
  user-visible recoverable states, not fatal errors. The caller should surface
  a "press Build graph (Ctrl+G) to (re)build" prompt and continue.
- `BuildFromCollection` (in consumer code) must clear both `<dir>/sst_data/`
  and `<dir>/bleve/` together at the start of every build so that an
  interrupted prior build cannot contaminate the next one (FR-019).

### Performance

- Benchmark suite: `BenchmarkAddNodeBatch_5k`, `BenchmarkSearchByQuery_Mixed`
  in `bleve_index_bench_test.go`. Run via `go test -bench=. ./pkg/SSTorytime/`.
- 5 000-document index size on disk: ~3 MB on the bench fixture (well within
  SC-009's 50 MB target).
- 5 000-document `ApplyBatch`: ~330 ms on a 14-core x86 laptop with WSL2
  disk; production hardware will vary. Callers exceeding the SC-008
  ≤ 200 ms target should switch to chunked batch flushes (research §R9).

## [v0.3.0] and earlier

See git history.

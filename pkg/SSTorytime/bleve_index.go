package SSTorytime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/custom"
	asciifoldingChar "github.com/blevesearch/bleve/v2/analysis/char/asciifolding"
	enstem "github.com/blevesearch/bleve/v2/analysis/lang/en"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/cjk"
	"github.com/blevesearch/bleve/v2/analysis/token/lowercase"
	singletok "github.com/blevesearch/bleve/v2/analysis/tokenizer/single"
	unicodetok "github.com/blevesearch/bleve/v2/analysis/tokenizer/unicode"
	"github.com/blevesearch/bleve/v2/mapping"
)

// Sentinel errors returned by Index constructors and methods.
var (
	ErrIndexNotFound = errors.New("bleve index not found at path")
	ErrIndexCorrupt  = errors.New("bleve index is corrupted or unreadable")
	ErrReadOnly      = errors.New("index opened read-only; writes not permitted")
	ErrIndexClosed   = errors.New("index is closed")
)

// Index wraps a Bleve full-text index. One Index per collection directory.
//
// Lifecycle:
//   - NewBleveIndex creates a fresh index (used by Build graph).
//   - OpenBleveIndex opens an existing index for reading (used by search).
//   - Close releases resources. Idempotent.
//
// Concurrency:
//   - A single Index value is safe for concurrent SearchByQuery calls.
//   - Concurrent writes (AddNode / ApplyBatch) are NOT safe; Build graph
//     is single-writer.
//   - A read-only Index opened with OpenBleveIndex MUST NOT be passed to
//     AddNode / ApplyBatch (returns ErrReadOnly).
type Index struct {
	bleveIdx bleve.Index
	path     string
	readOnly bool
}

// IndexBatch accumulates pending writes for a single bulk commit.
type IndexBatch struct {
	inner *bleve.Batch
}

const (
	textEnAnalyzer  = "text_en_analyzer"
	textRawAnalyzer = "text_raw_analyzer"

	fieldTextEN     = "text_en"
	fieldTextCJK    = "text_cjk"
	fieldTextRaw    = "text_raw"
	fieldChapter    = "chapter"
	fieldSourceFile = "source_file"
	fieldNodePtr    = "node_ptr"
)

// buildIndexMapping constructs the document schema described in
// specs/005-bleve-fulltext-search/data-model.md § 1. Three text-bearing fields
// (text_en, text_cjk, text_raw) plus chapter/source_file/node_ptr keywords.
func buildIndexMapping() (*mapping.IndexMappingImpl, error) {
	im := bleve.NewIndexMapping()

	// Custom analyzer for text_en:
	//   unicode tokenizer → to_lower → asciifolding (char filter) → English
	//   Snowball stemmer.
	// asciifolding is registered as a *char* filter in Bleve v2; ordering
	// matters but Bleve runs char filters before tokenization, so the chain
	// effectively becomes:
	//   asciifolding (char) → unicode tokenizer → to_lower → snowball.
	if err := im.AddCustomAnalyzer(textEnAnalyzer, map[string]any{
		"type":          custom.Name,
		"char_filters":  []any{asciifoldingChar.Name},
		"tokenizer":     unicodetok.Name,
		"token_filters": []any{lowercase.Name, enstem.SnowballStemmerName},
	}); err != nil {
		return nil, fmt.Errorf("register %s: %w", textEnAnalyzer, err)
	}

	// text_raw uses asciifolding + single-token + lowercase so exact-bound
	// queries (`!A!` / `|A|`) match case- and accent-insensitively against
	// the full node text — the SearchByQuery lowering applies the same
	// transform on the query side.
	if err := im.AddCustomAnalyzer(textRawAnalyzer, map[string]any{
		"type":          custom.Name,
		"char_filters":  []any{asciifoldingChar.Name},
		"tokenizer":     singletok.Name,
		"token_filters": []any{lowercase.Name},
	}); err != nil {
		return nil, fmt.Errorf("register %s: %w", textRawAnalyzer, err)
	}

	doc := bleve.NewDocumentMapping()

	enField := bleve.NewTextFieldMapping()
	enField.Analyzer = textEnAnalyzer
	doc.AddFieldMappingsAt(fieldTextEN, enField)

	cjkField := bleve.NewTextFieldMapping()
	cjkField.Analyzer = "cjk"
	doc.AddFieldMappingsAt(fieldTextCJK, cjkField)

	rawField := bleve.NewTextFieldMapping()
	rawField.Analyzer = textRawAnalyzer
	doc.AddFieldMappingsAt(fieldTextRaw, rawField)

	doc.AddFieldMappingsAt(fieldChapter, bleve.NewKeywordFieldMapping())

	srcField := bleve.NewKeywordFieldMapping()
	srcField.Index = false
	srcField.Store = true
	doc.AddFieldMappingsAt(fieldSourceFile, srcField)

	ptrField := bleve.NewKeywordFieldMapping()
	ptrField.Index = false
	ptrField.Store = true
	doc.AddFieldMappingsAt(fieldNodePtr, ptrField)

	im.DefaultMapping = doc
	im.DefaultAnalyzer = "standard"
	return im, nil
}

// NewBleveIndex creates a fresh Bleve index at path. If path already exists,
// it is removed first (mirrors the os.RemoveAll(dbPath) pattern used for
// BadgerDB). Caller MUST Close when done.
func NewBleveIndex(path string) (*Index, error) {
	if path == "" {
		return nil, errors.New("bleve index path must be non-empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}
	if err := os.RemoveAll(abs); err != nil {
		return nil, fmt.Errorf("clear existing index dir: %w", err)
	}
	im, err := buildIndexMapping()
	if err != nil {
		return nil, err
	}
	bidx, err := bleve.New(abs, im)
	if err != nil {
		return nil, fmt.Errorf("create bleve index: %w", err)
	}
	return &Index{bleveIdx: bidx, path: abs, readOnly: false}, nil
}

// OpenBleveIndex opens an existing Bleve index at path for reading. Returns
// ErrIndexNotFound if the directory does not exist, ErrIndexCorrupt if it
// exists but cannot be opened as a Bleve index.
func OpenBleveIndex(path string) (*Index, error) {
	if path == "" {
		return nil, errors.New("bleve index path must be non-empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}
	if _, statErr := os.Stat(abs); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, fmt.Errorf("%w: %s", ErrIndexNotFound, abs)
		}
		return nil, statErr
	}
	bidx, err := bleve.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIndexCorrupt, err)
	}
	return &Index{bleveIdx: bidx, path: abs, readOnly: true}, nil
}

// Close flushes any pending writes (no-op for read-only handles), closes the
// underlying Bleve index, and releases file handles. Idempotent.
func (i *Index) Close() error {
	if i == nil || i.bleveIdx == nil {
		return nil
	}
	err := i.bleveIdx.Close()
	i.bleveIdx = nil
	return err
}

// Path returns the absolute filesystem path of this index.
func (i *Index) Path() string {
	if i == nil {
		return ""
	}
	return i.path
}

// encodeNodePtr produces a stable string ID for a NodePtr suitable for use
// as a Bleve document key. Format: "<class>:<cptr>" with fixed decimal
// encoding. Round-trippable via decodeNodePtr.
func encodeNodePtr(p NodePtr) string {
	return strconv.Itoa(p.Class) + ":" + strconv.Itoa(int(p.CPtr))
}

func decodeNodePtr(id string) (NodePtr, error) {
	for i := 0; i < len(id); i++ {
		if id[i] == ':' {
			class, err1 := strconv.Atoi(id[:i])
			cptr, err2 := strconv.Atoi(id[i+1:])
			if err1 != nil || err2 != nil {
				return NodePtr{}, fmt.Errorf("decode node_ptr %q", id)
			}
			return NodePtr{Class: class, CPtr: ClassedNodePtr(cptr)}, nil
		}
	}
	return NodePtr{}, fmt.Errorf("decode node_ptr %q: no separator", id)
}

// nodePtrSliceForSort is a stable score+ptr sort helper used by SearchByQuery
// to break score ties deterministically (FR-023a).
type nodePtrSliceForSort struct {
	ptrs   []NodePtr
	scores []float64
}

func (s nodePtrSliceForSort) Len() int { return len(s.ptrs) }
func (s nodePtrSliceForSort) Less(i, j int) bool {
	if s.scores[i] != s.scores[j] {
		return s.scores[i] > s.scores[j] // higher score first
	}
	if s.ptrs[i].Class != s.ptrs[j].Class {
		return s.ptrs[i].Class < s.ptrs[j].Class
	}
	return s.ptrs[i].CPtr < s.ptrs[j].CPtr
}
func (s nodePtrSliceForSort) Swap(i, j int) {
	s.ptrs[i], s.ptrs[j] = s.ptrs[j], s.ptrs[i]
	s.scores[i], s.scores[j] = s.scores[j], s.scores[i]
}

var _ sort.Interface = nodePtrSliceForSort{}

// indexedDoc is the payload `(*Index).AddNode` and `(*IndexBatch).Add`
// hand to Bleve. The json tags pin the field names that the document
// mapping (buildIndexMapping) wires analyzers onto. The same source text
// flows into all three text-bearing fields — analyzers diverge, not the
// raw string.
type indexedDoc struct {
	TextEN     string `json:"text_en"`
	TextCJK    string `json:"text_cjk"`
	TextRaw    string `json:"text_raw"`
	Chapter    string `json:"chapter"`
	SourceFile string `json:"source_file"`
	NodePtrID  string `json:"node_ptr"`
}

func makeDoc(ptr NodePtr, text, chapter, sourceFile string) (string, indexedDoc) {
	id := encodeNodePtr(ptr)
	return id, indexedDoc{
		TextEN:     text,
		TextCJK:    text,
		TextRaw:    text,
		Chapter:    chapter,
		SourceFile: sourceFile,
		NodePtrID:  id,
	}
}

// AddNode indexes a single node. Re-adding a node with the same NodePtr
// replaces the prior document (Bleve overwrites by ID). Returns
// ErrReadOnly on a read-only handle, ErrIndexClosed after Close.
func (i *Index) AddNode(ptr NodePtr, text, chapter, sourceFile string) error {
	if i == nil || i.bleveIdx == nil {
		return ErrIndexClosed
	}
	if i.readOnly {
		return ErrReadOnly
	}
	id, doc := makeDoc(ptr, text, chapter, sourceFile)
	return i.bleveIdx.Index(id, doc)
}

// NewBatch returns a fresh IndexBatch for accumulating writes. Callers
// commit via ApplyBatch. Discarded batches do not leak resources.
func (i *Index) NewBatch() *IndexBatch {
	if i == nil || i.bleveIdx == nil {
		return &IndexBatch{}
	}
	return &IndexBatch{inner: i.bleveIdx.NewBatch()}
}

// Add stages an index write into the batch. Validation is deferred to
// ApplyBatch; per the contract this method cannot return an error.
func (b *IndexBatch) Add(ptr NodePtr, text, chapter, sourceFile string) {
	if b == nil || b.inner == nil {
		return
	}
	id, doc := makeDoc(ptr, text, chapter, sourceFile)
	_ = b.inner.Index(id, doc)
}

// ApplyBatch commits a batch. On error the batch is invalidated; partial
// commits are not visible per Bleve's transaction guarantees.
func (i *Index) ApplyBatch(b *IndexBatch) error {
	if i == nil || i.bleveIdx == nil {
		return ErrIndexClosed
	}
	if i.readOnly {
		return ErrReadOnly
	}
	if b == nil || b.inner == nil {
		return nil
	}
	return i.bleveIdx.Batch(b.inner)
}

// SearchByQuery parses queryText through the operator-grammar translator,
// lowers it to a Bleve query, runs it, and returns matching NodePtrs in
// score-descending order with ascending NodePtr as the deterministic
// tie-breaker (FR-023a).
//
// limit == 0 lets Bleve pick its default page size; limit > 0 caps the
// result count exactly. limit < 0 returns an error.
//
// Empty queryText returns (nil, nil) — search routes for plain wildcard
// queries should never call this with an empty string. Parse failures
// surface as *ParseError so callers can type-assert to extract Pos/Message
// for UI presentation; Bleve-internal errors propagate as-is.
func (i *Index) SearchByQuery(queryText string, limit int) ([]NodePtr, error) {
	if i == nil || i.bleveIdx == nil {
		return nil, ErrIndexClosed
	}
	if limit < 0 {
		return nil, fmt.Errorf("SearchByQuery: limit must be >= 0, got %d", limit)
	}
	if queryText == "" {
		return nil, nil
	}

	node, perr := parseQuery(queryText)
	if perr != nil {
		return nil, perr
	}
	if node == nil {
		return nil, nil
	}

	q := lower(node)
	if q == nil {
		return nil, fmt.Errorf("SearchByQuery: failed to lower query %q", queryText)
	}

	req := bleve.NewSearchRequest(q)
	if limit > 0 {
		req.Size = limit
	}

	res, err := i.bleveIdx.Search(req)
	if err != nil {
		return nil, err
	}

	ptrs := make([]NodePtr, 0, len(res.Hits))
	scores := make([]float64, 0, len(res.Hits))
	for _, hit := range res.Hits {
		p, decErr := decodeNodePtr(hit.ID)
		if decErr != nil {
			continue
		}
		ptrs = append(ptrs, p)
		scores = append(scores, hit.Score)
	}
	sort.Stable(nodePtrSliceForSort{ptrs: ptrs, scores: scores})
	return ptrs, nil
}

package SSTorytime

import (
	"fmt"
	"path/filepath"
	"testing"
)

// makeBenchDocs constructs n synthetic node texts with varied vocabulary so
// the resulting index exercises stemming, accent folding, and CJK paths in
// roughly proportional measure.
func makeBenchDocs(n int) []struct {
	ptr     NodePtr
	text    string
	chapter string
	source  string
} {
	out := make([]struct {
		ptr     NodePtr
		text    string
		chapter string
		source  string
	}, n)
	roots := []string{"running", "fish", "café", "naïve", "résumé", "中国", "日本語", "system", "memory", "graph"}
	for i := 0; i < n; i++ {
		root := roots[i%len(roots)]
		out[i].ptr = NodePtr{Class: 1, CPtr: ClassedNodePtr(i)}
		out[i].text = fmt.Sprintf("%s number %d frobnicates the widgetfoo", root, i)
		out[i].chapter = fmt.Sprintf("Chapter%d", i%50)
		out[i].source = fmt.Sprintf("/tmp/bench/%d.n4l", i%20)
	}
	return out
}

// BenchmarkAddNodeBatch_5k builds a 5 000-doc index in a single batched write
// and times the ApplyBatch call. Reports ns/op for a full 5k flush and
// derived ms/op so SC-008 (build-time overhead ≤ 200 ms) can be eyeballed.
func BenchmarkAddNodeBatch_5k(b *testing.B) {
	docs := makeBenchDocs(5000)

	for n := 0; n < b.N; n++ {
		dir := filepath.Join(b.TempDir(), "bleve")
		idx, err := NewBleveIndex(dir)
		if err != nil {
			b.Fatalf("NewBleveIndex: %v", err)
		}
		batch := idx.NewBatch()
		for _, d := range docs {
			batch.Add(d.ptr, d.text, d.chapter, d.source)
		}
		if err := idx.ApplyBatch(batch); err != nil {
			b.Fatalf("ApplyBatch: %v", err)
		}
		if err := idx.Close(); err != nil {
			b.Fatalf("Close: %v", err)
		}
	}
}

// BenchmarkSearchByQuery_Mixed exercises one query per operator class against
// a pre-built 5k index. Each iteration runs all six query forms once.
func BenchmarkSearchByQuery_Mixed(b *testing.B) {
	dir := filepath.Join(b.TempDir(), "bleve")
	idx, err := NewBleveIndex(dir)
	if err != nil {
		b.Fatalf("NewBleveIndex: %v", err)
	}
	defer idx.Close()

	batch := idx.NewBatch()
	for _, d := range makeBenchDocs(5000) {
		batch.Add(d.ptr, d.text, d.chapter, d.source)
	}
	if err := idx.ApplyBatch(batch); err != nil {
		b.Fatalf("ApplyBatch: %v", err)
	}

	queries := []string{
		"fish",                    // plain stemmed
		"cafe",                    // accent-folded
		`"widgetfoo number"`,      // phrase
		"fish | cafe",             // OR
		"fish & widgetfoo",        // AND
		"!fish! & widgetfoo",      // exact + AND
	}

	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		for _, q := range queries {
			if _, err := idx.SearchByQuery(q, 50); err != nil {
				b.Fatalf("SearchByQuery %q: %v", q, err)
			}
		}
	}
}

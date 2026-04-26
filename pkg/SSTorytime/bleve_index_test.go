package SSTorytime

import (
	"path/filepath"
	"testing"
)

// TestBleveIndex_OpenMissing verifies the ErrIndexNotFound sentinel for a
// missing-directory open.
func TestBleveIndex_OpenMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does_not_exist")
	if _, err := OpenBleveIndex(dir); err == nil {
		t.Fatalf("expected error opening missing index dir, got nil")
	}
}

// TestBleveIndex_NewAndClose covers the happy path: create, close, reopen.
func TestBleveIndex_NewAndClose(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bleve")
	idx, err := NewBleveIndex(dir)
	if err != nil {
		t.Fatalf("NewBleveIndex: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Idempotent.
	if err := idx.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// Reopen as read-only.
	ro, err := OpenBleveIndex(dir)
	if err != nil {
		t.Fatalf("OpenBleveIndex: %v", err)
	}
	if !ro.readOnly {
		t.Fatalf("expected read-only flag")
	}
	_ = ro.Close()
}

// indexFixture builds a fresh write-mode Index in t.TempDir, indexes the
// given (text, chapter) docs as NodePtr{Class: 1, CPtr: i+1}, then returns
// the index plus a cleanup that the caller does not need to defer (t.Cleanup
// already wired). Used by all SearchByQuery tests.
func indexFixture(t *testing.T, docs []struct{ Text, Chapter string }) *Index {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bleve")
	idx, err := NewBleveIndex(dir)
	if err != nil {
		t.Fatalf("NewBleveIndex: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	batch := idx.NewBatch()
	for i, d := range docs {
		batch.Add(NodePtr{Class: 1, CPtr: ClassedNodePtr(i + 1)}, d.Text, d.Chapter, "")
	}
	if err := idx.ApplyBatch(batch); err != nil {
		t.Fatalf("ApplyBatch: %v", err)
	}
	return idx
}

func nodePtrSet(ptrs []NodePtr) map[NodePtr]bool {
	out := make(map[NodePtr]bool, len(ptrs))
	for _, p := range ptrs {
		out[p] = true
	}
	return out
}

// TestSearchByQuery_BareToken_Stemming verifies the English Snowball stemmer
// produces a shared root for run/runs/running. `runner` is intentionally
// included as a negative control: both Porter and Snowball treat it as a
// distinct agent-noun ("a person who runs") and do NOT reduce it to "run",
// so it must NOT match. This matches US1 acceptance scenario 1, which only
// requires running and runs to match.
func TestSearchByQuery_BareToken_Stemming(t *testing.T) {
	idx := indexFixture(t, []struct{ Text, Chapter string }{
		{"I was running in the park", "ch1"},
		{"he runs every morning", "ch1"},
		{"the runner finished", "ch1"},
		{"unrelated text", "ch1"},
	})
	got, err := idx.SearchByQuery("run", 10)
	if err != nil {
		t.Fatalf("SearchByQuery: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d hits; want 2 (running, runs). got=%v", len(got), got)
	}
	hits := nodePtrSet(got)
	for i := 1; i <= 2; i++ {
		want := NodePtr{Class: 1, CPtr: ClassedNodePtr(i)}
		if !hits[want] {
			t.Errorf("missing expected hit %v", want)
		}
	}
	if hits[(NodePtr{Class: 1, CPtr: 3})] {
		t.Errorf("runner (doc 3) should NOT stem to 'run' — agent-noun preserved by stemmer")
	}
	if hits[(NodePtr{Class: 1, CPtr: 4})] {
		t.Errorf("unrelated doc 4 should not match")
	}
}

// TestSearchByQuery_CJK_Bigram verifies the CJK analyzer bigram-tokenizes
// Han characters so 房子 indexes as the bigram "房子" and the query matches.
// The pure-English doc must be excluded.
func TestSearchByQuery_CJK_Bigram(t *testing.T) {
	idx := indexFixture(t, []struct{ Text, Chapter string }{
		{"房子 means house", "cn"},
		{"pure english text", "en"},
	})
	got, err := idx.SearchByQuery("房子", 10)
	if err != nil {
		t.Fatalf("SearchByQuery: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d hits; want 1. got=%v", len(got), got)
	}
	want := NodePtr{Class: 1, CPtr: 1}
	if got[0] != want {
		t.Fatalf("got[0] = %v; want %v", got[0], want)
	}
}

// TestSearchByQuery_BoolAndNot_US2 covers acceptance scenario 1: brain&!notes
// returns nodes that contain "brain" but not "notes".
func TestSearchByQuery_BoolAndNot_US2(t *testing.T) {
	idx := indexFixture(t, []struct{ Text, Chapter string }{
		{"brain research", "ch1"},
		{"brain notes", "ch1"},
		{"notes about music", "ch1"},
	})
	got, err := idx.SearchByQuery("brain&!notes", 10)
	if err != nil {
		t.Fatalf("SearchByQuery: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d hits; want 1 (brain research). got=%v", len(got), got)
	}
	want := NodePtr{Class: 1, CPtr: 1}
	if got[0] != want {
		t.Fatalf("got[0] = %v; want %v", got[0], want)
	}
}

// TestSearchByQuery_Phrase_US2 covers acceptance scenario 2: a quoted phrase
// matches the exact ordered sequence, not the bag of its words.
func TestSearchByQuery_Phrase_US2(t *testing.T) {
	idx := indexFixture(t, []struct{ Text, Chapter string }{
		{"fish soup recipe", "ch1"},
		{"fish curry and soup", "ch1"},
	})
	got, err := idx.SearchByQuery(`"fish soup"`, 10)
	if err != nil {
		t.Fatalf("SearchByQuery: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d hits; want 1 (fish soup recipe). got=%v", len(got), got)
	}
	want := NodePtr{Class: 1, CPtr: 1}
	if got[0] != want {
		t.Fatalf("got[0] = %v; want %v", got[0], want)
	}
}

// TestSearchByQuery_ProximityAdjacent_US2 covers acceptance scenario 3:
// strange<->kind matches when the two words are adjacent (slop=0).
func TestSearchByQuery_ProximityAdjacent_US2(t *testing.T) {
	idx := indexFixture(t, []struct{ Text, Chapter string }{
		{"she had a strange kind demeanour", "ch1"},
		{"strange events and kind people", "ch1"},
	})
	got, err := idx.SearchByQuery("strange<->kind", 10)
	if err != nil {
		t.Fatalf("SearchByQuery: %v", err)
	}
	if len(got) < 1 {
		t.Fatalf("got %d hits; want at least 1 (adjacent strange kind). got=%v", len(got), got)
	}
	hits := nodePtrSet(got)
	if !hits[(NodePtr{Class: 1, CPtr: 1})] {
		t.Fatalf("expected adjacent doc 1 in hits; got %v", got)
	}
}

// TestSearchByQuery_ProximitySlop_US2 covers acceptance scenario 4:
// strange<2>woman matches when up to 2 words separate the two terms.
func TestSearchByQuery_ProximitySlop_US2(t *testing.T) {
	idx := indexFixture(t, []struct{ Text, Chapter string }{
		{"a strange but kind woman", "ch1"},
		{"unrelated text here", "ch1"},
	})
	got, err := idx.SearchByQuery("strange<2>woman", 10)
	if err != nil {
		t.Fatalf("SearchByQuery: %v", err)
	}
	if len(got) < 1 {
		t.Fatalf("got %d hits; want at least 1 (slop-2 match). got=%v", len(got), got)
	}
	hits := nodePtrSet(got)
	if !hits[(NodePtr{Class: 1, CPtr: 1})] {
		t.Fatalf("expected slop-2 doc 1 in hits; got %v", got)
	}
	if hits[(NodePtr{Class: 1, CPtr: 2})] {
		t.Errorf("unrelated doc 2 should not match")
	}
}

// TestSearchByQuery_Prefix_US2 covers acceptance scenario 5: flo:* matches
// flower/floor/flora and excludes frog.
func TestSearchByQuery_Prefix_US2(t *testing.T) {
	idx := indexFixture(t, []struct{ Text, Chapter string }{
		{"flower", "ch1"},
		{"floor", "ch1"},
		{"flora", "ch1"},
		{"frog", "ch1"},
	})
	got, err := idx.SearchByQuery("flo:*", 10)
	if err != nil {
		t.Fatalf("SearchByQuery: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d hits; want 3 (flower, floor, flora). got=%v", len(got), got)
	}
	hits := nodePtrSet(got)
	for i := 1; i <= 3; i++ {
		want := NodePtr{Class: 1, CPtr: ClassedNodePtr(i)}
		if !hits[want] {
			t.Errorf("missing expected hit %v", want)
		}
	}
	if hits[(NodePtr{Class: 1, CPtr: 4})] {
		t.Errorf("frog (doc 4) should not match prefix flo:*")
	}
}

// TestSearchByQuery_ExactBang_US2 covers acceptance scenarios 6 and 7:
// !A! matches the exact text "A" (case-insensitive on text_raw) and not
// "Apple" or "Banana".
func TestSearchByQuery_ExactBang_US2(t *testing.T) {
	idx := indexFixture(t, []struct{ Text, Chapter string }{
		{"A", "ch1"},
		{"a", "ch1"},
		{"Apple", "ch1"},
		{"Banana", "ch1"},
	})
	got, err := idx.SearchByQuery("!A!", 10)
	if err != nil {
		t.Fatalf("SearchByQuery: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d hits; want 2 (A and a, case-insensitive exact). got=%v", len(got), got)
	}
	hits := nodePtrSet(got)
	for i := 1; i <= 2; i++ {
		want := NodePtr{Class: 1, CPtr: ClassedNodePtr(i)}
		if !hits[want] {
			t.Errorf("missing expected hit %v", want)
		}
	}
	if hits[(NodePtr{Class: 1, CPtr: 3})] || hits[(NodePtr{Class: 1, CPtr: 4})] {
		t.Errorf("Apple/Banana should not match !A!")
	}
}

// TestSearchByQuery_DisjunctionExplicit_US2 covers acceptance scenario 8:
// `word1 | word2` returns docs containing either or both terms.
func TestSearchByQuery_DisjunctionExplicit_US2(t *testing.T) {
	idx := indexFixture(t, []struct{ Text, Chapter string }{
		{"contains word1 only here", "ch1"},
		{"contains word2 only here", "ch1"},
		{"both word1 and word2 appear here", "ch1"},
		{"unrelated text", "ch1"},
	})
	got, err := idx.SearchByQuery("word1 | word2", 10)
	if err != nil {
		t.Fatalf("SearchByQuery: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d hits; want 3 (word1, word2, both). got=%v", len(got), got)
	}
	hits := nodePtrSet(got)
	for i := 1; i <= 3; i++ {
		want := NodePtr{Class: 1, CPtr: ClassedNodePtr(i)}
		if !hits[want] {
			t.Errorf("missing expected hit %v", want)
		}
	}
	if hits[(NodePtr{Class: 1, CPtr: 4})] {
		t.Errorf("unrelated doc 4 should not match")
	}
}

// TestSearchByQuery_AccentFold verifies the asciifolding char filter folds
// diacritics so a query for `(fangzi)` matches both `fángzi` and `Fangzi`,
// and excludes unrelated text.
func TestSearchByQuery_AccentFold(t *testing.T) {
	idx := indexFixture(t, []struct{ Text, Chapter string }{
		{"fángzi", "cn"},
		{"Fangzi", "cn"},
		{"unrelated", "cn"},
	})
	got, err := idx.SearchByQuery("(fangzi)", 10)
	if err != nil {
		t.Fatalf("SearchByQuery: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d hits; want 2. got=%v", len(got), got)
	}
	hits := nodePtrSet(got)
	if !hits[(NodePtr{Class: 1, CPtr: 1})] || !hits[(NodePtr{Class: 1, CPtr: 2})] {
		t.Fatalf("expected fold-equivalent docs 1 and 2; got %v", got)
	}
	if hits[(NodePtr{Class: 1, CPtr: 3})] {
		t.Errorf("unrelated doc 3 should not match")
	}
}

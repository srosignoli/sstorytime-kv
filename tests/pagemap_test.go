package tests

// pagemap_test.go — unit tests for SavePageMap / GetPageMap, the per-line
// notes-replay feature added in v0.3.0.
//
// Test matrix:
//
//   Round-trip & ordering
//     TestPageMap_RoundTrip             – Save then Get returns the same rows
//     TestPageMap_OrderedByLine         – rows come back sorted by (chap, line)
//     TestPageMap_WithinLineSeq         – duplicate (chap,line) rows coexist, each stored
//
//   Chapter filter
//     TestPageMap_ChapterFilter         – chap="brain" only returns that chapter
//     TestPageMap_ChapterAll            – chap="" returns every chapter
//
//   Context filter
//     TestPageMap_ContextFilter_Match   – cn=["waves"] returns matching events
//     TestPageMap_ContextFilter_NoMatch – cn that matches nothing returns empty
//     TestPageMap_ContextFilter_Empty   – empty cn returns all events
//
//   DeleteChapter integration
//     TestPageMap_DeleteChapterClears   – DeleteChapter removes pm:<chap>: rows
//
//   JSON survival
//     TestPageMap_PathLinksSurvive      – Path []Link roundtrips all fields

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	SST "github.com/srosignoli/sstorytime-kv/pkg/SSTorytime"
)

func newPageMap(chap string, line int, ctx SST.ContextPtr, path []SST.Link) SST.PageMap {
	return SST.PageMap{
		Chapter: chap,
		Line:    line,
		Context: ctx,
		Path:    path,
	}
}

func linePath(dst SST.NodePtr, steps ...SST.Link) []SST.Link {
	out := make([]SST.Link, 0, 1+len(steps))
	out = append(out, SST.Link{Dst: dst})
	out = append(out, steps...)
	return out
}

func pageMapLines(rows []SST.PageMap) []int {
	out := make([]int, len(rows))
	for i, r := range rows {
		out[i] = r.Line
	}
	return out
}

func pageMapChapters(rows []SST.PageMap) []string {
	seen := make(map[string]bool)
	for _, r := range rows {
		seen[r.Chapter] = true
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func setupPageMap(t *testing.T) (SST.PoSST, func()) {
	t.Helper()
	dir := fmt.Sprintf("pm_test_db_%s", strings.ReplaceAll(t.Name(), "/", "_"))
	os.RemoveAll(dir)

	kv, err := SST.OpenBadgerStore(dir)
	if err != nil {
		t.Fatalf("OpenBadgerStore: %v", err)
	}

	SST.NO_NODE_PTR.Class = 0
	SST.NO_NODE_PTR.CPtr = -1
	SST.NONODE.Class = 0
	SST.NONODE.CPtr = 0
	SST.NODE_DIRECTORY = SST.NodeDirectory{}
	SST.MemoryInit()

	var sst SST.PoSST
	sst.KV = kv

	return sst, func() {
		kv.Close()
		os.RemoveAll(dir)
	}
}

// ── Round-trip & ordering ────────────────────────────────────────────────────

func TestPageMap_RoundTrip(t *testing.T) {
	sst, teardown := setupPageMap(t)
	defer teardown()

	alpha := insertNode(sst, "alpha", "chap_a")
	beta := insertNode(sst, "beta", "chap_a")

	ev := newPageMap("chap_a", 3, 0, linePath(alpha,
		SST.Link{Arr: 7, Dst: beta, Wgt: 1.0},
	))
	if err := sst.KV.SavePageMap(ev); err != nil {
		t.Fatalf("SavePageMap: %v", err)
	}

	rows := sst.KV.GetPageMap("chap_a", nil, 1)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	got := rows[0]
	if got.Chapter != "chap_a" || got.Line != 3 {
		t.Errorf("roundtrip mismatch: chap=%q line=%d", got.Chapter, got.Line)
	}
	if len(got.Path) != 2 {
		t.Fatalf("expected 2-step path, got %d", len(got.Path))
	}
	if got.Path[0].Dst != alpha {
		t.Errorf("path[0].Dst want %v got %v", alpha, got.Path[0].Dst)
	}
	if got.Path[1].Arr != 7 || got.Path[1].Dst != beta {
		t.Errorf("path[1] want Arr=7 Dst=%v, got Arr=%v Dst=%v", beta, got.Path[1].Arr, got.Path[1].Dst)
	}
}

func TestPageMap_OrderedByLine(t *testing.T) {
	sst, teardown := setupPageMap(t)
	defer teardown()

	a := insertNode(sst, "a", "chap_a")

	// Insert out of order to verify the backend sorts on read.
	for _, ln := range []int{42, 3, 17, 1, 2500} {
		ev := newPageMap("chap_a", ln, 0, linePath(a))
		if err := sst.KV.SavePageMap(ev); err != nil {
			t.Fatalf("SavePageMap: %v", err)
		}
	}

	rows := sst.KV.GetPageMap("chap_a", nil, 1)
	got := pageMapLines(rows)
	want := []int{1, 3, 17, 42, 2500}
	if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
		t.Errorf("line order: want %v, got %v", want, got)
	}
}

func TestPageMap_WithinLineSeq(t *testing.T) {
	sst, teardown := setupPageMap(t)
	defer teardown()

	a := insertNode(sst, "a", "chap_a")
	b := insertNode(sst, "b", "chap_a")

	for _, arr := range []SST.ArrowPtr{1, 2, 3} {
		ev := newPageMap("chap_a", 5, 0, linePath(a,
			SST.Link{Arr: arr, Dst: b, Wgt: 1.0},
		))
		if err := sst.KV.SavePageMap(ev); err != nil {
			t.Fatalf("SavePageMap: %v", err)
		}
	}

	rows := sst.KV.GetPageMap("chap_a", nil, 1)
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows for line 5, got %d", len(rows))
	}
	gotArrs := []SST.ArrowPtr{rows[0].Path[1].Arr, rows[1].Path[1].Arr, rows[2].Path[1].Arr}
	sort.Slice(gotArrs, func(i, j int) bool { return gotArrs[i] < gotArrs[j] })
	want := []SST.ArrowPtr{1, 2, 3}
	for i := range want {
		if gotArrs[i] != want[i] {
			t.Errorf("arrow[%d] want %v, got %v", i, want[i], gotArrs[i])
		}
	}
}

// ── Chapter filter ───────────────────────────────────────────────────────────

func TestPageMap_ChapterFilter(t *testing.T) {
	sst, teardown := setupPageMap(t)
	defer teardown()

	a := insertNode(sst, "a", "chap_a")
	b := insertNode(sst, "b", "chap_b")

	_ = sst.KV.SavePageMap(newPageMap("chap_a", 1, 0, linePath(a)))
	_ = sst.KV.SavePageMap(newPageMap("chap_b", 1, 0, linePath(b)))

	rows := sst.KV.GetPageMap("chap_a", nil, 1)
	if chaps := pageMapChapters(rows); len(chaps) != 1 || chaps[0] != "chap_a" {
		t.Errorf("chapter filter want [chap_a], got %v", chaps)
	}
}

func TestPageMap_ChapterAll(t *testing.T) {
	sst, teardown := setupPageMap(t)
	defer teardown()

	a := insertNode(sst, "a", "chap_a")
	b := insertNode(sst, "b", "chap_b")

	_ = sst.KV.SavePageMap(newPageMap("chap_a", 1, 0, linePath(a)))
	_ = sst.KV.SavePageMap(newPageMap("chap_b", 1, 0, linePath(b)))

	rows := sst.KV.GetPageMap("", nil, 1)
	chaps := pageMapChapters(rows)
	if fmt.Sprintf("%v", chaps) != "[chap_a chap_b]" {
		t.Errorf("no-filter want [chap_a chap_b], got %v", chaps)
	}

	rowsAny := sst.KV.GetPageMap("any", nil, 1)
	if len(rowsAny) != 2 {
		t.Errorf("chap=any should return 2 rows, got %d", len(rowsAny))
	}
}

// ── Context filter ───────────────────────────────────────────────────────────

func TestPageMap_ContextFilter_Match(t *testing.T) {
	sst, teardown := setupPageMap(t)
	defer teardown()

	a := insertNode(sst, "a", "chap_a")

	ctxWaves := sst.KV.UploadContext("brain waves, oscillations", 1)
	ctxStruct := sst.KV.UploadContext("structure", 2)

	_ = sst.KV.SavePageMap(newPageMap("chap_a", 1, ctxWaves, linePath(a)))
	_ = sst.KV.SavePageMap(newPageMap("chap_a", 2, ctxStruct, linePath(a)))

	rows := sst.KV.GetPageMap("chap_a", []string{"brain waves"}, 1)
	if len(rows) != 1 || rows[0].Line != 1 {
		t.Errorf("want 1 row on line 1, got %+v", rows)
	}
}

func TestPageMap_ContextFilter_NoMatch(t *testing.T) {
	sst, teardown := setupPageMap(t)
	defer teardown()

	a := insertNode(sst, "a", "chap_a")
	ctx := sst.KV.UploadContext("brain waves", 1)
	_ = sst.KV.SavePageMap(newPageMap("chap_a", 1, ctx, linePath(a)))

	rows := sst.KV.GetPageMap("chap_a", []string{"wholly unrelated"}, 1)
	if len(rows) != 0 {
		t.Errorf("expected 0 rows with non-matching cn, got %d", len(rows))
	}
}

func TestPageMap_ContextFilter_Empty(t *testing.T) {
	sst, teardown := setupPageMap(t)
	defer teardown()

	a := insertNode(sst, "a", "chap_a")
	ctx := sst.KV.UploadContext("anything", 1)
	_ = sst.KV.SavePageMap(newPageMap("chap_a", 1, ctx, linePath(a)))
	_ = sst.KV.SavePageMap(newPageMap("chap_a", 2, 0, linePath(a)))

	rows := sst.KV.GetPageMap("chap_a", nil, 1)
	if len(rows) != 2 {
		t.Errorf("empty cn should return both rows, got %d", len(rows))
	}
}

// ── DeleteChapter integration ────────────────────────────────────────────────

func TestPageMap_DeleteChapterClears(t *testing.T) {
	sst, teardown := setupPageMap(t)
	defer teardown()

	a := insertNode(sst, "a", "chap_a")
	b := insertNode(sst, "b", "chap_b")

	_ = sst.KV.SavePageMap(newPageMap("chap_a", 1, 0, linePath(a)))
	_ = sst.KV.SavePageMap(newPageMap("chap_b", 1, 0, linePath(b)))

	if err := sst.KV.DeleteChapter("chap_a"); err != nil {
		t.Fatalf("DeleteChapter: %v", err)
	}

	rows := sst.KV.GetPageMap("chap_a", nil, 1)
	if len(rows) != 0 {
		t.Errorf("chap_a pm rows should be gone after DeleteChapter, got %d", len(rows))
	}
	rowsB := sst.KV.GetPageMap("chap_b", nil, 1)
	if len(rowsB) != 1 {
		t.Errorf("chap_b pm rows must survive, got %d", len(rowsB))
	}
}

// ── JSON survival ────────────────────────────────────────────────────────────

func TestPageMap_PathLinksSurvive(t *testing.T) {
	sst, teardown := setupPageMap(t)
	defer teardown()

	a := insertNode(sst, "a", "chap_a")
	b := insertNode(sst, "b", "chap_a")
	c := insertNode(sst, "c", "chap_a")

	ev := newPageMap("chap_a", 9, 42, linePath(a,
		SST.Link{Arr: 5, Ctx: 42, Dst: b, Wgt: 1.0},
		SST.Link{Arr: 6, Ctx: 42, Dst: c, Wgt: 0.5},
	))
	if err := sst.KV.SavePageMap(ev); err != nil {
		t.Fatalf("SavePageMap: %v", err)
	}

	rows := sst.KV.GetPageMap("chap_a", nil, 1)
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	got := rows[0]
	if got.Context != 42 {
		t.Errorf("Context: want 42, got %v", got.Context)
	}
	if len(got.Path) != 3 {
		t.Fatalf("Path len: want 3, got %d", len(got.Path))
	}
	if got.Path[1].Arr != 5 || got.Path[1].Ctx != 42 || got.Path[1].Dst != b || got.Path[1].Wgt != 1.0 {
		t.Errorf("path[1] fields lost: %+v", got.Path[1])
	}
	if got.Path[2].Wgt != 0.5 {
		t.Errorf("path[2].Wgt lost: want 0.5, got %v", got.Path[2].Wgt)
	}
}

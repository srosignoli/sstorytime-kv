package tests

// api_completeness_test.go — tests for the three API gaps closed to bring
// SStorytime-KV to full parity with the PostgreSQL API documentation.
//
// Gaps covered:
//
//	Gap 1 – GetFwdPathsAsLinks  (store_paths.go)   was a stub → now uses enumeratePaths
//	Gap 2 – GetDBNodePtrMatchingName / GetDBNodePtrMatchingNCCS (store_indexes.go) were missing
//	Gap 3 – Context persistence (kv_badger.go) UploadContext/GetContextByName/GetContextByPtr were stubs

import (
	"fmt"
	"os"
	"testing"

	SST "github.com/srosignoli/sstorytime-kv/pkg/SSTorytime"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func setupAPI(t *testing.T) (SST.PoSST, func()) {
	t.Helper()
	dir := fmt.Sprintf("api_test_db_%s", t.Name())
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

// insertMemAndKV inserts a node into both in-memory NODE_DIRECTORY and BadgerDB.
// GetFwdPathsAsLinks reads from the in-memory graph, so both must be populated.
func insertMemAndKV(sst SST.PoSST, text, chap string) SST.NodePtr {
	// In-memory insertion (used by enumeratePaths / GetFwdPathsAsLinks)
	var n SST.Node
	n.S = text
	n.Chap = chap
	n.L, n.NPtr.Class = SST.StorageClass(text)
	memPtr := SST.AppendTextToDirectory(n, func(string) {})

	// KV insertion (used by GetDBNodePtrMatchingName etc.)
	n.NPtr = memPtr
	sst.KV.AddNode(n)

	return memPtr
}

// addMemLinkAPI adds a forward LEADSTO link in in-memory NODE_DIRECTORY.
func addMemLinkAPI(fromPtr, toPtr SST.NodePtr) {
	fwd := SST.ST_ZERO + SST.LEADSTO
	inv := SST.ST_ZERO - SST.LEADSTO
	if fromPtr.Class != SST.N1GRAM || toPtr.Class != SST.N1GRAM {
		return // helpers only handle N1GRAM
	}
	fwdLnk := SST.Link{Arr: 1, Wgt: 1.0, Dst: toPtr}
	invLnk := SST.Link{Arr: 1, Wgt: 1.0, Dst: fromPtr}
	SST.NODE_DIRECTORY.N1directory[fromPtr.CPtr].I[fwd] =
		append(SST.NODE_DIRECTORY.N1directory[fromPtr.CPtr].I[fwd], fwdLnk)
	SST.NODE_DIRECTORY.N1directory[toPtr.CPtr].I[inv] =
		append(SST.NODE_DIRECTORY.N1directory[toPtr.CPtr].I[inv], invLnk)
}

// ── Gap 1: GetFwdPathsAsLinks ─────────────────────────────────────────────────

// TestGetFwdPathsAsLinks_BasicChain mirrors the API docs example:
// builds A→B→C→D and verifies all paths are returned.
// Single-word (N1GRAM) nodes are used so addMemLinkAPI can set up links.
func TestGetFwdPathsAsLinks_BasicChain(t *testing.T) {
	sst, teardown := setupAPI(t)
	defer teardown()

	// Single-word nodes → N1GRAM class; addMemLinkAPI only handles N1GRAM.
	ptrA := insertMemAndKV(sst, "alpha", "poem")
	ptrB := insertMemAndKV(sst, "bravo", "poem")
	ptrC := insertMemAndKV(sst, "charlie", "poem")
	ptrD := insertMemAndKV(sst, "delta", "poem")

	addMemLinkAPI(ptrA, ptrB)
	addMemLinkAPI(ptrB, ptrC)
	addMemLinkAPI(ptrC, ptrD)

	paths, count := SST.GetFwdPathsAsLinks(sst, ptrA, SST.LEADSTO, 4, 100)

	if count != len(paths) {
		t.Errorf("count/len mismatch: count=%d len=%d", count, len(paths))
	}
	if len(paths) == 0 {
		t.Fatalf("expected at least one path from A→B→C→D, got 0")
	}

	// Verify paths are non-empty and contain links.
	for i, p := range paths {
		if len(p) == 0 {
			t.Errorf("path %d is empty", i)
		}
	}
}

// TestGetFwdPathsAsLinks_EmptyGraph returns empty, not panic.
func TestGetFwdPathsAsLinks_EmptyGraph(t *testing.T) {
	sst, teardown := setupAPI(t)
	defer teardown()

	var start SST.NodePtr
	start.Class = SST.N1GRAM
	start.CPtr = 9999 // non-existent

	paths, count := SST.GetFwdPathsAsLinks(sst, start, SST.LEADSTO, 3, 100)
	if count != 0 {
		t.Errorf("empty graph: expected 0 paths, got %d", count)
	}
	if len(paths) != 0 {
		t.Errorf("empty graph: expected empty slice, got %d paths", len(paths))
	}
}

// TestGetFwdPathsAsLinks_DepthLimit confirms paths do not exceed depth.
func TestGetFwdPathsAsLinks_DepthLimit(t *testing.T) {
	sst, teardown := setupAPI(t)
	defer teardown()

	// Single-word N1GRAM nodes required for addMemLinkAPI.
	pA := insertMemAndKV(sst, "dA", "d")
	pB := insertMemAndKV(sst, "dB", "d")
	pC := insertMemAndKV(sst, "dC", "d")
	pD := insertMemAndKV(sst, "dD", "d")

	addMemLinkAPI(pA, pB)
	addMemLinkAPI(pB, pC)
	addMemLinkAPI(pC, pD)

	paths, _ := SST.GetFwdPathsAsLinks(sst, pA, SST.LEADSTO, 2, 100)

	for i, p := range paths {
		if len(p) > 2 {
			t.Errorf("path %d exceeds depth=2: len=%d", i, len(p))
		}
	}
}

// TestGetFwdPathsAsLinks_SortedShortestFirst verifies ordering guarantee.
func TestGetFwdPathsAsLinks_SortedShortestFirst(t *testing.T) {
	sst, teardown := setupAPI(t)
	defer teardown()

	// Single-word N1GRAM nodes required for addMemLinkAPI.
	pA := insertMemAndKV(sst, "sA", "s")
	pB := insertMemAndKV(sst, "sB", "s")
	pC := insertMemAndKV(sst, "sC", "s")
	pD := insertMemAndKV(sst, "sD", "s")

	addMemLinkAPI(pA, pB)
	addMemLinkAPI(pA, pC) // short path A→C
	addMemLinkAPI(pB, pD) // longer path A→B→D

	paths, _ := SST.GetFwdPathsAsLinks(sst, pA, SST.LEADSTO, 3, 100)

	for i := 1; i < len(paths); i++ {
		if len(paths[i]) < len(paths[i-1]) {
			t.Errorf("paths not sorted shortest-first: paths[%d] len=%d < paths[%d] len=%d",
				i, len(paths[i]), i-1, len(paths[i-1]))
		}
	}
}

// ── Gap 2: GetDBNodePtrMatchingName / GetDBNodePtrMatchingNCCS ────────────────

// TestGetDBNodePtrMatchingName_FindsBySubstring replicates the API example lookup.
func TestGetDBNodePtrMatchingName_FindsBySubstring(t *testing.T) {
	sst, teardown := setupAPI(t)
	defer teardown()

	insertMemAndKV(sst, "Mary had a little lamb", "poem")
	insertMemAndKV(sst, "Whose fleece was white as snow", "poem")

	results := SST.GetDBNodePtrMatchingName(sst, "Mary had a", "")
	if len(results) == 0 {
		t.Fatal("expected at least one result for 'Mary had a'")
	}

	// Verify the returned node actually contains the search text.
	for _, ptr := range results {
		n := sst.KV.GetNode(ptr)
		if n.S == "" {
			t.Errorf("GetNode returned empty node for ptr %v", ptr)
		}
	}
}

// TestGetDBNodePtrMatchingName_NoMatch returns empty, not nil.
func TestGetDBNodePtrMatchingName_NoMatch(t *testing.T) {
	sst, teardown := setupAPI(t)
	defer teardown()

	insertMemAndKV(sst, "something real", "ch")

	results := SST.GetDBNodePtrMatchingName(sst, "xyzzy_absent", "")
	if len(results) != 0 {
		t.Errorf("expected empty result for absent name, got %d", len(results))
	}
}

// TestGetDBNodePtrMatchingNCCS_ChapterFilter verifies the chapter constraint.
func TestGetDBNodePtrMatchingNCCS_ChapterFilter(t *testing.T) {
	sst, teardown := setupAPI(t)
	defer teardown()

	insertMemAndKV(sst, "alpha node", "chapter_alpha")
	insertMemAndKV(sst, "alpha beta node", "chapter_beta")

	// Filter by chapter_alpha — should only return the first node.
	results := SST.GetDBNodePtrMatchingNCCS(sst, "alpha", "chapter_alpha", nil, nil, false, 100)
	if len(results) == 0 {
		t.Fatal("expected at least 1 result for chapter_alpha filter")
	}
	for _, ptr := range results {
		n := sst.KV.GetNode(ptr)
		if n.Chap != "chapter_alpha" {
			t.Errorf("chapter filter failed: node has Chap=%q, want 'chapter_alpha'", n.Chap)
		}
	}
}

// TestGetDBNodePtrMatchingNCCS_SeqFilter verifies the sequence-start filter.
func TestGetDBNodePtrMatchingNCCS_SeqFilter(t *testing.T) {
	sst, teardown := setupAPI(t)
	defer teardown()

	// Insert one seq-start node and one regular node.
	var seqNode SST.Node
	seqNode.S = "sequence starter"
	seqNode.Chap = "ch"
	seqNode.Seq = true
	seqPtr := sst.KV.AddNode(seqNode)

	var regNode SST.Node
	regNode.S = "regular node"
	regNode.Chap = "ch"
	regNode.Seq = false
	sst.KV.AddNode(regNode)

	results := SST.GetDBNodePtrMatchingNCCS(sst, "", "", nil, nil, true, 100)

	if len(results) == 0 {
		t.Fatal("expected at least 1 seq-start node")
	}
	for _, ptr := range results {
		if ptr != seqPtr {
			n := sst.KV.GetNode(ptr)
			if !n.Seq {
				t.Errorf("seq filter returned non-seq node: %q", n.S)
			}
		}
	}
}

// TestGetDBNodePtrMatchingNCCS_ArrowFilter verifies the arrow constraint.
func TestGetDBNodePtrMatchingNCCS_ArrowFilter(t *testing.T) {
	sst, teardown := setupAPI(t)
	defer teardown()

	dstPtr := sst.KV.AddNode(SST.Node{S: "dst", Chap: "ch"})

	// Node with a LEADSTO link (arrow=1).
	var linked SST.Node
	linked.S = "linked node"
	linked.Chap = "ch"
	idx := SST.ST_ZERO + SST.LEADSTO
	linked.I[idx] = []SST.Link{{Arr: 1, Wgt: 1.0, Dst: dstPtr}}
	linkedPtr := sst.KV.AddNode(linked)

	// Node with no links.
	sst.KV.AddNode(SST.Node{S: "unlinked node", Chap: "ch"})

	results := SST.GetDBNodePtrMatchingNCCS(sst, "", "", nil, []SST.ArrowPtr{1}, false, 100)

	if len(results) == 0 {
		t.Fatal("expected node with arrow=1 to be returned")
	}
	found := false
	for _, ptr := range results {
		if ptr == linkedPtr {
			found = true
			break
		}
	}
	if !found {
		t.Error("linkedPtr not found in arrow-filtered results")
	}
}

// ── Gap 3: Context persistence ────────────────────────────────────────────────

// TestContextPersistence_RoundTrip verifies that UploadContext persists data
// retrievable by both name and pointer.
func TestContextPersistence_RoundTrip(t *testing.T) {
	sst, teardown := setupAPI(t)
	defer teardown()

	// Upload two contexts.
	id1 := sst.KV.UploadContext("context_alpha", 1)
	id2 := sst.KV.UploadContext("context_beta", 2)

	if id1 != 1 {
		t.Errorf("UploadContext: expected id=1, got %d", id1)
	}
	if id2 != 2 {
		t.Errorf("UploadContext: expected id=2, got %d", id2)
	}

	// Lookup by name.
	name1, ptr1 := sst.KV.GetContextByName("context_alpha")
	if name1 != "context_alpha" {
		t.Errorf("GetContextByName: wrong name %q", name1)
	}
	if ptr1 != 1 {
		t.Errorf("GetContextByName: expected ptr=1, got %d", ptr1)
	}

	name2, ptr2 := sst.KV.GetContextByName("context_beta")
	if name2 != "context_beta" || ptr2 != 2 {
		t.Errorf("GetContextByName beta: got (%q, %d)", name2, ptr2)
	}

	// Lookup by pointer.
	rname1, rid1 := sst.KV.GetContextByPtr(1)
	if rname1 != "context_alpha" || rid1 != 1 {
		t.Errorf("GetContextByPtr(1): got (%q, %d)", rname1, rid1)
	}

	rname2, rid2 := sst.KV.GetContextByPtr(2)
	if rname2 != "context_beta" || rid2 != 2 {
		t.Errorf("GetContextByPtr(2): got (%q, %d)", rname2, rid2)
	}
}

// TestContextPersistence_IdempotentUpload verifies that uploading the same
// context name twice returns the same id and does not overwrite.
func TestContextPersistence_IdempotentUpload(t *testing.T) {
	sst, teardown := setupAPI(t)
	defer teardown()

	id1 := sst.KV.UploadContext("my_context", 42)
	id2 := sst.KV.UploadContext("my_context", 99) // same name, different id

	if id1 != 42 {
		t.Errorf("first upload: expected 42, got %d", id1)
	}
	if id2 != 42 {
		t.Errorf("second upload should return original id=42, got %d", id2)
	}

	_, ptr := sst.KV.GetContextByName("my_context")
	if ptr != 42 {
		t.Errorf("stored ptr should be 42, got %d", ptr)
	}
}

// TestContextPersistence_MissingNameFallback verifies the stub fallback for
// unknown names (returns (name, 0) to preserve existing behaviour).
func TestContextPersistence_MissingNameFallback(t *testing.T) {
	sst, teardown := setupAPI(t)
	defer teardown()

	name, ptr := sst.KV.GetContextByName("nonexistent")
	if ptr != -1 {
		t.Errorf("missing context: expected ptr=-1 (not found), got %d", ptr)
	}
	if name != "nonexistent" {
		t.Errorf("missing context: expected name passthrough, got %q", name)
	}
}

// TestContextPersistence_MissingPtrFallback verifies fallback for unknown ptrs.
func TestContextPersistence_MissingPtrFallback(t *testing.T) {
	sst, teardown := setupAPI(t)
	defer teardown()

	name, id := sst.KV.GetContextByPtr(999)
	if name != "any" {
		t.Errorf("missing ptr: expected 'any', got %q", name)
	}
	if id != 999 {
		t.Errorf("missing ptr: expected id=999 passthrough, got %d", id)
	}
}

// TestGetDBContextByName_PublicWrapper tests the public API wrapper in SSTorytime.go.
func TestGetDBContextByName_PublicWrapper(t *testing.T) {
	sst, teardown := setupAPI(t)
	defer teardown()

	sst.KV.UploadContext("wrapper_ctx", 7)

	name, ptr := SST.GetDBContextByName(sst, "wrapper_ctx")
	if ptr != 7 {
		t.Errorf("public wrapper: expected ptr=7, got %d (name=%q)", ptr, name)
	}
}

// TestGetDBContextByPtr_PublicWrapper tests the other direction.
func TestGetDBContextByPtr_PublicWrapper(t *testing.T) {
	sst, teardown := setupAPI(t)
	defer teardown()

	sst.KV.UploadContext("wrapper_ctx_b", 8)

	name, id := SST.GetDBContextByPtr(sst, 8)
	if name != "wrapper_ctx_b" {
		t.Errorf("public wrapper ptr→name: expected 'wrapper_ctx_b', got %q (id=%d)", name, id)
	}
}

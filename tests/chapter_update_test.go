package tests

// chapter_update_test.go — test suite for the BadgerDB chapter management
// and idempotent-insert features introduced to match (and improve on) the
// PostgreSQL DeleteChapter() stored procedure.
//
// Test matrix:
//
//   Idempotency
//     TestAddNode_SameTextReturnsSamePtr          – text index deduplication
//     TestAddNode_DifferentTextGetsDifferentPtr   – independent inserts are separate
//     TestAddNode_SharedChapter                   – second insert adds chapter to Chap list
//
//   Chapter index
//     TestChapIndex_WrittenOnInsert               – chap: index entries visible via GetChapters
//     TestChapIndex_MultiTokenChap                – comma-separated Chap → multiple entries
//     TestGetChapterNames                         – GetChapterNames returns distinct names
//
//   DeleteChapter — exclusive nodes
//     TestDeleteChapter_RemovesExclusiveNodes     – basic delete path
//     TestDeleteChapter_NonExistentIsNoop         – empty chapter → no error, no change
//     TestDeleteChapter_NodeCountAfterDelete      – other chapters are untouched
//
//   DeleteChapter — shared nodes
//     TestDeleteChapter_PreservesSharedNode       – shared node survives with reduced Chap
//     TestDeleteChapter_SharedChapIndexUpdated    – remaining chapter's chap: entry intact
//
//   DeleteChapter — link cleanup
//     TestDeleteChapter_CleansLinksInSurvivors    – links to deleted nodes removed
//     TestDeleteChapter_KeepsLinksToSurvivors     – links between survivors unchanged
//
//   SearchNodesByName
//     TestSearchNodesByName_ExactMatch            – exact (case-insensitive) match
//     TestSearchNodesByName_SubstringMatch        – substring in middle of text
//     TestSearchNodesByName_NoMatch               – query not present → empty result
//     TestSearchNodesByName_Limit                 – limit parameter honoured
//
//   End-to-end re-submit workflow
//     TestReSubmitNote_FreshState                 – delete + re-insert yields correct final count
//     TestReSubmitNote_NoOrphans                  – no stale nodes remain after re-submit

import (
	"fmt"
	"os"
	"strings"
	"testing"

	SST "github.com/srosignoli/sstorytime-kv/pkg/SSTorytime"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func setupChapter(t *testing.T) (SST.PoSST, func()) {
	t.Helper()
	dir := fmt.Sprintf("chap_test_db_%s", strings.ReplaceAll(t.Name(), "/", "_"))
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

// nodeCount returns the total number of node: entries in BadgerDB.
func nodeCount(sst SST.PoSST) int {
	count := 0
	sst.KV.IterateNodes(func(_ SST.Node, _ SST.NodePtr) bool {
		count++
		return true
	})
	return count
}

// findNode returns (node, true) if a node with the given text exists.
func findNode(sst SST.PoSST, text string) (SST.Node, bool) {
	ptrs := sst.KV.SearchNodesByName(text, 1)
	if len(ptrs) == 0 {
		return SST.Node{}, false
	}
	n := sst.KV.GetNode(ptrs[0])
	return n, n.S != ""
}

// chapNodeCount returns how many nodes GetChapters reports for a chapter.
func chapNodeCount(sst SST.PoSST, chapter string) int {
	chapters := sst.KV.GetChapters(chapter, nil, 0)
	return len(chapters[chapter])
}

// insertNode is a one-liner for inserting a node and returning its NodePtr.
func insertNode(sst SST.PoSST, text, chap string) SST.NodePtr {
	var n SST.Node
	n.S = text
	n.Chap = chap
	return sst.KV.AddNode(n)
}

// nodeWithLinksTo returns a Node whose I[ST_ZERO+LEADSTO] contains a link to dst.
func nodeWithLinksTo(text, chap string, dst SST.NodePtr) SST.Node {
	var n SST.Node
	n.S = text
	n.Chap = chap
	n.L, n.NPtr.Class = SST.StorageClass(text)
	idx := SST.ST_ZERO + SST.LEADSTO
	n.I[idx] = append(n.I[idx], SST.Link{Arr: 1, Wgt: 1.0, Dst: dst})
	return n
}

// ── Idempotency ───────────────────────────────────────────────────────────────

// TestAddNode_SameTextReturnsSamePtr verifies the text: secondary index prevents
// duplicate entries for the same text.
func TestAddNode_SameTextReturnsSamePtr(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	ptr1 := insertNode(sst, "the quick fox", "chap_a")
	ptr2 := insertNode(sst, "the quick fox", "chap_a")

	if ptr1 != ptr2 {
		t.Errorf("same text must return same NodePtr: got %v and %v", ptr1, ptr2)
	}
	if nodeCount(sst) != 1 {
		t.Errorf("expected 1 node in DB, got %d", nodeCount(sst))
	}
}

// TestAddNode_DifferentTextGetsDifferentPtr confirms distinct texts get distinct pointers.
func TestAddNode_DifferentTextGetsDifferentPtr(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	ptr1 := insertNode(sst, "alpha", "chap_a")
	ptr2 := insertNode(sst, "beta", "chap_a")

	if ptr1 == ptr2 {
		t.Errorf("different texts must produce different NodePtrs")
	}
	if nodeCount(sst) != 2 {
		t.Errorf("expected 2 nodes, got %d", nodeCount(sst))
	}
}

// TestAddNode_SharedChapter inserts the same node under two chapters and verifies
// that the Chap field becomes comma-separated and both chap: index entries exist.
func TestAddNode_SharedChapter(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	insertNode(sst, "shared text", "chap_a")
	insertNode(sst, "shared text", "chap_b") // same text, new chapter

	// Still only one node in the DB.
	if nodeCount(sst) != 1 {
		t.Errorf("expected 1 node (shared), got %d", nodeCount(sst))
	}

	// The node's Chap must include both chapters.
	n, ok := findNode(sst, "shared text")
	if !ok {
		t.Fatal("shared text node not found")
	}
	chaps := strings.Split(n.Chap, ",")
	hasA, hasB := false, false
	for _, c := range chaps {
		c = strings.TrimSpace(c)
		if c == "chap_a" {
			hasA = true
		}
		if c == "chap_b" {
			hasB = true
		}
	}
	if !hasA || !hasB {
		t.Errorf("Chap field should contain both chapters, got %q", n.Chap)
	}

	// Both chapter indexes must report this node.
	if chapNodeCount(sst, "chap_a") != 1 {
		t.Errorf("chap_a should have 1 node via GetChapters")
	}
	if chapNodeCount(sst, "chap_b") != 1 {
		t.Errorf("chap_b should have 1 node via GetChapters")
	}
}

// ── Chapter index ─────────────────────────────────────────────────────────────

// TestChapIndex_WrittenOnInsert confirms GetChapters finds nodes after insert.
func TestChapIndex_WrittenOnInsert(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	insertNode(sst, "node one", "science")
	insertNode(sst, "node two", "science")
	insertNode(sst, "node three", "history")

	scienceNodes := chapNodeCount(sst, "science")
	if scienceNodes != 2 {
		t.Errorf("expected 2 nodes in 'science', got %d", scienceNodes)
	}
	historyNodes := chapNodeCount(sst, "history")
	if historyNodes != 1 {
		t.Errorf("expected 1 node in 'history', got %d", historyNodes)
	}
}

// TestChapIndex_MultiTokenChap verifies a node with a comma-separated Chap value
// creates one chap: index entry per token at insert time.
func TestChapIndex_MultiTokenChap(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	var n SST.Node
	n.S = "multi chapter node"
	n.Chap = "alpha,beta"
	sst.KV.AddNode(n)

	if chapNodeCount(sst, "alpha") != 1 {
		t.Errorf("expected 1 node in 'alpha'")
	}
	if chapNodeCount(sst, "beta") != 1 {
		t.Errorf("expected 1 node in 'beta'")
	}
}

// TestGetChapterNames verifies distinct chapter names are returned.
func TestGetChapterNames(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	insertNode(sst, "apple", "fruits")
	insertNode(sst, "banana", "fruits")
	insertNode(sst, "carrot", "vegetables")

	names := sst.KV.GetChapterNames()
	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[n] = true
	}
	if !nameSet["fruits"] {
		t.Errorf("GetChapterNames: 'fruits' missing from %v", names)
	}
	if !nameSet["vegetables"] {
		t.Errorf("GetChapterNames: 'vegetables' missing from %v", names)
	}
	if len(names) != 2 {
		t.Errorf("expected 2 distinct chapter names, got %d: %v", len(names), names)
	}
}

// ── DeleteChapter — exclusive nodes ──────────────────────────────────────────

// TestDeleteChapter_RemovesExclusiveNodes is the basic happy path.
func TestDeleteChapter_RemovesExclusiveNodes(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	insertNode(sst, "to be deleted A", "doomed")
	insertNode(sst, "to be deleted B", "doomed")
	insertNode(sst, "survivor", "safe")

	if err := SST.DeleteChapterFromDB(sst, "doomed"); err != nil {
		t.Fatalf("DeleteChapterFromDB: %v", err)
	}

	if nodeCount(sst) != 1 {
		t.Errorf("expected 1 surviving node, got %d", nodeCount(sst))
	}
	if _, found := findNode(sst, "to be deleted A"); found {
		t.Error("'to be deleted A' should have been removed")
	}
	if _, found := findNode(sst, "to be deleted B"); found {
		t.Error("'to be deleted B' should have been removed")
	}
	if _, found := findNode(sst, "survivor"); !found {
		t.Error("'survivor' should still be present")
	}
}

// TestDeleteChapter_NonExistentIsNoop verifies that deleting an unknown chapter
// returns nil and leaves the DB unchanged.
func TestDeleteChapter_NonExistentIsNoop(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	insertNode(sst, "untouched", "real")

	if err := SST.DeleteChapterFromDB(sst, "ghost"); err != nil {
		t.Fatalf("DeleteChapterFromDB on non-existent chapter should return nil, got %v", err)
	}
	if nodeCount(sst) != 1 {
		t.Errorf("DB should be unchanged after no-op delete, got %d nodes", nodeCount(sst))
	}
}

// TestDeleteChapter_NodeCountAfterDelete checks that only the target chapter's
// nodes are removed — other chapters are untouched.
func TestDeleteChapter_NodeCountAfterDelete(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	insertNode(sst, "ch1 node 1", "ch1")
	insertNode(sst, "ch1 node 2", "ch1")
	insertNode(sst, "ch2 node 1", "ch2")
	insertNode(sst, "ch2 node 2", "ch2")
	insertNode(sst, "ch2 node 3", "ch2")

	if err := SST.DeleteChapterFromDB(sst, "ch1"); err != nil {
		t.Fatalf("DeleteChapterFromDB: %v", err)
	}

	if nodeCount(sst) != 3 {
		t.Errorf("expected 3 nodes remaining (ch2 intact), got %d", nodeCount(sst))
	}
	if chapNodeCount(sst, "ch2") != 3 {
		t.Errorf("ch2 should still have 3 nodes, got %d", chapNodeCount(sst, "ch2"))
	}
	if chapNodeCount(sst, "ch1") != 0 {
		t.Errorf("ch1 should have 0 nodes after delete, got %d", chapNodeCount(sst, "ch1"))
	}
}

// ── DeleteChapter — shared nodes ──────────────────────────────────────────────

// TestDeleteChapter_PreservesSharedNode verifies that a node shared across two
// chapters survives when one chapter is deleted, and that its Chap field is
// updated to remove the deleted chapter.
func TestDeleteChapter_PreservesSharedNode(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	// Insert the node under chap_a first, then chap_b (shared-node path).
	insertNode(sst, "shared node", "chap_a")
	insertNode(sst, "shared node", "chap_b")

	if err := SST.DeleteChapterFromDB(sst, "chap_a"); err != nil {
		t.Fatalf("DeleteChapterFromDB: %v", err)
	}

	// Node must still exist.
	n, found := findNode(sst, "shared node")
	if !found {
		t.Fatal("shared node was deleted but should have been preserved")
	}

	// Chap must no longer contain chap_a.
	if strings.Contains(n.Chap, "chap_a") {
		t.Errorf("Chap field still contains 'chap_a' after delete: %q", n.Chap)
	}
	if !strings.Contains(n.Chap, "chap_b") {
		t.Errorf("Chap field lost 'chap_b': %q", n.Chap)
	}
}

// TestDeleteChapter_SharedChapIndexUpdated verifies that after deleting one
// chapter, GetChapters still finds the shared node under the remaining chapter.
func TestDeleteChapter_SharedChapIndexUpdated(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	insertNode(sst, "shared index node", "x")
	insertNode(sst, "shared index node", "y")

	if err := SST.DeleteChapterFromDB(sst, "x"); err != nil {
		t.Fatalf("DeleteChapterFromDB: %v", err)
	}

	// Chapter "x" must be gone from the index.
	if chapNodeCount(sst, "x") != 0 {
		t.Errorf("chapter 'x' should have 0 nodes after delete")
	}
	// Chapter "y" must still have the shared node.
	if chapNodeCount(sst, "y") != 1 {
		t.Errorf("chapter 'y' should still have 1 node, got %d", chapNodeCount(sst, "y"))
	}
}

// ── DeleteChapter — link cleanup ─────────────────────────────────────────────

// TestDeleteChapter_CleansLinksInSurvivors verifies that after deleting a chapter,
// all links in surviving nodes that pointed to the deleted nodes are removed.
func TestDeleteChapter_CleansLinksInSurvivors(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	// Build the target node first so we have its NodePtr for the link.
	targetPtr := insertNode(sst, "target to delete", "doomed")

	// Survivor has a LEADSTO link pointing at the target.
	survivor := nodeWithLinksTo("survivor with link", "safe", targetPtr)
	survivorPtr := sst.KV.AddNode(survivor)

	if err := SST.DeleteChapterFromDB(sst, "doomed"); err != nil {
		t.Fatalf("DeleteChapterFromDB: %v", err)
	}

	// The target must be gone.
	if _, found := findNode(sst, "target to delete"); found {
		t.Fatal("target node should have been deleted")
	}

	// The survivor must still exist with its dangling link cleaned up.
	stored := sst.KV.GetNode(survivorPtr)
	if stored.S == "" {
		t.Fatal("survivor node not found after chapter delete")
	}
	idx := SST.ST_ZERO + SST.LEADSTO
	for _, lnk := range stored.I[idx] {
		if lnk.Dst == targetPtr {
			t.Errorf("survivor still has a link to the deleted node %v", targetPtr)
		}
	}
}

// TestDeleteChapter_KeepsLinksToSurvivors verifies that links between surviving
// nodes are not disturbed by the delete operation.
func TestDeleteChapter_KeepsLinksToSurvivors(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	survivorBPtr := insertNode(sst, "survivor B", "safe")
	survivorA := nodeWithLinksTo("survivor A", "safe", survivorBPtr)
	survivorAPtr := sst.KV.AddNode(survivorA)

	// Also insert a node in the doomed chapter so DeleteChapter has something to do.
	insertNode(sst, "doomed node", "doomed")

	if err := SST.DeleteChapterFromDB(sst, "doomed"); err != nil {
		t.Fatalf("DeleteChapterFromDB: %v", err)
	}

	// survivorA's link to survivorB must be intact.
	stored := sst.KV.GetNode(survivorAPtr)
	idx := SST.ST_ZERO + SST.LEADSTO
	found := false
	for _, lnk := range stored.I[idx] {
		if lnk.Dst == survivorBPtr {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("link from survivorA to survivorB was lost during chapter delete")
	}
}

// ── SearchNodesByName ─────────────────────────────────────────────────────────

// TestSearchNodesByName_ExactMatch verifies a case-insensitive exact match.
func TestSearchNodesByName_ExactMatch(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	insertNode(sst, "Apollo Mission", "space")
	insertNode(sst, "Gemini Program", "space")

	results := sst.KV.SearchNodesByName("Apollo Mission", 10)
	if len(results) != 1 {
		t.Fatalf("expected 1 result for exact match, got %d", len(results))
	}
	n := sst.KV.GetNode(results[0])
	if n.S != "Apollo Mission" {
		t.Errorf("wrong node returned: %q", n.S)
	}
}

// TestSearchNodesByName_SubstringMatch checks that partial text finds the node.
func TestSearchNodesByName_SubstringMatch(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	insertNode(sst, "The quick brown fox", "tales")
	insertNode(sst, "The lazy dog slept", "tales")

	results := sst.KV.SearchNodesByName("quick brown", 10)
	if len(results) != 1 {
		t.Fatalf("expected 1 result for substring 'quick brown', got %d", len(results))
	}
}

// TestSearchNodesByName_NoMatch returns an empty slice for an absent query.
func TestSearchNodesByName_NoMatch(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	insertNode(sst, "something here", "ch")

	results := sst.KV.SearchNodesByName("xyzzy", 10)
	if len(results) != 0 {
		t.Errorf("expected 0 results for absent query, got %d", len(results))
	}
}

// TestSearchNodesByName_Limit verifies the limit parameter is respected.
func TestSearchNodesByName_Limit(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	for i := 0; i < 10; i++ {
		insertNode(sst, fmt.Sprintf("common prefix item %d", i), "ch")
	}

	results := sst.KV.SearchNodesByName("common prefix", 3)
	if len(results) > 3 {
		t.Errorf("limit=3: expected at most 3 results, got %d", len(results))
	}
}

// ── End-to-end re-submit workflow ─────────────────────────────────────────────

// TestReSubmitNote_FreshState simulates a full note re-submit cycle:
//   1. Insert version 1 of a note (3 nodes).
//   2. Delete the chapter.
//   3. Insert version 2 of the note (2 original nodes changed, 1 new).
//
// The final state must reflect version 2 exactly.
func TestReSubmitNote_FreshState(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	chapter := "my_note"

	// Version 1: three sentences.
	insertNode(sst, "The cat sat on the mat", chapter)
	insertNode(sst, "It was a sunny day", chapter)
	insertNode(sst, "The wind blew gently", chapter)

	if nodeCount(sst) != 3 {
		t.Fatalf("v1 setup: expected 3 nodes, got %d", nodeCount(sst))
	}

	// Delete chapter before re-import.
	if err := SST.DeleteChapterFromDB(sst, chapter); err != nil {
		t.Fatalf("DeleteChapterFromDB v1: %v", err)
	}
	if nodeCount(sst) != 0 {
		t.Fatalf("after delete: expected 0 nodes, got %d", nodeCount(sst))
	}

	// Version 2: one sentence removed, one changed, one new.
	insertNode(sst, "The cat sat on the mat", chapter)  // same
	insertNode(sst, "It was a stormy night", chapter)   // changed
	insertNode(sst, "A new sentence appears", chapter)  // new

	if nodeCount(sst) != 3 {
		t.Errorf("v2: expected 3 nodes, got %d", nodeCount(sst))
	}

	// The deleted sentence must not be present.
	if _, found := findNode(sst, "The wind blew gently"); found {
		t.Error("deleted sentence still present after re-submit")
	}
	// The unchanged sentence must be present.
	if _, found := findNode(sst, "The cat sat on the mat"); !found {
		t.Error("unchanged sentence missing after re-submit")
	}
	// The changed sentence must be the new version.
	if _, found := findNode(sst, "It was a stormy night"); !found {
		t.Error("updated sentence missing after re-submit")
	}
	if _, found := findNode(sst, "It was a sunny day"); found {
		t.Error("old version of updated sentence still present")
	}
	// The new sentence must be present.
	if _, found := findNode(sst, "A new sentence appears"); !found {
		t.Error("new sentence missing after re-submit")
	}
	// Chapter index must be correct.
	if chapNodeCount(sst, chapter) != 3 {
		t.Errorf("chapter index: expected 3 nodes, got %d", chapNodeCount(sst, chapter))
	}
}

// TestReSubmitNote_NoOrphans re-submits a note twice and confirms that orphaned
// nodes from the first version are never present after the second import.
func TestReSubmitNote_NoOrphans(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	chapter := "orphan_check"

	// Version 1
	for i := 0; i < 5; i++ {
		insertNode(sst, fmt.Sprintf("v1 sentence %d", i), chapter)
	}

	// Re-import cycle 1 → 2
	if err := SST.DeleteChapterFromDB(sst, chapter); err != nil {
		t.Fatalf("cycle 1 delete: %v", err)
	}
	for i := 0; i < 4; i++ { // one fewer sentence
		insertNode(sst, fmt.Sprintf("v2 sentence %d", i), chapter)
	}

	// Re-import cycle 2 → 3
	if err := SST.DeleteChapterFromDB(sst, chapter); err != nil {
		t.Fatalf("cycle 2 delete: %v", err)
	}
	for i := 0; i < 6; i++ { // two more sentences
		insertNode(sst, fmt.Sprintf("v3 sentence %d", i), chapter)
	}

	// Final state must be exactly v3 — no orphans from v1 or v2.
	total := nodeCount(sst)
	if total != 6 {
		t.Errorf("expected 6 nodes (v3), got %d — possible orphans from earlier versions", total)
	}

	for i := 0; i < 5; i++ {
		if _, found := findNode(sst, fmt.Sprintf("v1 sentence %d", i)); found {
			t.Errorf("v1 sentence %d is an orphan still in DB", i)
		}
	}
	for i := 0; i < 4; i++ {
		if _, found := findNode(sst, fmt.Sprintf("v2 sentence %d", i)); found {
			t.Errorf("v2 sentence %d is an orphan still in DB", i)
		}
	}
}

// TestDeleteChapterFromDB_StoresCRUDWrapper verifies the store_crud.go public
// wrapper DeleteChapterFromDB delegates correctly.
func TestDeleteChapterFromDB_StoresCRUDWrapper(t *testing.T) {
	sst, teardown := setupChapter(t)
	defer teardown()

	insertNode(sst, "wrapper test node", "wrapper_chap")
	if nodeCount(sst) != 1 {
		t.Fatalf("setup: expected 1 node, got %d", nodeCount(sst))
	}

	if err := SST.DeleteChapterFromDB(sst, "wrapper_chap"); err != nil {
		t.Fatalf("DeleteChapterFromDB: %v", err)
	}
	if nodeCount(sst) != 0 {
		t.Errorf("expected 0 nodes after delete via wrapper, got %d", nodeCount(sst))
	}
}

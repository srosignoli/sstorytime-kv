package tests

import (
	"github.com/srosignoli/sstorytime-kv/pkg/SSTorytime"
	"os"
	"testing"
)

func setupTestStore(t *testing.T) SSTorytime.PoSST {
	os.RemoveAll("test_db")
	kvStore, err := SSTorytime.OpenBadgerStore("test_db")
	if err != nil {
		t.Fatalf("Failed to initialize test BadgerKV: %v", err)
	}

	var sst SSTorytime.PoSST
	sst.KV = kvStore

	SSTorytime.NO_NODE_PTR.Class = 0
	SSTorytime.NO_NODE_PTR.CPtr = -1
	SSTorytime.NONODE.Class = 0
	SSTorytime.NONODE.CPtr = 0

	return sst
}

func teardownTestStore(sst SSTorytime.PoSST) {
	if sst.KV != nil {
		sst.KV.Close()
	}
	os.RemoveAll("test_db")
}

// =====================================================================
// Phase 1 Tests: Schema operations are no-ops in KV
// =====================================================================

func TestCreateTypeAndTable(t *testing.T) {
	sst := setupTestStore(t)
	defer teardownTestStore(sst)

	if !SSTorytime.CreateType(sst, "mock") {
		t.Errorf("CreateType should transparently return true for KV wrapper")
	}

	if !SSTorytime.CreateTable(sst, "mock") {
		t.Errorf("CreateTable should transparently return true for KV wrapper")
	}
}

// =====================================================================
// Phase 2 Tests: Context CRUD
// =====================================================================

func TestContextUploadAndRetrieval(t *testing.T) {
	sst := setupTestStore(t)
	defer teardownTestStore(sst)

	ptr := SSTorytime.UploadContextToDB(sst, "galaxy", 123)
	if ptr != 123 {
		t.Errorf("Expected 123, got %d", ptr)
	}

	// Context persistence is now implemented: GetDBContextByPtr returns the
	// stored name, not the old stub value "any".
	name, _ := SSTorytime.GetDBContextByPtr(sst, 123)
	if name != "galaxy" {
		t.Errorf("Expected 'galaxy', got %s", name)
	}
}

func TestGetLastSawSection(t *testing.T) {
	sst := setupTestStore(t)
	defer teardownTestStore(sst)

	result := SSTorytime.GetLastSawSection(sst)
	if result == nil {
		t.Errorf("GetLastSawSection should return empty slice, not nil")
	}
}

func TestGetLastSawNPtr(t *testing.T) {
	sst := setupTestStore(t)
	defer teardownTestStore(sst)

	var nptr SSTorytime.NodePtr
	nptr.Class = 1
	nptr.CPtr = 42

	ls := SSTorytime.GetLastSawNPtr(sst, nptr)
	// Should return zero-value LastSeen without panicking
	if ls.Freq != 0 {
		t.Errorf("Expected zero frequency for unseen node")
	}
}

// =====================================================================
// Phase 3 Tests: Adjacency graph scans
// =====================================================================

func TestGetDBSingletonBySTType(t *testing.T) {
	sst := setupTestStore(t)
	defer teardownTestStore(sst)

	// An empty graph should return empty slices, not panic
	src, snk := SSTorytime.GetDBSingletonBySTType(sst, []int{1}, "any", nil)
	if src != nil && len(src) != 0 {
		t.Errorf("Expected empty src for empty graph, got %d nodes", len(src))
	}
	if snk != nil && len(snk) != 0 {
		t.Errorf("Expected empty snk for empty graph, got %d nodes", len(snk))
	}
}

func TestGetConstrainedFwdLinks(t *testing.T) {
	sst := setupTestStore(t)
	defer teardownTestStore(sst)

	// Empty start set should produce no links, not panic
	links := SSTorytime.GetConstrainedFwdLinks(sst, nil, "any", nil, []int{1}, nil, 100)
	if links != nil && len(links) != 0 {
		t.Errorf("Expected empty links for nil start set, got %d", len(links))
	}
}

func TestGetNewlySeenNPtrs(t *testing.T) {
	sst := setupTestStore(t)
	defer teardownTestStore(sst)

	var sp SSTorytime.SearchParameters
	nptrs := SSTorytime.GetNewlySeenNPtrs(sst, sp)
	if nptrs == nil {
		t.Errorf("Expected empty map, not nil")
	}
	if len(nptrs) != 0 {
		t.Errorf("Expected zero entries, got %d", len(nptrs))
	}
}

func TestGetDBAdjacentNodePtrBySTType(t *testing.T) {
	sst := setupTestStore(t)
	defer teardownTestStore(sst)

	// Should return nil values gracefully
	adj, nptrs := SSTorytime.GetDBAdjacentNodePtrBySTType(sst, []int{1}, "any", nil, false)
	if adj != nil {
		t.Errorf("Expected nil adjacency for empty graph")
	}
	if nptrs != nil {
		t.Errorf("Expected nil node list for empty graph")
	}
}

func TestGetAppointedNodesBySTType(t *testing.T) {
	sst := setupTestStore(t)
	defer teardownTestStore(sst)

	// Empty graph should return empty map, not panic
	result := SSTorytime.GetAppointedNodesBySTType(sst, 1, nil, "any", 10)
	if result == nil {
		t.Errorf("Expected empty map, not nil")
	}
	if len(result) != 0 {
		t.Errorf("Expected zero appointments for empty graph, got %d", len(result))
	}
}

// =====================================================================
// Phase 4 Tests: BFS/DFS cone traversals
// =====================================================================

func TestGetFwdConeAsNodes_EmptyGraph(t *testing.T) {
	sst := setupTestStore(t)
	defer teardownTestStore(sst)

	var start SSTorytime.NodePtr
	start.Class = 1
	start.CPtr = 0

	// Should return empty, not panic
	nodes := SSTorytime.GetFwdConeAsNodes(sst, start, 1, 3, 100)
	if nodes != nil && len(nodes) != 0 {
		t.Errorf("Expected empty cone from empty graph, got %d nodes", len(nodes))
	}
}

func TestGetFwdConeAsLinks_EmptyGraph(t *testing.T) {
	sst := setupTestStore(t)
	defer teardownTestStore(sst)

	var start SSTorytime.NodePtr
	start.Class = 1
	start.CPtr = 0

	links := SSTorytime.GetFwdConeAsLinks(sst, start, 1, 3)
	if links != nil && len(links) != 0 {
		t.Errorf("Expected empty links from empty graph, got %d", len(links))
	}
}

func TestGetEntireConePathsAsLinks(t *testing.T) {
	sst := setupTestStore(t)
	defer teardownTestStore(sst)

	var start SSTorytime.NodePtr
	start.Class = 1
	start.CPtr = 0

	paths, count := SSTorytime.GetEntireConePathsAsLinks(sst, "fwd", start, 3, 100)
	if count != len(paths) {
		t.Errorf("Count mismatch: %d vs %d", count, len(paths))
	}
}

func TestGetEntireNCConePathsAsLinks(t *testing.T) {
	sst := setupTestStore(t)
	defer teardownTestStore(sst)

	var start SSTorytime.NodePtr
	start.Class = 1
	start.CPtr = 0

	paths, count := SSTorytime.GetEntireNCConePathsAsLinks(sst, "both", []SSTorytime.NodePtr{start}, 3, "any", nil, 100)
	if count != len(paths) {
		t.Errorf("Count mismatch: %d vs %d", count, len(paths))
	}
}

func TestGetConstraintConePathsAsLinks(t *testing.T) {
	sst := setupTestStore(t)
	defer teardownTestStore(sst)

	var start SSTorytime.NodePtr
	start.Class = 1
	start.CPtr = 0

	paths, count := SSTorytime.GetConstraintConePathsAsLinks(sst,
		[]SSTorytime.NodePtr{start}, 3, "any", nil, nil, []int{1}, 100)
	if count != len(paths) {
		t.Errorf("Count mismatch: %d vs %d", count, len(paths))
	}
}

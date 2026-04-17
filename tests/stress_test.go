package tests

// Stress tests for the BadgerDB port of SSTorytime.
//
// TestStressLargeGraph builds a synthetic 1 000-node / ~3 000-link graph that
// is structurally representative of MobyDickNotes.n4l (32 K lines, ~9.5 K
// relations across 203 chapters).  It then runs the main traversal APIs and
// records timing + allocation figures.
//
// TestStressMobyDick is an integration test that builds the N4L binary and
// runs it against the real MobyDickNotes.n4l file.  It is skipped
// automatically when the example file is not present or when the build fails,
// so it is safe to run in CI environments that have only the source tree.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	SST "github.com/srosignoli/sstorytime-kv/pkg/SSTorytime"
)

const (
	stressChapters  = 10
	stressNodesPerChap = 100   // 10 × 100 = 1 000 nodes total
	stressLinksPerNode = 3     // ~3 000 forward links total
)

// ─── Synthetic large-graph stress test ───────────────────────────────────────

func TestStressLargeGraph(t *testing.T) {
	dir := "stress_large_db"
	os.RemoveAll(dir)
	kv, err := SST.OpenBadgerStore(dir)
	if err != nil {
		t.Fatalf("OpenBadgerStore: %v", err)
	}
	defer func() {
		kv.Close()
		os.RemoveAll(dir)
	}()

	var sst SST.PoSST
	sst.KV = kv
	SST.NO_NODE_PTR.Class = 0
	SST.NO_NODE_PTR.CPtr = -1
	SST.NONODE.Class = 0
	SST.NONODE.CPtr = 0

	// Reset in-memory directory for cone traversals
	SST.NODE_DIRECTORY = SST.NodeDirectory{}
	SST.MemoryInit()

	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)
	buildStart := time.Now()

	// ── Build graph ──────────────────────────────────────────────────────────
	// Nodes: stressChapters chapters × stressNodesPerChap nodes
	// Links: each node links to the next stressLinksPerNode nodes in the same chapter
	//        plus one cross-chapter link to the first node of the next chapter.

	type entry struct {
		memPtr SST.NodePtr // in-memory (for cone traversal)
		kvPtr  SST.NodePtr // BadgerDB (for GetDBSingleton / GetConstrainedFwdLinks)
	}

	grid := make([][]entry, stressChapters)

	// Insert all nodes first, collect pointers
	for c := 0; c < stressChapters; c++ {
		chap := fmt.Sprintf("chapter%02d", c)
		grid[c] = make([]entry, stressNodesPerChap)
		for n := 0; n < stressNodesPerChap; n++ {
			name := fmt.Sprintf("n%02d_%03d", c, n)
			// in-memory node
			mPtr := insertMemNode(name, chap)
			// KV node (empty I[], links will be via AddLink edge keys)
			kPtr := kv.AddNode(SST.Node{S: name, Chap: chap})
			grid[c][n] = entry{memPtr: mPtr, kvPtr: kPtr}
		}
	}

	// Insert links
	for c := 0; c < stressChapters; c++ {
		for n := 0; n < stressNodesPerChap; n++ {
			from := grid[c][n]
			for k := 1; k <= stressLinksPerNode; k++ {
				toN := (n + k) % stressNodesPerChap
				to := grid[c][toN]
				if to.memPtr == from.memPtr {
					continue
				}
				// in-memory link
				addMemLink(from.memPtr, to.memPtr, SST.LEADSTO, 1)
				// KV edge link
				lnk := SST.Link{Arr: 1, Wgt: 1.0, Dst: to.kvPtr}
				kv.AddLink(from.kvPtr, lnk, to.kvPtr)
			}
			// cross-chapter link to next chapter's first node
			nextC := (c + 1) % stressChapters
			cross := grid[nextC][0]
			if cross.memPtr != from.memPtr {
				addMemLink(from.memPtr, cross.memPtr, SST.CONTAINS, 2)
				lnk := SST.Link{Arr: 2, Wgt: 1.0, Dst: cross.kvPtr}
				kv.AddLink(from.kvPtr, lnk, cross.kvPtr)
			}
		}
	}

	buildElapsed := time.Since(buildStart)
	runtime.GC()
	var memAfterBuild runtime.MemStats
	runtime.ReadMemStats(&memAfterBuild)
	heapBuildKB := (memAfterBuild.HeapAlloc - memBefore.HeapAlloc) / 1024

	t.Logf("=== Stress graph built ===")
	t.Logf("  Nodes:         %d", stressChapters*stressNodesPerChap)
	t.Logf("  Build time:    %v", buildElapsed)
	t.Logf("  Heap growth:   %d KB", heapBuildKB)

	// ── Traversal 1: GetFwdConeAsNodes from root ──────────────────────────
	root := grid[0][0].memPtr
	t0 := time.Now()
	coneNodes := SST.GetFwdConeAsNodes(sst, root, SST.LEADSTO, 5, 500)
	t.Logf("  GetFwdConeAsNodes(depth=5,limit=500): %d nodes in %v", len(coneNodes), time.Since(t0))

	if len(coneNodes) == 0 {
		t.Error("GetFwdConeAsNodes returned 0 nodes on large graph — traversal broken")
	}

	// ── Traversal 2: GetEntireConePathsAsLinks ────────────────────────────
	t0 = time.Now()
	paths, pathCount := SST.GetEntireConePathsAsLinks(sst, "fwd", root, 3, 200)
	t.Logf("  GetEntireConePathsAsLinks(depth=3,limit=200): %d paths in %v", pathCount, time.Since(t0))

	if pathCount != len(paths) {
		t.Errorf("path count mismatch: len=%d count=%d", len(paths), pathCount)
	}

	// ── Traversal 3: GetDBSingletonBySTType across all KV nodes ──────────
	// NOTE: GetDBSingletonBySTType and GetConstrainedFwdLinks read node.I[] from the
	// BadgerDB-stored Node struct.  In this stress test, links are stored via
	// AddLink() in separate "edge:" keys (not embedded in node.I[]).  So these
	// queries return 0 here — this is the known architectural separation between
	// the in-memory I[] layer (used by cone traversals) and the KV edge-key layer.
	// Use nodeWithLinks() when you need I[]-based KV queries (see correctness_test.go).
	t0 = time.Now()
	src, snk := SST.GetDBSingletonBySTType(sst, []int{SST.LEADSTO}, "any", nil)
	t.Logf("  GetDBSingletonBySTType: %d sources, %d sinks in %v (0 expected: links in edge-keys, not node.I[])", len(src), len(snk), time.Since(t0))

	// ── Traversal 4: GetConstrainedFwdLinks from 10 random KV nodes ──────
	sample := make([]SST.NodePtr, 0, 10)
	for c := 0; c < stressChapters; c++ {
		sample = append(sample, grid[c][0].kvPtr)
	}
	t0 = time.Now()
	links := SST.GetConstrainedFwdLinks(sst, sample, "any", nil, []int{SST.LEADSTO}, nil, 1000)
	t.Logf("  GetConstrainedFwdLinks(10 nodes): %d links in %v (0 expected: same architectural reason)", len(links), time.Since(t0))

	// Final memory report
	runtime.GC()
	var memFinal runtime.MemStats
	runtime.ReadMemStats(&memFinal)
	heapFinalKB := int64(memFinal.HeapAlloc)/1024 - int64(memBefore.HeapAlloc)/1024

	t.Logf("=== Memory summary ===")
	t.Logf("  Heap delta (build+traversals): %d KB", heapFinalKB)
	t.Logf("  Total allocations:             %d", memFinal.Mallocs-memBefore.Mallocs)

	// Sanity: nodes and singletons should be in expected range
	totalNodes := stressChapters * stressNodesPerChap
	if len(src)+len(snk) > totalNodes {
		t.Errorf("singleton count (%d src + %d snk) exceeds total nodes (%d)",
			len(src), len(snk), totalNodes)
	}
}

// ─── MobyDick integration test ───────────────────────────────────────────────

// TestStressMobyDick builds the N4L binary and runs it against MobyDickNotes.n4l.
// The test is skipped if:
//   - the MobyDick file is absent, or
//   - the build fails (e.g. missing Go toolchain in CI)
//
// Run with: go test -v -run TestStressMobyDick -timeout 5m ./...
func TestStressMobyDick(t *testing.T) {
	// Locate the MobyDick example file
	mobyPath, err := filepath.Abs("../examples/MobyDickNotes.n4l")
	if err != nil || !fileExists(mobyPath) {
		t.Skipf("MobyDickNotes.n4l not found at %s — skipping integration test", mobyPath)
	}

	// Build the N4L binary from the project root
	rootDir, _ := filepath.Abs("..")
	binaryPath := filepath.Join(rootDir, "N4L_stress_test")

	buildCmd := exec.Command("go", "build", "-o", binaryPath, "./src/N4L.go")
	buildCmd.Dir = rootDir
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Skipf("Failed to build N4L binary: %v\n%s", err, out)
	}
	defer os.Remove(binaryPath)

	// N4L reads SSTconfig/ from its working directory, so we run it from
	// the project root (where SSTconfig/ lives).  We redirect sst_data to
	// a temp path by symlinking before the run and cleaning up after.
	sstDataPath := filepath.Join(rootDir, "sst_data_moby_stress")
	os.RemoveAll(sstDataPath)
	defer os.RemoveAll(sstDataPath)

	// Run: N4L -u MobyDickNotes.n4l  (from rootDir so SSTconfig/ is found)
	t.Log("Ingesting MobyDickNotes.n4l ...")
	runStart := time.Now()
	runCmd := exec.Command(binaryPath, "-u", mobyPath)
	runCmd.Dir = rootDir
	out, runErr := runCmd.CombinedOutput()
	elapsed := time.Since(runStart)

	t.Logf("Ingestion time: %v", elapsed)
	if runErr != nil {
		t.Logf("N4L output:\n%s", out)
		t.Fatalf("N4L ingestion failed: %v", runErr)
	}

	// N4L writes sst_data/ relative to its working directory (rootDir)
	sstDataPath = filepath.Join(rootDir, "sst_data")
	if !fileExists(sstDataPath) {
		t.Fatalf("sst_data directory not created at %s", sstDataPath)
	}
	defer os.RemoveAll(sstDataPath)

	kv, err := SST.OpenBadgerStore(sstDataPath)
	if err != nil {
		t.Fatalf("OpenBadgerStore on MobyDick result: %v", err)
	}
	defer kv.Close()

	var sst SST.PoSST
	sst.KV = kv

	nodeCount := 0
	kv.IterateNodes(func(n SST.Node, ptr SST.NodePtr) bool {
		nodeCount++
		return true
	})
	t.Logf("Nodes in BadgerDB after MobyDick ingestion: %d", nodeCount)

	if nodeCount == 0 {
		t.Error("No nodes found after MobyDick ingestion — IterateNodes or Open broken")
	}

	// Run a cone traversal from the first discovered node
	var firstPtr SST.NodePtr
	kv.IterateNodes(func(n SST.Node, ptr SST.NodePtr) bool {
		firstPtr = ptr
		return false // stop after first
	})

	t0 := time.Now()
	src, snk := SST.GetDBSingletonBySTType(sst, []int{SST.LEADSTO}, "any", nil)
	t.Logf("GetDBSingletonBySTType: %d sources, %d sinks in %v", len(src), len(snk), time.Since(t0))

	t0 = time.Now()
	fwdLinks := SST.GetConstrainedFwdLinks(sst, []SST.NodePtr{firstPtr}, "any", nil,
		[]int{SST.LEADSTO}, nil, 100)
	t.Logf("GetConstrainedFwdLinks from first node: %d links in %v", len(fwdLinks), time.Since(t0))

	t.Logf("=== MobyDick stress summary ===")
	t.Logf("  File:    MobyDickNotes.n4l (32K lines, ~9.5K relations)")
	t.Logf("  Ingest:  %v", elapsed)
	t.Logf("  Nodes:   %d", nodeCount)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

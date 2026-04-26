package SSTorytime

import (
	"log"
	"strings"
	"sync"
)

// GetDBPageMap delegates page-map retrieval to the storage backend.
func GetDBPageMap(sst PoSST, chap string, cn []string, page int) []PageMap {
	return sst.KV.GetPageMap(chap, cn, page)
}

// GetChaptersByChapContext returns a map of chapter-name → []node-text.
// If chap is non-empty only that chapter is returned; cn filters by context;
// limit caps the number of chapters (0 = unlimited).
func GetChaptersByChapContext(sst PoSST, chap string, cn []string, limit int) map[string][]string {
	return sst.KV.GetChapters(chap, cn, limit)
}

// GetDBChaptersMatchingName returns all chapter names that contain src as a
// substring.  Uses the chap: secondary index — no full-node scan required.
func GetDBChaptersMatchingName(sst PoSST, src string) []string {
	all := sst.KV.GetChapterNames()
	if src == "" {
		return all
	}
	var out []string
	for _, name := range all {
		if strings.Contains(name, src) {
			out = append(out, name)
		}
	}
	return out
}

// ── Node-name search ──────────────────────────────────────────────────────────

// GetDBNodePtrMatchingName returns all NodePtrs whose text contains name as a
// substring (case-insensitive).  An optional chapter filter narrows the result.
//
// This matches the PostgreSQL API signature used in the docs' API example:
//
//	start_set := SST.GetDBNodePtrMatchingName(ctx, "Mary had a", "")
func GetDBNodePtrMatchingName(sst PoSST, name, chap string) []NodePtr {
	return GetDBNodePtrMatchingNCCS(sst, name, chap, nil, nil, false, CAUSAL_CONE_MAXLIMIT)
}

var getDBNodePtrMatchingNCCSDeprecationOnce sync.Once

// GetDBNodePtrMatchingNCCS returns NodePtrs matching the full NCCS criteria
// (Name, Chapter, Context, Sequence) used by the PostgreSQL implementation.
//
// Parameters:
//
//	nm    – case-insensitive substring to match against node text ("" = all nodes)
//	chap  – chapter filter; "" or "any" = no filter
//	cn    – context name filter (ignored if nil; requires context persistence)
//	arrow – arrow-type filter: node must have ≥1 link with one of these arrows
//	seq   – if true, only return nodes marked as sequence starts (n.Seq == true)
//	limit – maximum results (use CAUSAL_CONE_MAXLIMIT for no effective cap)
//
// The Postgres version issues a single SQL query with ORDER BY L ASC and
// CARDINALITY(links) DESC.  The KV implementation fetches via the text: index
// (already sorted by key, closest match first) and then applies filters in Go.
//
// Deprecated: prefer (*Index).SearchByQuery for text matching, then apply
// chapter/context/arrow filters in caller code. SearchByQuery handles
// stemming, accent folding, CJK bigrams, and the full operator grammar;
// GetDBNodePtrMatchingNCCS uses raw substring matching that fails on stemmed
// or accent-folded inputs. Emits a one-shot warning the first time it runs.
func GetDBNodePtrMatchingNCCS(sst PoSST, nm, chap string, cn []string, arrow []ArrowPtr, seq bool, limit int) []NodePtr {
	getDBNodePtrMatchingNCCSDeprecationOnce.Do(func() {
		log.Println("SSTorytime: GetDBNodePtrMatchingNCCS is deprecated; use (*Index).SearchByQuery instead")
	})
	// Build arrow set for O(1) membership check.
	var arrowSet map[ArrowPtr]bool
	if len(arrow) > 0 {
		arrowSet = make(map[ArrowPtr]bool, len(arrow))
		for _, a := range arrow {
			arrowSet[a] = true
		}
	}

	// Over-fetch from the text index, then filter.
	fetchLimit := limit * 10
	if fetchLimit < 100 {
		fetchLimit = 100
	}
	var candidates []NodePtr
	if nm == "" {
		// No text filter — iterate all nodes.
		sst.KV.IterateNodes(func(n Node, ptr NodePtr) bool {
			candidates = append(candidates, ptr)
			return len(candidates) < fetchLimit
		})
	} else {
		candidates = sst.KV.SearchNodesByName(nm, fetchLimit)
	}

	var results []NodePtr
	for _, ptr := range candidates {
		n := sst.KV.GetNode(ptr)

		// Chapter filter.
		if chap != "" && chap != "any" {
			if !strings.Contains(n.Chap, chap) {
				continue
			}
		}

		// Sequence filter.
		if seq && !n.Seq {
			continue
		}

		// Arrow filter: node must have ≥1 link with one of the requested arrows.
		if arrowSet != nil && !nodeHasAnyArrow(n, arrowSet) {
			continue
		}

		results = append(results, ptr)
		if len(results) >= limit {
			break
		}
	}
	return results
}

// nodeHasAnyArrow returns true if node n has at least one link whose arrow
// pointer is in arrowSet.
func nodeHasAnyArrow(n Node, arrowSet map[ArrowPtr]bool) bool {
	for st := 0; st < ST_TOP; st++ {
		for _, lnk := range n.I[st] {
			if arrowSet[lnk.Arr] {
				return true
			}
		}
	}
	return false
}

// containsSubstring is a simple case-sensitive substring check.
func containsSubstring(s, substr string) bool {
	return strings.Contains(s, substr)
}

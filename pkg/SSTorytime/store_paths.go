package SSTorytime

// GetFwdPathsAsLinks returns all forward paths from start as [][]Link, filtered
// by the given STtype and capped at maxlimit paths of up to depth hops.
//
// This is the function referenced in the API documentation example:
//
//	paths, _ := SST.GetFwdPathsAsLinks(ctx, startNode, sttype, pathLength, maxlimit)
//
// Implementation: delegates to enumeratePaths (the same in-memory BFS used by
// GetEntireConePathsAsLinks and GetConstraintConePathsAsLinks) with forward=true
// and sttypes=[sttype].  The KV store is not consulted — traversal operates on
// the in-memory NODE_DIRECTORY populated by GraphToDB / N4L parsing.
func GetFwdPathsAsLinks(sst PoSST, start NodePtr, sttype, depth int, maxlimit int) ([][]Link, int) {
	var retval [][]Link

	var sttypes []int
	if sttype != 0 {
		sttypes = []int{sttype}
	}
	// sttype == 0 means "any forward link" — pass nil sttypes to enumeratePaths.

	enumeratePaths(start, depth, maxlimit, true, nil, &retval, nil, sttypes)

	// Sort paths shortest-first (mirrors Postgres FwdPathsAsLinks ordering).
	sortPathsByLength(retval)

	return retval, len(retval)
}

// sortPathsByLength sorts a slice of link-paths by ascending length in place.
func sortPathsByLength(paths [][]Link) {
	for i := 1; i < len(paths); i++ {
		for j := i; j > 0 && len(paths[j]) < len(paths[j-1]); j-- {
			paths[j], paths[j-1] = paths[j-1], paths[j]
		}
	}
}

// SolveNodePtrs resolves a mixed list of node names and literal NodePtrs to a
// deduplicated set of NodePtrs satisfying the given search parameters.
func SolveNodePtrs(sst PoSST, nodenames []string, search SearchParameters, arr []ArrowPtr, limit int) []NodePtr {
	nodeptrs, rest := ParseLiteralNodePtrs(nodenames)

	idempotence := make(map[NodePtr]bool)
	for _, p := range nodeptrs {
		idempotence[p] = true
	}

	for _, textName := range rest {
		matches := sst.KV.SearchNodesByName(textName, limit)
		for _, m := range matches {
			idempotence[m] = true
		}
	}

	result := make([]NodePtr, 0, len(idempotence))
	for ptr := range idempotence {
		result = append(result, ptr)
	}
	return result
}

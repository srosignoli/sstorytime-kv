package SSTorytime

import (
	"fmt"
	"os"
)

// IdempDBAddNode inserts a node into the database idempotently.
// If a node with the same text already exists its NodePtr is reused.
// If the node belongs to a chapter not yet recorded on the existing entry,
// the chapter is appended and the index is updated automatically (shared-node case).
func IdempDBAddNode(sst PoSST, n Node) Node {
	n.L, n.NPtr.Class = StorageClass(n.S)
	ptr := sst.KV.AddNode(n)
	n.NPtr = ptr
	return n
}

// IdempDBAddLink adds a directed link between two nodes (and its inverse).
// Duplicate links are silently ignored.
func IdempDBAddLink(sst PoSST, from Node, link Link, to Node) {
	frptr := from.NPtr
	toptr := to.NPtr
	link.Dst = toptr

	if frptr == toptr {
		fmt.Println("Self-loops are not allowed", from.S, from, link, to)
		os.Exit(-1)
	}

	if link.Wgt == 0 {
		fmt.Println("Attempt to register a link with zero weight is pointless")
		os.Exit(-1)
	}

	// Forward link
	sst.KV.AddLink(frptr, link, toptr)

	// Inverse link (mirrors the INVERSE_ARROWS table in Postgres)
	var invlink Link
	invlink.Arr = INVERSE_ARROWS[link.Arr]
	invlink.Wgt = link.Wgt
	invlink.Dst = frptr
	sst.KV.AddLink(toptr, invlink, frptr)
}

// GetDBNodeByNodePtr retrieves a node by its pointer, returning an empty node
// for sentinel values (NO_NODE_PTR, NONODE).
func GetDBNodeByNodePtr(sst PoSST, db_nptr NodePtr) Node {
	if db_nptr == NO_NODE_PTR || db_nptr == NONODE {
		var n Node
		n.NPtr = db_nptr
		n.S = ""
		return n
	}
	return sst.KV.GetNode(db_nptr)
}

// GetDBArrowByPtr resolves an arrow by pointer from the in-memory cache.
func GetDBArrowByPtr(sst PoSST, arrowptr ArrowPtr) ArrowDirectory {
	for a := range ARROW_DIRECTORY {
		if ARROW_DIRECTORY[a].Ptr == arrowptr {
			return ARROW_DIRECTORY[a]
		}
	}
	var empty ArrowDirectory
	empty.Ptr = -1
	return empty
}

// DeleteChapterFromDB removes all nodes belonging exclusively to the named
// chapter, cleans up dangling links, and updates shared nodes' chapter lists.
// It is safe to call on a chapter that does not exist (returns nil).
//
// Typical usage before re-importing an edited N4L file:
//
//	if err := DeleteChapterFromDB(sst, chapterName); err != nil {
//	    log.Fatal(err)
//	}
//	// … re-parse and re-insert the note file …
func DeleteChapterFromDB(sst PoSST, chapter string) error {
	return sst.KV.DeleteChapter(chapter)
}

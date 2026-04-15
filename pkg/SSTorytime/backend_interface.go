package SSTorytime

// GraphStore is the storage backend abstraction used by all SSTorytime
// operations.  BadgerKV is the production implementation; the interface
// makes it straightforward to swap in a mock or a future backend.
type GraphStore interface {
	// Node lifecycle
	AddNode(n Node) NodePtr
	GetNode(n NodePtr) Node
	IterateNodes(callback func(n Node, ptr NodePtr) bool)

	// Link lifecycle
	AddLink(from NodePtr, link Link, to NodePtr)

	// Chapter management
	//
	// DeleteChapter removes all nodes that belong exclusively to the named
	// chapter and cleans up any links pointing to those nodes in the rest of
	// the graph.  Nodes shared across multiple chapters have the chapter
	// removed from their Chap field and are preserved.  This is the primary
	// mechanism for safely re-submitting an edited N4L file.
	DeleteChapter(chapter string) error

	// Search / index queries
	SearchNodesByName(textName string, limit int) []NodePtr
	GetChapters(chap string, cn []string, limit int) map[string][]string
	GetChapterNames() []string

	// Path traversal
	GetFwdPaths(from NodePtr, depth int) [][]Link

	// Page-map retrieval (stub — populated by higher-level layers)
	GetPageMap(chap string, cn []string, page int) []PageMap

	// Context CRUD
	UploadContext(name string, id ContextPtr) ContextPtr
	GetContextByName(name string) (string, ContextPtr)
	GetContextByPtr(id ContextPtr) (string, ContextPtr)

	// Last-seen tracking
	GetLastSawSection() []LastSeen
	GetLastSawNPtr(nptr NodePtr) LastSeen

	Close()
}

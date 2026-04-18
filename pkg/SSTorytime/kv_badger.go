package SSTorytime

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/dgraph-io/badger/v4"
)

type BadgerKV struct {
	db *badger.DB
}

func OpenBadgerStore(path string) (*BadgerKV, error) {
	opts := badger.DefaultOptions(path)
	opts.Logger = nil // mute badger logs
	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}
	return &BadgerKV{db: db}, nil
}

func (store *BadgerKV) Close() {
	if store.db != nil {
		store.db.Close()
	}
}

// ── Key helpers ───────────────────────────────────────────────────────────────

func nodeKey(ptr NodePtr) []byte {
	return []byte(fmt.Sprintf("node:%d:%d", ptr.Class, ptr.CPtr))
}

func edgeKey(from NodePtr, arr ArrowPtr) []byte {
	return []byte(fmt.Sprintf("edge:%d:%d:%d", from.Class, from.CPtr, arr))
}

// textIndexKey returns the secondary index key for a node's text.
// Lower-cased so lookup and storage are case-insensitive, matching Postgres
// behaviour (lower(s) = lower(iSi)).
func textIndexKey(text string) []byte {
	return []byte("text:" + strings.ToLower(text))
}

// chapIndexKey returns a single chap: index entry for one chapter token.
func chapIndexKey(chapter string, ptr NodePtr) []byte {
	return []byte(fmt.Sprintf("chap:%s:%d:%d", chapter, ptr.Class, ptr.CPtr))
}

// chapIndexKeys returns one chap: key per chapter token.
// Node.Chap may be a comma-separated list when a node is shared across chapters.
func chapIndexKeys(n Node, ptr NodePtr) [][]byte {
	tokens := splitChap(n.Chap)
	keys := make([][]byte, 0, len(tokens))
	for _, tok := range tokens {
		keys = append(keys, chapIndexKey(tok, ptr))
	}
	return keys
}

// splitChap splits a comma-separated chapter string into trimmed, non-empty tokens.
func splitChap(chap string) []string {
	if chap == "" {
		return nil
	}
	parts := strings.Split(chap, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// removeFromChapList returns a new slice with the named chapter removed.
func removeFromChapList(chapters []string, chapter string) []string {
	out := make([]string, 0, len(chapters))
	for _, c := range chapters {
		if c != chapter {
			out = append(out, c)
		}
	}
	return out
}

// ── AddNode (idempotent) ─────────────────────────────────────────────────────

// AddNode inserts a node into BadgerDB idempotently using a text: secondary index.
//
// If a node with the same text (case-insensitive) already exists:
//   - Its existing NodePtr is returned without a new entry being written.
//   - If the caller supplies a new chapter that the existing node does not yet
//     belong to, the node's Chap list and the chap: index are updated atomically.
//     This is the shared-node case handled by Postgres via comma-separated Chap.
//
// If no matching node exists, a new sequence ID is generated and the node is
// written together with its text: and chap: secondary index entries in a single
// atomic transaction.
func (store *BadgerKV) AddNode(n Node) NodePtr {
	n.L, n.NPtr.Class = StorageClass(n.S)

	if n.S != "" {
		tkey := textIndexKey(n.S)

		var existing NodePtr
		var existingNode Node
		found := false

		store.db.View(func(txn *badger.Txn) error {
			item, err := txn.Get(tkey)
			if err != nil {
				return err
			}
			if err := item.Value(func(val []byte) error {
				return json.Unmarshal(val, &existing)
			}); err != nil {
				return err
			}
			found = true

			// Also read the existing node to check its Chap field.
			nodeItem, err := txn.Get(nodeKey(existing))
			if err != nil {
				return err
			}
			return nodeItem.Value(func(val []byte) error {
				return json.Unmarshal(val, &existingNode)
			})
		})

		if found {
			// Node exists — check whether the caller's chapter is new.
			if n.Chap != "" {
				existingChaps := splitChap(existingNode.Chap)
				hasChap := false
				for _, c := range existingChaps {
					if c == n.Chap {
						hasChap = true
						break
					}
				}
				if !hasChap {
					// Add new chapter: update Chap field and write chap: index entry.
					if existingNode.Chap != "" {
						existingNode.Chap = existingNode.Chap + "," + n.Chap
					} else {
						existingNode.Chap = n.Chap
					}
					updatedVal, _ := json.Marshal(existingNode)
					ck := chapIndexKey(n.Chap, existing)
					store.db.Update(func(txn *badger.Txn) error {
						if err := txn.Set(nodeKey(existing), updatedVal); err != nil {
							return err
						}
						return txn.Set(ck, []byte{})
					})
				}
			}
			return existing
		}
	}

	// New node — generate a sequence ID.
	seq, err := store.db.GetSequence([]byte("seq:node"), 1000)
	if err != nil {
		log.Fatal(err)
	}
	defer seq.Release()

	id, _ := seq.Next()
	ptr := NodePtr{Class: n.NPtr.Class, CPtr: ClassedNodePtr(id)}
	n.NPtr = ptr

	nodeVal, _ := json.Marshal(n)
	ptrVal, _ := json.Marshal(ptr)

	store.db.Update(func(txn *badger.Txn) error {
		// Primary node entry
		if err := txn.Set(nodeKey(ptr), nodeVal); err != nil {
			return err
		}
		// Text secondary index
		if n.S != "" {
			if err := txn.Set(textIndexKey(n.S), ptrVal); err != nil {
				return err
			}
		}
		// Chapter secondary index (one entry per chapter token)
		for _, ck := range chapIndexKeys(n, ptr) {
			if err := txn.Set(ck, []byte{}); err != nil {
				return err
			}
		}
		return nil
	})

	return ptr
}

// ── AddLink (idempotent) ─────────────────────────────────────────────────────

func (store *BadgerKV) AddLink(from NodePtr, link Link, to NodePtr) {
	key := edgeKey(from, link.Arr)

	store.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		var destinations []NodePtr

		if err == nil {
			item.Value(func(val []byte) error {
				return json.Unmarshal(val, &destinations)
			})
		}

		for _, existing := range destinations {
			if existing == to {
				return nil // already present — idempotent no-op
			}
		}
		destinations = append(destinations, to)
		val, _ := json.Marshal(destinations)
		return txn.Set(key, val)
	})
}

// ── GetNode ──────────────────────────────────────────────────────────────────

func (store *BadgerKV) GetNode(n NodePtr) Node {
	var node Node
	store.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(nodeKey(n))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &node)
		})
	})
	return node
}

// ── DeleteChapter ─────────────────────────────────────────────────────────────
//
// DeleteChapter removes all nodes that belong exclusively to the given chapter
// and cleans up any links that point to those deleted nodes in the surviving graph.
//
// Nodes shared across multiple chapters have the chapter removed from their
// Chap list and are preserved — exactly mirroring the PostgreSQL DeleteChapter()
// stored procedure, but implemented as pure Go with BadgerDB primitives:
//
//  1. O(log n) prefix scan on the chap: secondary index (vs full-table LIKE scan).
//  2. Single WriteBatch for all deletes and updates (vs multiple SQL UPDATE rounds).
//  3. No network round-trips, no SQL parsing, no row-level locking.

func (store *BadgerKV) DeleteChapter(chapter string) error {
	// ── Step 1: Collect all NodePtrs in this chapter via prefix scan ──────────
	prefix := []byte(fmt.Sprintf("chap:%s:", chapter))
	chapPrefixStr := fmt.Sprintf("chap:%s:", chapter)

	var allPtrs []NodePtr
	err := store.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false // keys only — no value needed for the index
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			key := string(it.Item().Key())
			suffix := key[len(chapPrefixStr):]
			var class, cptr int
			fmt.Sscanf(suffix, "%d:%d", &class, &cptr)
			allPtrs = append(allPtrs, NodePtr{Class: class, CPtr: ClassedNodePtr(cptr)})
		}
		return nil
	})
	if err != nil || len(allPtrs) == 0 {
		return err
	}

	// ── Step 2: Read node data; classify exclusive vs shared ──────────────────
	deletePtrs := make(map[NodePtr]bool) // will be deleted
	updateNodes := make(map[NodePtr]Node) // Chap field reduced, node kept
	nodeCache := make(map[NodePtr]Node)   // all nodes read in this step

	err = store.db.View(func(txn *badger.Txn) error {
		for _, ptr := range allPtrs {
			item, err := txn.Get(nodeKey(ptr))
			if err != nil {
				continue
			}
			var n Node
			item.Value(func(val []byte) error {
				return json.Unmarshal(val, &n)
			})
			nodeCache[ptr] = n

			chaps := splitChap(n.Chap)
			if len(chaps) <= 1 {
				deletePtrs[ptr] = true
			} else {
				n.Chap = strings.Join(removeFromChapList(chaps, chapter), ",")
				updateNodes[ptr] = n
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	// ── Step 3: Scan all nodes for dangling links pointing to deletePtrs ──────
	//
	// For every surviving node, filter out any Link whose Dst is in deletePtrs.
	// This mirrors the per-channel link-array cleaning in the Postgres function.
	type nodeWrite struct {
		ptr  NodePtr
		node Node
	}
	var writes []nodeWrite
	seenInWrites := make(map[NodePtr]bool)

	store.db.View(func(txn *badger.Txn) error {
		nodePrefix := []byte("node:")
		opts := badger.DefaultIteratorOptions
		opts.Prefix = nodePrefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(nodePrefix); it.ValidForPrefix(nodePrefix); it.Next() {
			item := it.Item()
			key := string(item.Key())
			var class, cptr int
			fmt.Sscanf(key, "node:%d:%d", &class, &cptr)
			ptr := NodePtr{Class: class, CPtr: ClassedNodePtr(cptr)}

			if deletePtrs[ptr] {
				continue // being deleted — skip
			}

			// Use the pre-read/updated node when available, otherwise read fresh.
			var n Node
			if updated, ok := updateNodes[ptr]; ok {
				n = updated
			} else if cached, ok := nodeCache[ptr]; ok {
				n = cached
			} else {
				item.Value(func(val []byte) error {
					return json.Unmarshal(val, &n)
				})
			}

			// Check whether any link slot references a node being deleted.
			dirty := false
			for st := 0; st < ST_TOP; st++ {
				for _, lnk := range n.I[st] {
					if deletePtrs[lnk.Dst] {
						dirty = true
						break
					}
				}
				if dirty {
					break
				}
			}

			needsWrite := false
			if dirty {
				for st := 0; st < ST_TOP; st++ {
					filtered := make([]Link, 0, len(n.I[st]))
					for _, lnk := range n.I[st] {
						if !deletePtrs[lnk.Dst] {
							filtered = append(filtered, lnk)
						}
					}
					n.I[st] = filtered
				}
				needsWrite = true
			}
			// A shared node also needs its Chap update written even if no links changed.
			if _, ok := updateNodes[ptr]; ok {
				needsWrite = true
			}

			if needsWrite {
				writes = append(writes, nodeWrite{ptr, n})
				seenInWrites[ptr] = true
			}
		}
		return nil
	})

	// Catch any shared nodes the scan above didn't visit (safety net).
	for ptr, node := range updateNodes {
		if !seenInWrites[ptr] {
			writes = append(writes, nodeWrite{ptr, node})
		}
	}

	// ── Step 4: Apply all changes in a single WriteBatch ─────────────────────
	wb := store.db.NewWriteBatch()
	defer wb.Cancel()

	// Write updated surviving nodes.
	for _, w := range writes {
		val, _ := json.Marshal(w.node)
		if err := wb.Set(nodeKey(w.ptr), val); err != nil {
			return err
		}
	}

	// Delete exclusive nodes + their index entries.
	for ptr := range deletePtrs {
		if err := wb.Delete(nodeKey(ptr)); err != nil {
			return err
		}
		if n, ok := nodeCache[ptr]; ok && n.S != "" {
			if err := wb.Delete(textIndexKey(n.S)); err != nil {
				return err
			}
		}
		if err := wb.Delete(chapIndexKey(chapter, ptr)); err != nil {
			return err
		}
	}

	// Remove the chapter's chap: index entries for shared nodes.
	for ptr := range updateNodes {
		if err := wb.Delete(chapIndexKey(chapter, ptr)); err != nil {
			return err
		}
	}

	return wb.Flush()
}

// ── SearchNodesByName ─────────────────────────────────────────────────────────

// SearchNodesByName returns NodePtrs whose stored text contains textName
// (case-insensitive substring match).  Uses the text: secondary index so only
// index keys are scanned — no full node-value reads until a match is found.
func (store *BadgerKV) SearchNodesByName(textName string, limit int) []NodePtr {
	var results []NodePtr
	query := strings.ToLower(textName)
	prefix := []byte("text:")

	store.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			key := string(it.Item().Key())
			text := key[len("text:"):]
			if !strings.Contains(text, query) {
				continue
			}
			var ptr NodePtr
			if err := it.Item().Value(func(val []byte) error {
				return json.Unmarshal(val, &ptr)
			}); err != nil {
				continue
			}
			results = append(results, ptr)
			if limit > 0 && len(results) >= limit {
				return fmt.Errorf("limit reached")
			}
		}
		return nil
	})

	return results
}

// ── GetChapters ───────────────────────────────────────────────────────────────

// GetChapters returns a map of chapter-name → []node-text using the chap:
// secondary index (O(log n) prefix scan).
//
// If chap is non-empty only that specific chapter is returned.
// If cn is non-nil only chapters in that list are included.
// limit caps the total number of chapters returned (0 = unlimited).
func (store *BadgerKV) GetChapters(chap string, cn []string, limit int) map[string][]string {
	result := make(map[string][]string)

	cnSet := make(map[string]bool, len(cn))
	for _, c := range cn {
		cnSet[c] = true
	}

	var prefix []byte
	if chap != "" {
		prefix = []byte(fmt.Sprintf("chap:%s:", chap))
	} else {
		prefix = []byte("chap:")
	}

	store.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			key := string(it.Item().Key())
			// key format: chap:<chapter>:<class>:<cptr>
			rest := key[len("chap:"):]
			// Split into [chapter, class, cptr] — chapter itself may not contain ":"
			// but class and cptr are plain integers, so we split from the right.
			lastColon := strings.LastIndex(rest, ":")
			if lastColon < 0 {
				continue
			}
			secondLastColon := strings.LastIndex(rest[:lastColon], ":")
			if secondLastColon < 0 {
				continue
			}
			chapName := rest[:secondLastColon]
			classCptr := rest[secondLastColon+1:]

			if len(cnSet) > 0 && !cnSet[chapName] {
				continue
			}
			if limit > 0 && len(result) >= limit {
				if _, exists := result[chapName]; !exists {
					return fmt.Errorf("limit reached")
				}
			}

			var class, cptr int
			fmt.Sscanf(classCptr, "%d:%d", &class, &cptr)
			ptr := NodePtr{Class: class, CPtr: ClassedNodePtr(cptr)}

			node := store.GetNode(ptr)
			result[chapName] = append(result[chapName], node.S)
		}
		return nil
	})

	return result
}

// GetChapterNames returns a sorted list of all distinct chapter names in the DB.
// Uses the chap: index — no full node scan required.
func (store *BadgerKV) GetChapterNames() []string {
	seen := make(map[string]bool)
	prefix := []byte("chap:")

	store.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			key := string(it.Item().Key())
			rest := key[len("chap:"):]
			// Extract chapter name: everything before <class>:<cptr> suffix
			lastColon := strings.LastIndex(rest, ":")
			if lastColon < 0 {
				continue
			}
			secondLastColon := strings.LastIndex(rest[:lastColon], ":")
			if secondLastColon < 0 {
				continue
			}
			seen[rest[:secondLastColon]] = true
		}
		return nil
	})

	names := make([]string, 0, len(seen))
	for k := range seen {
		names = append(names, k)
	}
	return names
}

// ── IterateNodes ──────────────────────────────────────────────────────────────

func (store *BadgerKV) IterateNodes(callback func(n Node, ptr NodePtr) bool) {
	store.db.View(func(txn *badger.Txn) error {
		prefix := []byte("node:")
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			key := string(item.Key())
			var class, cptr int
			fmt.Sscanf(key, "node:%d:%d", &class, &cptr)

			ptr := NodePtr{Class: class, CPtr: ClassedNodePtr(cptr)}

			err := item.Value(func(val []byte) error {
				var n Node
				json.Unmarshal(val, &n)
				if !callback(n, ptr) {
					return fmt.Errorf("break")
				}
				return nil
			})
			if err != nil {
				break
			}
		}
		return nil
	})
}

// ── Context persistence ───────────────────────────────────────────────────────
//
// Contexts are stored under two complementary index keys so both lookup
// directions are O(log n):
//
//	ctx:name:<context_name>  →  JSON-encoded ContextPtr
//	ctx:ptr:<decimal_id>     →  JSON-encoded context name string
//
// This mirrors the PostgreSQL contextdirectory table and enables
// GetDBContextByName / GetDBContextByPtr to work correctly across sessions.

func ctxNameKey(name string) []byte {
	return []byte("ctx:name:" + name)
}

func ctxPtrKey(id ContextPtr) []byte {
	return []byte(fmt.Sprintf("ctx:ptr:%d", id))
}

// UploadContext persists a (name, id) context pair to BadgerDB.
// If a context with the same name already exists its id is returned unchanged.
func (store *BadgerKV) UploadContext(name string, id ContextPtr) ContextPtr {
	if name == "" {
		return id
	}
	nkey := ctxNameKey(name)
	pkey := ctxPtrKey(id)

	// Check whether this name is already stored.
	var existingID ContextPtr
	found := false
	store.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(nkey)
		if err != nil {
			return err
		}
		found = true
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &existingID)
		})
	})
	if found {
		return existingID
	}

	idVal, _ := json.Marshal(id)
	nameVal, _ := json.Marshal(name)
	store.db.Update(func(txn *badger.Txn) error {
		if err := txn.Set(nkey, idVal); err != nil {
			return err
		}
		return txn.Set(pkey, nameVal)
	})
	return id
}

// GetContextByName looks up a context name and returns its ContextPtr.
// Returns (name, 0) if not found (preserving stub behaviour as fallback).
func (store *BadgerKV) GetContextByName(name string) (string, ContextPtr) {
	if name == "" {
		return name, 0
	}
	var id ContextPtr
	found := false
	store.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(ctxNameKey(name))
		if err != nil {
			return err
		}
		found = true
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &id)
		})
	})
	if found {
		return name, id
	}
	return name, -1
}

// GetContextByPtr looks up a ContextPtr and returns its name.
// Returns ("any", id) if not found (preserving stub behaviour as fallback).
func (store *BadgerKV) GetContextByPtr(id ContextPtr) (string, ContextPtr) {
	var name string
	found := false
	store.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(ctxPtrKey(id))
		if err != nil {
			return err
		}
		found = true
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &name)
		})
	})
	if found {
		return name, id
	}
	return "any", id
}

// ── Remaining stubs ───────────────────────────────────────────────────────────

func (store *BadgerKV) GetFwdPaths(from NodePtr, depth int) [][]Link {
	return [][]Link{}
}

func (store *BadgerKV) GetPageMap(chap string, cn []string, page int) []PageMap {
	return []PageMap{}
}

func (store *BadgerKV) GetLastSawSection() []LastSeen {
	return []LastSeen{}
}

func (store *BadgerKV) GetLastSawNPtr(nptr NodePtr) LastSeen {
	var ls LastSeen
	return ls
}

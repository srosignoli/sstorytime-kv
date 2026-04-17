package main

import (
	"os"
	"regexp"
)

func main() {
	b, err := os.ReadFile("pkg/SSTorytime/SSTorytime.go")
	if err != nil {
		panic(err)
	}
	s := string(b)

	// Remove Postgres Imports
	s = regexp.MustCompile(`"database/sql"\n`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`_ "github\.com/lib/pq"\n`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`"io/ioutil"\n`).ReplaceAllString(s, "")
    // Add store import
	s = regexp.MustCompile(`"math"\n`).ReplaceAllString(s, "\"math\"\n\tstore \"github.com/srosignoli/sstorytime-kv/pkg/store\"\n")

	// Remove DB *sql.DB from PoSST
	s = regexp.MustCompile(`	DB \*sql\.DB\n\tKV GraphStore\n`).ReplaceAllString(s, "\tKV GraphStore\n")

	// Rewrite Open
	OpenImpl := `func Open(load_arrows bool) PoSST {
	var sst PoSST

	importStore, err := store.OpenBadgerStore("sst_data")
	if err != nil {
		fmt.Println("Error opening BadgerKV:", err)
		os.Exit(-1)
	}
	sst.KV = importStore

	MemoryInit()
	Configure(sst, load_arrows)

	NO_NODE_PTR.Class = 0
	NO_NODE_PTR.CPtr = -1
	NONODE.Class = 0
	NONODE.CPtr = 0

	return sst
}`
	s = regexp.MustCompile(`(?s)func Open\(load_arrows bool\) PoSST \{.*?\n\}`).ReplaceAllString(s, OpenImpl)

	// Erase Configure, OverrideCredentials, Close
	s = regexp.MustCompile(`(?s)func Configure\(sst PoSST,load_arrows bool\) \{.*?\n\}`).ReplaceAllString(s, "func Configure(sst PoSST,load_arrows bool) {}")
	s = regexp.MustCompile(`(?s)func OverrideCredentials\(u, p, d string\) \(string, string, string\) \{.*?\n\}`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?s)func Close\(sst PoSST\) \{.*?\n\}`).ReplaceAllString(s, "func Close(sst PoSST) {\n\tsst.KV.Close()\n}")

	os.WriteFile("pkg/SSTorytime/SSTorytime.go", []byte(s), 0644)
}

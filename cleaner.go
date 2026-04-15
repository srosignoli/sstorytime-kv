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

	// Step 1: Replace PoSST SQL DB with dual interface to suppress compiler breakages while we dissect
	reDB := regexp.MustCompile(`DB \*sql\.DB\n`)
	s = reDB.ReplaceAllString(s, "DB *sql.DB\n\tKV GraphStore\n")

	// Step 2: List of all 12 functions extracted natively across Steps 1, 2, and 3
	funcs := []string{
		"IdempDBAddNode",
		"IdempDBAddLink",
		"GetDBNodeByNodePtr",
		"GetDBArrowByPtr",
		"GetDBPageMap",
		"GetChaptersByChapContext",
		"SolveNodePtrs",
		"GetDBNodePtrMatchingName",
		"GetDBNodePtrMatchingNCCS",
		"NodeWhereString",
		"GetDBChaptersMatchingName",
		"GetFwdPathsAsLinks",
	}

	for _, f := range funcs {
		// Target precisely `\nfunc Name(anything)\n` up until the very first `\n// ****` delimiter 
		// that separates ALL functions uniformly in the monolith. 
		// `(?s)` allows `.` to match newlines cleanly.
		reFunc := regexp.MustCompile(`(?s)\nfunc ` + f + `\(.*?\n// \*\*\*\*+`)
		s = reFunc.ReplaceAllString(s, "\n// **************************************************************************")
	}

	os.WriteFile("pkg/SSTorytime/SSTorytime.go", []byte(s), 0644)
}

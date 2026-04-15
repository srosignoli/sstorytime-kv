package main

import (
	"os"
	"strings"
)

func main() {
	b, err := os.ReadFile("pkg/SSTorytime/SSTorytime.go")
	if err != nil {
		panic(err)
	}
	content := string(b)

	funcs := []string{
		"SolveNodePtrs",
		"GetDBNodePtrMatchingName",
		"GetDBNodePtrMatchingNCCS",
		"NodeWhereString",
		"GetDBChaptersMatchingName",
		"GetFwdPathsAsLinks",
	}

	for _, fname := range funcs {
		lines := strings.Split(content, "\n")
		var out []string
		deleting := false
		for _, line := range lines {
			if strings.HasPrefix(line, "func "+fname+"(") {
				deleting = true
			}
			if !deleting {
				out = append(out, line)
			}
			if deleting && line == "}" {
				deleting = false
			}
		}
		content = strings.Join(out, "\n")
	}

	os.WriteFile("pkg/SSTorytime/SSTorytime.go", []byte(content), 0644)
}

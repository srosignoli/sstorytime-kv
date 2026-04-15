package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
)

func main() {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "pkg/SSTorytime/SSTorytime.go", nil, 0)
	if err != nil {
		panic(err)
	}

	count := 0
	for _, decl := range f.Decls {
		if fn, isFn := decl.(*ast.FuncDecl); isFn && fn.Body != nil {
			for _, stmt := range fn.Body.List {
				if expr, isExpr := stmt.(*ast.ExprStmt); isExpr {
					if call, isCall := expr.X.(*ast.CallExpr); isCall {
						if id, isId := call.Fun.(*ast.Ident); isId && id.Name == "panic" {
							fmt.Println(fn.Name.Name)
							count++
						}
					}
				}
			}
		}
	}
	fmt.Printf("Total stubbed functions: %d\n", count)
}

// Command psdkit-web-docs extracts original public Go contracts for the browser capability catalog.
package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	result := map[string]any{}
	for _, directory := range []string{".", "psdfile", "psdedit", "psdfont", "psdwarp", "psdfilter",
		"psdvector", "psdgradient", "psdpattern", "psdeffects", "psdadjust", "psdcolor", "psdrender"} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(directory, entry.Name()), nil, parser.ParseComments)
			if err != nil {
				return err
			}
			packageName := file.Name.Name
			for _, declaration := range file.Decls {
				function, valid := declaration.(*ast.FuncDecl)
				if !valid || !function.Name.IsExported() {
					continue
				}
				key := packageName + "."
				if function.Recv != nil {
					receiver := function.Recv.List[0].Type
					if pointer, valid := receiver.(*ast.StarExpr); valid {
						receiver = pointer.X
					}
					identifier, valid := receiver.(*ast.Ident)
					if !valid {
						continue
					}
					key += identifier.Name + "."
				}
				key += function.Name.Name
				names := []string{}
				for _, field := range function.Type.Params.List {
					for _, name := range field.Names {
						names = append(names, name.Name)
					}
					if len(field.Names) == 0 {
						names = append(names, "argument")
					}
				}
				description := ""
				if function.Doc != nil {
					description = strings.TrimSpace(function.Doc.Text())
				}
				result[key] = map[string]any{"Description": description, "Parameters": names}
			}
		}
	}
	data, err := json.Marshal(result, jsontext.WithIndent("  "), json.Deterministic(true))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join("psdweb", "api-docs.json"), append(data, '\n'), 0644)
}

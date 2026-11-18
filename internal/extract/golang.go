package extract

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
)

type goExtractor struct{}

func (goExtractor) Language() string { return "go" }

func (goExtractor) Extract(path string, source []byte) Result {
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, path, source, parser.ParseComments)
	if err != nil {
		return Result{EnvVars: EnvVars("go", path, source)}
	}
	var symbols []Symbol
	add := func(name, kind, parent, signature string, doc *ast.CommentGroup, position token.Pos) {
		symbols = append(symbols, Symbol{
			Name: name, Kind: kind, Language: "go", File: path, Line: files.Position(position).Line,
			Parent: parent, Signature: signature, Doc: strings.TrimSpace(doc.Text()), Exported: ast.IsExported(name),
		})
	}
	symbols = append(symbols, Symbol{
		Name: file.Name.Name, Kind: "package", Language: "go", File: path, Line: files.Position(file.Package).Line,
		Doc: strings.TrimSpace(file.Doc.Text()), Exported: true,
	})
	for _, declaration := range file.Decls {
		switch decl := declaration.(type) {
		case *ast.FuncDecl:
			kind, parent := "function", ""
			if decl.Recv != nil && len(decl.Recv.List) > 0 {
				kind, parent = "method", receiverName(decl.Recv.List[0].Type)
			}
			add(decl.Name.Name, kind, parent, render(files, &ast.FuncDecl{Recv: decl.Recv, Name: decl.Name, Type: decl.Type}), decl.Doc, decl.Pos())
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					doc := spec.Doc
					if doc == nil {
						doc = decl.Doc
					}
					kind := "type"
					switch spec.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}
					add(spec.Name.Name, kind, "", "type "+spec.Name.Name, doc, spec.Pos())
				case *ast.ValueSpec:
					doc := spec.Doc
					if doc == nil {
						doc = decl.Doc
					}
					kind := "variable"
					if decl.Tok == token.CONST {
						kind = "constant"
					}
					for _, name := range spec.Names {
						if name.Name != "_" {
							add(name.Name, kind, "", "", doc, name.Pos())
						}
					}
				}
			}
		}
	}
	return Result{Symbols: symbols, EnvVars: EnvVars("go", path, source)}
}

func receiverName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.StarExpr:
		return receiverName(value.X)
	case *ast.Ident:
		return value.Name
	case *ast.IndexExpr:
		return receiverName(value.X)
	case *ast.IndexListExpr:
		return receiverName(value.X)
	}
	return ""
}

func render(files *token.FileSet, node any) string {
	var buffer bytes.Buffer
	if err := printer.Fprint(&buffer, files, node); err != nil {
		return ""
	}
	return strings.Join(strings.Fields(buffer.String()), " ")
}

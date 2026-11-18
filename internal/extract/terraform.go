package extract

import (
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

type terraformExtractor struct{}

func (terraformExtractor) Language() string { return "terraform" }

// Extract records variables, outputs, resources, data sources, modules, providers and
// locals. Variable and output descriptions become their docs. A .tfvars file gives its
// values, a Terragrunt file its dependencies and inputs, and a test file its runs.
func (terraformExtractor) Extract(path string, source []byte) Result {
	file, diagnostics := hclsyntax.ParseConfig(source, path, hcl.InitialPos)
	result := Result{EnvVars: EnvVars("terraform", path, source)}
	if diagnostics.HasErrors() || file == nil {
		return result
	}
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		return result
	}
	lines := strings.Split(string(source), "\n")
	// Top-level attributes: a .tfvars file's values, Terragrunt's inputs map.
	for name, attribute := range body.Attributes {
		line := attribute.SrcRange.Start.Line
		switch {
		case strings.HasSuffix(path, ".tfvars"):
			result.Symbols = append(result.Symbols, Symbol{Name: name, Kind: "tfvar", Language: "terraform", File: path,
				Line: line, Exported: true, Doc: commentBlock(lines, line-1, "#", "//"),
				Signature: "= " + strings.Join(strings.Fields(string(attribute.Expr.Range().SliceBytes(source))), " ")})
		case name == "inputs":
			if object, ok := attribute.Expr.(*hclsyntax.ObjectConsExpr); ok {
				for _, item := range object.Items {
					key := strings.Trim(string(item.KeyExpr.Range().SliceBytes(source)), `"`)
					result.Symbols = append(result.Symbols, Symbol{Name: key, Kind: "input", Language: "terraform", File: path,
						Line: item.KeyExpr.Range().Start.Line, Exported: true})
				}
			}
		}
	}
	for _, block := range body.Blocks {
		line := block.TypeRange.Start.Line
		doc := commentBlock(lines, line-1, "#", "//")
		symbol := Symbol{Kind: block.Type, Language: "terraform", File: path, Line: line, Exported: true, Doc: doc}
		switch block.Type {
		case "variable", "output", "module", "provider":
			if len(block.Labels) > 0 {
				symbol.Name = block.Labels[0]
			}
			if description := stringAttribute(block.Body, "description"); description != "" {
				symbol.Doc = description
			}
			if block.Type == "variable" {
				symbol.Signature = variableSignature(block.Body, source)
			}
			if block.Type == "module" {
				symbol.Signature = "source = " + stringAttribute(block.Body, "source")
			}
		case "dependency", "run": // Terragrunt dependency, Terraform test run
			if len(block.Labels) > 0 {
				symbol.Name = block.Labels[0]
			}
			if block.Type == "dependency" {
				symbol.Signature = "config_path = " + stringAttribute(block.Body, "config_path")
			}
		case "resource", "data":
			if len(block.Labels) == 2 {
				symbol.Name = block.Labels[0] + "." + block.Labels[1]
				if block.Type == "data" {
					symbol.Name = "data." + symbol.Name
				}
			}
		case "locals":
			for name, attribute := range block.Body.Attributes {
				result.Symbols = append(result.Symbols, Symbol{
					Name: "local." + name, Kind: "local", Language: "terraform", File: path,
					Line: attribute.SrcRange.Start.Line, Exported: false,
				})
			}
			continue
		default:
			continue
		}
		if symbol.Name != "" {
			result.Symbols = append(result.Symbols, symbol)
		}
	}
	return result
}

func stringAttribute(body *hclsyntax.Body, name string) string {
	attribute, ok := body.Attributes[name]
	if !ok {
		return ""
	}
	value, diagnostics := attribute.Expr.Value(nil)
	if diagnostics.HasErrors() || value.Type() != cty.String || value.IsNull() {
		return ""
	}
	return value.AsString()
}

// variableSignature shows the declared type and default as written.
func variableSignature(body *hclsyntax.Body, source []byte) string {
	var parts []string
	for _, name := range []string{"type", "default"} {
		if attribute, ok := body.Attributes[name]; ok {
			text := string(attribute.Expr.Range().SliceBytes(source))
			parts = append(parts, name+" = "+strings.Join(strings.Fields(text), " "))
		}
	}
	return strings.Join(parts, ", ")
}

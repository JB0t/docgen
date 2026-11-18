package extract

import (
	"regexp"
	"strings"
)

// Line-based extractors for languages without a cheap Go parser. They look for
// declaration lines and take the comment block directly above as the doc.

type bashExtractor struct{}

var (
	bashFunctionRE = regexp.MustCompile(`^\s*(?:function\s+([A-Za-z_][\w:.-]*)\s*(?:\(\s*\))?|([A-Za-z_][\w:.-]*)\s*\(\s*\))\s*\{?`)
	bashFlagRE     = regexp.MustCompile(`^\s*\|?\s*((?:-[A-Za-z0-9]\|)?--?[A-Za-z][\w-]*(?:\|--?[A-Za-z][\w-]*)*)\)`)
)

func (bashExtractor) Language() string { return "bash" }

func (bashExtractor) Extract(path string, source []byte) Result {
	lines := strings.Split(string(source), "\n")
	var symbols []Symbol
	// The script itself, documented by its header comment.
	symbols = append(symbols, Symbol{Name: baseName(path), Kind: "script", Language: "bash", File: path, Line: 1,
		Doc: headerComment(lines, "#"), Exported: true})
	seenFlags := map[string]bool{}
	for index, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if match := bashFunctionRE.FindStringSubmatch(line); match != nil {
			name := match[1] + match[2]
			symbols = append(symbols, Symbol{Name: name, Kind: "function", Language: "bash", File: path, Line: index + 1,
				Doc: commentBlock(lines, index, "#"), Exported: !strings.HasPrefix(name, "_")})
			continue
		}
		// case "$1" in --flag|-f) ... : flags the script accepts.
		if match := bashFlagRE.FindStringSubmatch(line); match != nil {
			for _, flag := range strings.Split(match[1], "|") {
				if !seenFlags[flag] {
					seenFlags[flag] = true
					symbols = append(symbols, Symbol{Name: flag, Kind: "flag", Language: "bash", File: path, Line: index + 1,
						Parent: baseName(path), Exported: true})
				}
			}
		}
	}
	return Result{Symbols: symbols, EnvVars: EnvVars("bash", path, source)}
}

type csharpExtractor struct{}

var (
	csTypeRE   = regexp.MustCompile(`^\s*(?:\[[^\]]*\]\s*)*((?:public|internal|protected|private)\s+)?(?:(?:static|abstract|sealed|partial|readonly|ref|unsafe|new)\s+)*(class|interface|record|struct|enum)\s+([A-Za-z_]\w*)`)
	csMethodRE = regexp.MustCompile(`^\s*(public|protected|internal)\s+(?:(?:static|virtual|override|abstract|async|sealed|extern|new|partial|unsafe)\s+)*([\w<>\[\],.? ]+?)\s+([A-Za-z_]\w*)\s*(?:<[^>]*>)?\s*\(([^)]*)\)?`)
	csNSRE     = regexp.MustCompile(`^\s*namespace\s+([\w.]+)`)
	xmlTagRE   = regexp.MustCompile(`<[^>]+>`)
)

func (csharpExtractor) Language() string { return "csharp" }

func (csharpExtractor) Extract(path string, source []byte) Result {
	lines := strings.Split(string(source), "\n")
	var symbols []Symbol
	currentType := ""
	for index, line := range lines {
		doc := func() string {
			return strings.TrimSpace(xmlTagRE.ReplaceAllString(commentBlock(lines, index, "///"), ""))
		}
		if match := csNSRE.FindStringSubmatch(line); match != nil {
			symbols = append(symbols, Symbol{Name: match[1], Kind: "namespace", Language: "csharp", File: path, Line: index + 1, Exported: true})
			continue
		}
		if match := csTypeRE.FindStringSubmatch(line); match != nil {
			currentType = match[3]
			symbols = append(symbols, Symbol{Name: match[3], Kind: match[2], Language: "csharp", File: path, Line: index + 1,
				Doc: doc(), Exported: strings.TrimSpace(match[1]) == "public", Signature: strings.TrimSpace(line)})
			continue
		}
		if match := csMethodRE.FindStringSubmatch(line); match != nil && match[2] != "class" && !strings.Contains(line, "=") {
			kind := "method"
			if match[3] == currentType {
				kind = "constructor"
			}
			symbols = append(symbols, Symbol{Name: match[3], Kind: kind, Language: "csharp", File: path, Line: index + 1,
				Parent: currentType, Doc: doc(), Exported: match[1] == "public",
				Signature: strings.TrimSuffix(strings.TrimSpace(line), "{")})
		}
	}
	return Result{Symbols: symbols, EnvVars: EnvVars("csharp", path, source)}
}

type rustExtractor struct{}

var (
	rustItemRE = regexp.MustCompile(`^\s*(pub(?:\([^)]*\))?\s+)?(?:(?:async|const|unsafe|extern\s+"[^"]*")\s+)*(fn|struct|enum|trait|mod|type|const|static|macro_rules!)\s*([A-Za-z_]\w*)`)
	rustImplRE = regexp.MustCompile(`^\s*impl(?:<[^>]*>)?\s+(?:([\w:]+)(?:<[^>]*>)?\s+for\s+)?([\w:]+)`)
)

func (rustExtractor) Language() string { return "rust" }

func (rustExtractor) Extract(path string, source []byte) Result {
	lines := strings.Split(string(source), "\n")
	var symbols []Symbol
	if doc := moduleDoc(lines, "//!"); doc != "" {
		symbols = append(symbols, Symbol{Name: baseName(path), Kind: "module", Language: "rust", File: path, Line: 1, Doc: doc, Exported: true})
	}
	currentImpl := ""
	for index, line := range lines {
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			currentImpl = "" // back at top level
		}
		if match := rustImplRE.FindStringSubmatch(line); match != nil {
			currentImpl = match[2]
			continue
		}
		if match := rustItemRE.FindStringSubmatch(line); match != nil {
			kind := match[2]
			parent := ""
			if kind == "fn" {
				kind = "function"
				if currentImpl != "" {
					kind, parent = "method", currentImpl
				}
			}
			symbols = append(symbols, Symbol{Name: match[3], Kind: strings.TrimSuffix(kind, "!"), Language: "rust", File: path,
				Line: index + 1, Parent: parent, Doc: commentBlock(lines, index, "///"), Exported: match[1] != "",
				Signature: strings.TrimSuffix(strings.TrimSpace(line), "{")})
		}
	}
	return Result{Symbols: symbols, EnvVars: EnvVars("rust", path, source)}
}

type rExtractor struct{}

var (
	rFunctionRE = regexp.MustCompile(`^([A-Za-z.][\w.]*)\s*(?:<-|=)\s*function\s*\(([^)]*)\)?`)
	roxygenTag  = regexp.MustCompile(`(?m)^@\w+.*$`)
)

func (rExtractor) Language() string { return "r" }

func (rExtractor) Extract(path string, source []byte) Result {
	lines := strings.Split(string(source), "\n")
	var symbols []Symbol
	for index, line := range lines {
		if match := rFunctionRE.FindStringSubmatch(line); match != nil {
			doc := commentBlock(lines, index, "#'")
			symbols = append(symbols, Symbol{Name: match[1], Kind: "function", Language: "r", File: path, Line: index + 1,
				Doc:       strings.TrimSpace(roxygenTag.ReplaceAllString(doc, "")),
				Exported:  strings.Contains(doc, "@export") || !strings.HasPrefix(match[1], "."),
				Signature: strings.TrimSpace(match[1] + "(" + match[2] + ")")})
		}
	}
	return Result{Symbols: symbols, EnvVars: EnvVars("r", path, source)}
}

// pythonRegexExtractor is the fallback when python3 is not available to run the ast
// extractor.
type pythonRegexExtractor struct{}

var pyDefRE = regexp.MustCompile(`^(\s*)(?:async\s+)?(def|class)\s+([A-Za-z_]\w*)\s*(\([^)]*\)?)?`)

func (pythonRegexExtractor) Language() string { return "python" }

func (pythonRegexExtractor) Extract(path string, source []byte) Result {
	lines := strings.Split(string(source), "\n")
	var symbols []Symbol
	currentClass := ""
	for index, line := range lines {
		match := pyDefRE.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		indent, kind, name := match[1], match[2], match[3]
		symbol := Symbol{Name: name, Language: "python", File: path, Line: index + 1, Exported: !strings.HasPrefix(name, "_"),
			Signature: strings.TrimSuffix(strings.TrimSpace(line), ":")}
		switch {
		case kind == "class":
			symbol.Kind, currentClass = "class", name
		case indent != "" && currentClass != "":
			symbol.Kind, symbol.Parent = "method", currentClass
		default:
			symbol.Kind, currentClass = "function", ""
		}
		symbols = append(symbols, symbol)
	}
	return Result{Symbols: symbols, EnvVars: EnvVars("python", path, source)}
}

func baseName(path string) string {
	if index := strings.LastIndex(path, "/"); index >= 0 {
		path = path[index+1:]
	}
	return path
}

// headerComment is the comment block at the top of a script, after any shebang.
func headerComment(lines []string, prefix string) string {
	var block []string
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if index == 0 && strings.HasPrefix(trimmed, "#!") {
			continue
		}
		if !strings.HasPrefix(trimmed, prefix) {
			if trimmed == "" && len(block) == 0 {
				continue
			}
			break
		}
		block = append(block, strings.TrimSpace(strings.TrimPrefix(trimmed, prefix)))
	}
	return strings.TrimSpace(strings.Join(block, "\n"))
}

func moduleDoc(lines []string, prefix string) string {
	var block []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) {
			block = append(block, strings.TrimSpace(strings.TrimPrefix(trimmed, prefix)))
		} else if trimmed != "" {
			break
		}
	}
	return strings.TrimSpace(strings.Join(block, "\n"))
}

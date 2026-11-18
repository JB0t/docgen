package code

import (
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/JB0t/docgen/internal/config"
	"github.com/JB0t/docgen/internal/facts"
	"github.com/JB0t/docgen/internal/llm"
)

// Related picks the source files a request most likely concerns, in rank order, and
// stops adding files once the prompt would exceed maxToks. It needs no model: the
// request's words are matched against paths, symbol names and doc comments from the
// facts, and against file contents with git grep. The returned terms are the words
// that were searched for, so a caller can say why files were chosen.
func Related(root string, f *facts.Facts, cfg *config.Config, request string, maxToks int) (paths []string, terms []string, err error) {
	terms = Terms(request)
	if len(terms) == 0 {
		return nil, nil, nil
	}
	all, err := SourceFiles(root, []string{"."}, cfg)
	if err != nil {
		return nil, nil, err
	}
	// Documentation is never picked by relevance: it is attached separately where it
	// helps (ask reads draft's overviews), and a doc change is asked for explicitly.
	scores := map[string]float64{}
	for _, path := range all {
		if !cfg.IsDoc(path) {
			scores[path] = 0
		}
	}
	// Paths: every segment and every word in a segment.
	for _, path := range all {
		words := wordSet(path)
		for _, term := range terms {
			if words[term] {
				scores[path] += 3
			}
		}
	}
	// Symbols and their doc comments.
	for _, symbol := range f.Symbols {
		if _, ok := scores[symbol.File]; !ok {
			continue
		}
		names := wordSet(symbol.Name + " " + symbol.Parent)
		docs := wordSet(firstDocLine(symbol.Doc))
		for _, term := range terms {
			if names[term] {
				scores[symbol.File] += 2
			} else if docs[term] {
				scores[symbol.File] += 1
			}
		}
	}
	// Contents: one git grep per term (bounded), fixed string, case-insensitive.
	for index, term := range terms {
		if index >= maxGrepTerms {
			break
		}
		for _, path := range gitGrep(root, term) {
			if _, ok := scores[path]; ok {
				scores[path] += 2
			}
		}
	}
	type ranked struct {
		path  string
		score float64
	}
	var list []ranked
	for path, score := range scores {
		if score > 0 {
			list = append(list, ranked{path, score})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].path < list[j].path
	})
	used := llm.EstimateTokens(request) + llm.EstimateTokens(diffInstructions) + 64
	for _, item := range list {
		block, err := FileBlock(root, item.path)
		if err != nil || block == "" {
			continue
		}
		cost := llm.EstimateTokens(block)
		if used+cost > maxToks {
			continue // a smaller lower-ranked file may still fit
		}
		used += cost
		paths = append(paths, item.path)
	}
	sort.Strings(paths)
	return paths, terms, nil
}

const maxGrepTerms = 8

// Terms splits a request into search words: identifiers and words of at least three
// letters, lower-cased, with camelCase and snake_case split into their parts as well
// as kept whole, minus common English filler.
func Terms(request string) []string {
	seen := map[string]bool{}
	var terms []string
	add := func(word string) {
		word = strings.ToLower(strings.Trim(word, "._-'\"`"))
		if len(word) < 3 || stopwords[word] || seen[word] {
			return
		}
		seen[word] = true
		terms = append(terms, word)
	}
	for _, token := range tokenRE.FindAllString(request, -1) {
		add(token)
		for _, part := range splitIdentifier(token) {
			add(part)
		}
	}
	return terms
}

var tokenRE = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// splitIdentifier breaks camelCase, PascalCase and snake_case into lower-case parts.
func splitIdentifier(identifier string) []string {
	var parts []string
	var current []rune
	flush := func() {
		if len(current) > 0 {
			parts = append(parts, strings.ToLower(string(current)))
			current = current[:0]
		}
	}
	runes := []rune(identifier)
	for index, char := range runes {
		switch {
		case char == '_' || char == '-' || char == '.':
			flush()
		case unicode.IsUpper(char) && index > 0 && (unicode.IsLower(runes[index-1]) ||
			(index+1 < len(runes) && unicode.IsLower(runes[index+1]))):
			flush()
			current = append(current, char)
		default:
			current = append(current, char)
		}
	}
	flush()
	if len(parts) <= 1 {
		return nil
	}
	return parts
}

// wordSet holds every term-shaped word in text, including identifier parts.
func wordSet(text string) map[string]bool {
	words := map[string]bool{}
	for _, token := range regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`).FindAllString(text, -1) {
		words[strings.ToLower(token)] = true
		for _, part := range splitIdentifier(token) {
			words[part] = true
		}
	}
	return words
}

func firstDocLine(doc string) string {
	if index := strings.IndexByte(doc, '\n'); index >= 0 {
		return doc[:index]
	}
	return doc
}

func gitGrep(root, term string) []string {
	output, err := exec.Command("git", "-C", root, "grep", "-l", "-i", "-F", "--", term).Output()
	if err != nil {
		return nil // exit 1 means no match
	}
	return strings.Split(strings.TrimSpace(string(output)), "\n")
}

var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "that": true, "this": true, "with": true, "from": true,
	"into": true, "when": true, "then": true, "than": true, "also": true, "only": true, "not": true,
	"are": true, "was": true, "were": true, "has": true, "have": true, "had": true, "but": true,
	"all": true, "any": true, "can": true, "should": true, "would": true, "could": true, "will": true,
	"add": true, "new": true, "use": true, "make": true, "change": true, "move": true, "rename": true,
	"remove": true, "delete": true, "fix": true, "update": true, "create": true, "its": true, "it's": true,
	"what": true, "where": true, "how": true, "why": true, "which": true, "does": true, "doing": true,
	"there": true, "here": true, "about": true, "each": true, "every": true, "some": true, "more": true,
	"less": true, "one": true, "two": true, "code": true, "file": true, "files": true, "function": true,
	"instead": true, "like": true, "just": true, "same": true, "other": true, "them": true, "they": true,
	"you": true, "your": true, "please": true, "want": true, "need": true, "need_to": true, "let": true,
	"via": true, "way": true, "work": true, "works": true, "working": true, "used": true, "using": true,
}

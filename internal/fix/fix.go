// Package fix lets the model correct hand-written documentation that docgen check has
// proven wrong: a command, flag, path, identifier or env var the code does not contain.
// It is the one place docgen edits text a person wrote, so it is opt-in (docgen check
// --fix), it changes only the lines the findings name, and every edited region starts
// with a loud marker comment so the change is visible in the file and in git.
package fix

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/JB0t/docgen/internal/check"
	"github.com/JB0t/docgen/internal/code"
)

// Marker starts the comment put above every edited region.
const Marker = "docgen:edited"

var (
	beginRE  = regexp.MustCompile(`<!--\s*docgen:begin\s`)
	endRE    = regexp.MustCompile(`<!--\s*docgen:end\s`)
	markerRE = regexp.MustCompile(`^\s*<!--\s*` + Marker + `\s`)
)

// fixableKinds are the finding kinds a model can act on in prose. Markers and stale
// sections are draft's job; coverage is informational.
var fixableKinds = map[string]bool{"command": true, "flag": true, "path": true, "symbol": true, "env": true, "value": true, "argument": true}

// Fixable keeps the findings for one doc that --fix may act on: fixable kinds, outside
// generated sections (those are redrafted, not patched).
func Fixable(text string, findings []check.Finding) []check.Finding {
	generated := generatedLines(text)
	var kept []check.Finding
	for _, finding := range findings {
		if !fixableKinds[finding.Kind] || generated[finding.Line] || finding.Severity == check.Info {
			continue
		}
		kept = append(kept, finding)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Line < kept[j].Line })
	return kept
}

// generatedLines marks the line numbers inside docgen:begin/end sections.
func generatedLines(text string) map[int]bool {
	inside := map[int]bool{}
	open := false
	for index, line := range strings.Split(text, "\n") {
		if beginRE.MatchString(line) {
			open = true
		}
		if open {
			inside[index+1] = true
		}
		if endRE.MatchString(line) {
			open = false
		}
	}
	return inside
}

// Request is the change request handed to the code engine for one doc.
func Request(doc string, findings []check.Finding, date string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "The documentation file %s makes claims that do not match the code. Correct ONLY the lines listed below, keeping the author's wording, tone and layout everywhere else:\n", doc)
	for _, finding := range findings {
		fmt.Fprintf(&builder, "- line %d: %s\n", finding.Line, finding.Message)
	}
	builder.WriteString("\nRules:\n" +
		"- When the code has the right name (a renamed command, flag, path or identifier), use it. When nothing in the code corresponds, rewrite the sentence so it no longer claims it, or remove the sentence.\n" +
		"- Keep each SEARCH part to the few lines around one finding; do not rewrite paragraphs.\n" +
		fmt.Sprintf("- Start every REPLACE part with this exact line, filled in: <!-- %s %s: what changed, in plain words, no backticks -->\n", Marker, date) +
		"- Do not touch text inside <!-- docgen:begin --> ... <!-- docgen:end --> sections.\n")
	return builder.String()
}

// Mark makes sure every block's replacement starts with a marker line, so an edit the
// model forgot to label is still visible. Backticks inside marker lines are removed,
// because the checker would read them as references.
func Mark(blocks []code.Block, date string) []code.Block {
	marked := make([]code.Block, 0, len(blocks))
	for _, block := range blocks {
		lines := strings.Split(strings.TrimSuffix(block.Replace, "\n"), "\n")
		if block.Replace == "" {
			lines = nil
		}
		if len(lines) == 0 || !markerRE.MatchString(lines[0]) {
			lines = append([]string{MarkerLine(date, "hand-written text changed by docgen check --fix")}, lines...)
		} else {
			lines[0] = strings.ReplaceAll(lines[0], "`", "")
		}
		block.Replace = strings.Join(lines, "\n") + "\n"
		marked = append(marked, block)
	}
	return marked
}

// MarkerLine formats the comment put above an edited region.
func MarkerLine(date, reason string) string {
	return fmt.Sprintf("<!-- %s %s: %s -->", Marker, date, strings.ReplaceAll(strings.TrimSpace(reason), "`", ""))
}

// Count reports how many marker lines a text holds, for the run summary.
func Count(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if markerRE.MatchString(line) {
			count++
		}
	}
	return count
}

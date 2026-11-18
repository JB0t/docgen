// Package ask answers questions about a repository with the local model, from the facts,
// the overviews draft already wrote (its cache of understanding) and as much source as
// fits. Answers are checked like docs: identifiers the code does not contain are sent
// back once, and still-wrong ones are reported next to the answer rather than hidden.
package ask

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JB0t/docgen/internal/check"
	codegen "github.com/JB0t/docgen/internal/code"
	"github.com/JB0t/docgen/internal/config"
	"github.com/JB0t/docgen/internal/draft"
	"github.com/JB0t/docgen/internal/facts"
	"github.com/JB0t/docgen/internal/llm"
)

const system = `You answer questions about one code repository for a developer who has it open.
Answer from the material given: facts, documentation overviews and source. Be brief and
concrete: name files, functions, commands and flags exactly as they appear, in backticks.
When the material does not show the answer, say what is missing instead of guessing.
Do not invent identifiers, paths or commands.`

// Options bound one question.
type Options struct {
	Question string
	Scopes   []string // files or directories; empty picks related files automatically
	MaxToks  int      // source budget in tokens
	Log      func(format string, args ...any)
}

// Result is an answer and what it was built from.
type Result struct {
	Answer   string
	Files    []string        // source files shown to the model
	Terms    []string        // search words when files were picked automatically
	Findings []check.Finding // claims the checker still rejects after one retry
	Stats    llm.Stats
}

// Answer builds the prompt, asks once, checks, retries once with the problems listed.
func Answer(f *facts.Facts, cfg *config.Config, client *llm.Client, options Options) (Result, error) {
	var result Result
	var err error
	if len(options.Scopes) == 0 {
		result.Files, result.Terms, err = codegen.Related(f.Root, f, cfg, options.Question, options.MaxToks*2/3)
	} else {
		result.Files, err = codegen.SourceFiles(f.Root, options.Scopes, cfg)
	}
	if err != nil {
		return result, err
	}
	prompt, err := buildPrompt(f, options.Question, result.Files, options.MaxToks)
	if err != nil {
		return result, err
	}
	messages := []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: prompt}}
	for attempt := 1; attempt <= 2; attempt++ {
		answer, stats, err := client.Chat(messages, 1500)
		if err != nil {
			return result, err
		}
		result.Answer, result.Stats = strings.TrimSpace(answer), stats
		result.Findings = draft.Problems(check.Text(f, cfg, "answer.md", answer))
		if len(result.Findings) == 0 || attempt == 2 {
			return result, nil
		}
		var list []string
		for _, finding := range result.Findings {
			list = append(list, "- "+finding.Message)
		}
		if options.Log != nil {
			options.Log("  answer named things the code does not contain, asking again:\n    %s", strings.Join(list, "\n    "))
		}
		messages = append(messages,
			llm.Message{Role: "assistant", Content: answer},
			llm.Message{Role: "user", Content: "These do not exist in the code:\n" + strings.Join(list, "\n") +
				"\n\nAnswer again without them. Mention only identifiers, paths and commands that appear in the material."})
	}
	return result, nil
}

// buildPrompt lays out facts for the files' directories, draft's overviews for them, then
// source until the budget is spent.
func buildPrompt(f *facts.Facts, question string, files []string, maxToks int) (string, error) {
	var builder strings.Builder
	fmt.Fprintf(&builder, "QUESTION:\n%s\n", question)
	dirs := map[string]bool{}
	for _, file := range files {
		dirs[filepath.ToSlash(filepath.Dir(file))] = true
	}
	var sorted []string
	for dir := range dirs {
		sorted = append(sorted, dir)
	}
	sort.Strings(sorted)
	builder.WriteString("\nDOCUMENTATION (generated earlier from the code; may lag behind it):\n")
	documented := 0
	for _, dir := range sorted {
		if overview := draft.Section(f.Root, dir, "overview"); overview != "" {
			fmt.Fprintf(&builder, "\n--- %s/README.md\n%s\n", dir, overview)
			documented++
		}
	}
	if documented == 0 {
		builder.WriteString("(none yet: docgen draft writes overviews that make answers better)\n")
	}
	builder.WriteString("\nEXPORTED SYMBOLS:\n")
	shown := map[string]bool{}
	for _, file := range files {
		shown[file] = true
	}
	for _, symbol := range f.Symbols {
		if !symbol.Exported || !shown[symbol.File] {
			continue
		}
		name := symbol.Name
		if symbol.Parent != "" {
			name = symbol.Parent + "." + symbol.Name
		}
		fmt.Fprintf(&builder, "- %s %s (%s:%d)", symbol.Kind, name, symbol.File, symbol.Line)
		if doc := firstLine(symbol.Doc); doc != "" {
			fmt.Fprintf(&builder, " - %s", doc)
		}
		builder.WriteString("\n")
	}
	used := llm.EstimateTokens(builder.String()) + llm.EstimateTokens(system)
	builder.WriteString("\nSOURCE:\n")
	for _, file := range files {
		block, err := codegen.FileBlock(f.Root, file)
		if err != nil {
			return "", err
		}
		cost := llm.EstimateTokens(block)
		if used+cost > maxToks {
			fmt.Fprintf(&builder, "\n(%s left out: over the source budget)\n", file)
			continue
		}
		used += cost
		builder.WriteString(block)
	}
	builder.WriteString("\nAnswer the question.")
	return builder.String(), nil
}

func firstLine(text string) string {
	if index := strings.Index(text, "\n"); index >= 0 {
		text = text[:index]
	}
	return strings.TrimSpace(text)
}

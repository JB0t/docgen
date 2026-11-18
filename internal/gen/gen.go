package gen

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JB0t/docgen/internal/code"
	"github.com/JB0t/docgen/internal/llm"
	"github.com/JB0t/docgen/internal/specs"
)

// Chatter is the model (llm.Client).
type Chatter interface {
	Chat(messages []llm.Message, maxTokens int) (string, llm.Stats, error)
}

// Session is one generation.
type Session struct {
	Root        string
	Plan        *Plan
	Sets        []*specs.Set
	Extra       []string // gen.validate commands for the kind
	Retries     int
	MaxResponse int
	// Budget fails when the conversation plus an answer would not fit the model's
	// context; a fix round that would not fit is skipped and the problems reported.
	Budget func(messages []llm.Message) error
	Log    func(format string, args ...any)
	// Save keeps a model answer for inspection and returns where.
	Save func(name, text string) string
}

// Result is what a generation produced.
type Result struct {
	Contents map[string]string // root-relative path -> new content
	Order    []string
	Exists   map[string]bool
	Diff     string
	Report   Report
	Rounds   int
}

// Allowed reports whether a model-written path is under one of the targets.
func Allowed(rel string, out []string) bool {
	for _, target := range out {
		target = strings.TrimSuffix(path.Clean(target), "/")
		if target == "." || rel == target || strings.HasPrefix(rel, target+"/") {
			return true
		}
	}
	return false
}

// Generate asks the model, applies its blocks in memory, checks the result in a stage
// and sends failures back (as fix blocks against the current files) up to Retries
// times. It returns the last result that applied, with the checks' report.
func Generate(chat Chatter, s Session) (*Result, error) {
	logf := s.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	messages := []llm.Message{{Role: "system", Content: s.Plan.System}, {Role: "user", Content: s.Plan.Prompt}}
	var current map[string]string
	var result *Result
	for round := 0; round <= s.Retries; round++ {
		if round > 0 && s.Budget != nil {
			if err := s.Budget(messages); err != nil {
				logf("Not asking for another fix: %v", err)
				break
			}
		}
		answer, stats, err := chat.Chat(messages, s.MaxResponse)
		saved := ""
		if s.Save != nil {
			saved = s.Save(fmt.Sprintf("gen-answer-%d.txt", round+1), answer)
		}
		if err != nil {
			return result, fmt.Errorf("%w (answer so far: %s)", err, saved)
		}
		logf("Model (round %d): %s", round+1, stats)
		contents, order, exists, applyErr := apply(s.Root, current, answer, s.Plan.Out, s.Plan.onlyFile(current))
		if applyErr != nil {
			logf("Rejected: %v", applyErr)
			if round == s.Retries {
				if result != nil {
					return result, nil
				}
				return nil, fmt.Errorf("%v (raw answer: %s)", applyErr, saved)
			}
			messages = append(messages, llm.Message{Role: "assistant", Content: answer}, llm.Message{Role: "user", Content: applyFeedback(applyErr, current != nil)})
			continue
		}
		current = contents
		report, err := check(s, contents, order)
		if err != nil {
			return nil, err
		}
		result = &Result{Contents: contents, Order: order, Exists: exists, Report: report, Rounds: round + 1}
		if len(report.Problems) == 0 {
			break
		}
		logf("Checks found %d problem(s):", len(report.Problems))
		for _, problem := range report.Problems {
			logf("  %s", firstLine(problem.String()))
		}
		if round == s.Retries {
			break
		}
		messages = append(messages, llm.Message{Role: "assistant", Content: answer}, llm.Message{Role: "user", Content: checkFeedback(report.Problems, contents)})
	}
	if result == nil {
		return nil, errors.New("the model gave no usable answer")
	}
	diff, err := code.DiffContents(s.Root, result.Contents, result.Order, result.Exists)
	if err != nil {
		return nil, err
	}
	result.Diff = diff
	return result, nil
}

// apply turns an answer into new contents on top of the current ones.
func apply(root string, current map[string]string, answer string, out []string, onlyFile string) (map[string]string, []string, map[string]bool, error) {
	blocks, err := code.ParseBlocksFor(answer, onlyFile)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, block := range blocks {
		rel := filepath.ToSlash(filepath.Clean(filepath.FromSlash(block.Path)))
		if !Allowed(rel, out) {
			return nil, nil, nil, fmt.Errorf("%s is outside the files you may write (%s)", block.Path, strings.Join(out, ", "))
		}
	}
	return code.ApplyBlocksOver(root, current, blocks)
}

// check stages the contents and runs the kind's checks.
func check(s Session, contents map[string]string, order []string) (Report, error) {
	stage, err := NewStage(s.Root, contents)
	if err != nil {
		return Report{}, fmt.Errorf("staging the generated files: %w", err)
	}
	defer stage.Close()
	files := append([]string(nil), order...)
	sort.Strings(files)
	ctx := &CheckContext{Stage: stage, Files: files, Contents: contents, Sets: s.Sets, Inputs: s.Plan.Inputs}
	return RunChecks(s.Plan.Kind.Checks, s.Extra, ctx), nil
}

// Validate runs a kind's checks on files as they are (docgen validate): no model.
func Validate(root string, kind *Kind, files map[string]string, sets []*specs.Set, inputs []Input, extra []string) (Report, error) {
	stage, err := NewStage(root, files)
	if err != nil {
		return Report{}, err
	}
	defer stage.Close()
	ctx := &CheckContext{Stage: stage, Files: sortedFiles(files), Contents: files, Sets: sets, Inputs: inputs}
	return RunChecks(kind.Checks, extra, ctx), nil
}

func applyFeedback(err error, hadFiles bool) string {
	if hadFiles {
		return "Those blocks could not be applied: " + err.Error() + "\n\nThe files are unchanged since the last blocks that applied. Return only search/replace blocks that fix the problems, with SEARCH parts copied exactly from the current files."
	}
	return "That answer could not be used: " + err.Error() + "\n\nReturn the complete answer again as search/replace blocks, nothing else."
}

func checkFeedback(problems []Problem, contents map[string]string) string {
	var feedback strings.Builder
	feedback.WriteString("Your blocks were applied. These checks failed on the result:\n")
	mentioned := map[string]bool{}
	for _, problem := range problems {
		feedback.WriteString("- " + problem.String() + "\n")
		if problem.File != "" {
			mentioned[problem.File] = true
		}
		for rel := range contents {
			if strings.Contains(problem.Message, rel) {
				mentioned[rel] = true
			}
		}
	}
	concerned := func(rel string) bool {
		for target := range mentioned {
			if rel == target || strings.HasPrefix(rel, strings.TrimSuffix(target, "/")+"/") || target == "." {
				return true
			}
		}
		return false
	}
	var files []string
	for _, rel := range sortedFiles(contents) {
		if concerned(rel) {
			files = append(files, rel)
		}
	}
	if len(files) == 0 {
		files = sortedFiles(contents)
	}
	feedback.WriteString("\nThe files concerned, as they are now:\n")
	for _, rel := range files {
		feedback.WriteString(fileBlock(rel, contents[rel]))
	}
	feedback.WriteString("\nReturn only search/replace blocks that fix these problems, with SEARCH parts copied exactly from the files above. Keep everything else as it is.")
	return feedback.String()
}

func firstLine(text string) string {
	line, rest, _ := strings.Cut(text, "\n")
	if strings.TrimSpace(rest) != "" {
		line += " ..."
	}
	return line
}

// onlyFile is the one file an answer can be about, or "": the single --out file, or
// during fixes the single generated file. Models often leave out the path line then.
func (p *Plan) onlyFile(current map[string]string) string {
	if len(current) == 1 {
		for rel := range current {
			return rel
		}
	}
	if current == nil && len(p.Out) == 1 && len(p.Targets) <= 1 && path.Ext(p.Out[0]) != "" {
		return p.Out[0]
	}
	return ""
}

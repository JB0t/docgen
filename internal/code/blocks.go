package code

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Format is how the model is asked to express a change.
type Format string

const (
	// FormatDiff asks for a unified diff: compact, but hunk headers and context lines are
	// easy for small models to get wrong (RepairPatch fixes the common slips).
	FormatDiff Format = "diff"
	// FormatBlocks asks for search/replace blocks: the model copies the lines to replace
	// and writes the new ones, with no line counting. docgen applies them and makes the
	// diff itself, so the result goes through exactly the same checks as a model diff.
	FormatBlocks Format = "blocks"
)

const blocksInstructions = "\nReturn the change as search/replace blocks and nothing else. For each edit write the file path on its own line, then:\n" +
	"<<<<<<< SEARCH\n(the exact existing lines to replace, copied verbatim, enough to be unique)\n=======\n(the new lines)\n>>>>>>> REPLACE\n" +
	"Use several blocks for several places. An empty SEARCH part creates the file (or appends when it exists). " +
	"Do not use Markdown fences, do not add explanations, and do not alter files outside the supplied scopes."

// Block is one search/replace edit to a repo-relative file.
type Block struct {
	Path    string
	Search  string
	Replace string
}

// ParseBlocks reads search/replace blocks from a model answer. Fences and prose
// between blocks are ignored; the path is the last non-empty line before a block.
func ParseBlocks(answer string) ([]Block, error) { return ParseBlocksFor(answer, "") }

// ParseBlocksFor is ParseBlocks for an answer that can only be about one file: a block
// without a path line is taken to be for defaultPath (when it is not empty).
func ParseBlocksFor(answer, defaultPath string) ([]Block, error) {
	lines := strings.Split(strings.ReplaceAll(answer, "\r\n", "\n"), "\n")
	var blocks []Block
	lastText := ""
	for index := 0; index < len(lines); index++ {
		line := lines[index]
		if !strings.HasPrefix(line, "<<<<<<< SEARCH") {
			if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "```") {
				lastText = trimmed
			}
			continue
		}
		path := cleanBlockPath(lastText)
		if path == "" && defaultPath != "" {
			path = defaultPath
		}
		if path == "" {
			return nil, fmt.Errorf("block %d has no file path on the line before <<<<<<< SEARCH", len(blocks)+1)
		}
		var search, replace []string
		index++
		for ; index < len(lines) && !strings.HasPrefix(lines[index], "======="); index++ {
			search = append(search, lines[index])
		}
		if index >= len(lines) {
			return nil, fmt.Errorf("block %d for %s has no ======= separator", len(blocks)+1, path)
		}
		index++
		for ; index < len(lines) && !strings.HasPrefix(lines[index], ">>>>>>> REPLACE"); index++ {
			replace = append(replace, lines[index])
		}
		if index >= len(lines) {
			return nil, fmt.Errorf("block %d for %s has no >>>>>>> REPLACE end", len(blocks)+1, path)
		}
		blocks = append(blocks, Block{Path: path, Search: joinLines(search), Replace: joinLines(replace)})
		lastText = ""
	}
	if len(blocks) == 0 {
		return nil, errors.New("model did not return any search/replace block")
	}
	return blocks, nil
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// cleanBlockPath strips the decorations models put around a path: backticks, a
// "File:" label, a trailing colon, an a/ or b/ diff prefix.
func cleanBlockPath(text string) string {
	text = strings.Trim(text, "`*: ")
	for _, label := range []string{"File:", "file:", "Path:", "path:", "FILE:"} {
		text = strings.TrimSpace(strings.TrimPrefix(text, label))
	}
	text = strings.Trim(text, "`*: ")
	if strings.ContainsAny(text, " \t") {
		return ""
	}
	return strings.TrimPrefix(strings.TrimPrefix(text, "a/"), "b/")
}

// ApplyBlocks applies blocks to copies of the files and returns the result as a unified
// diff with git-style headers, without touching the repository. The diff goes through
// ValidatePaths and git apply --check like a model-written one.
func ApplyBlocks(root string, blocks []Block) (string, error) {
	contents, order, exists, err := ApplyBlocksContents(root, blocks)
	if err != nil {
		return "", err
	}
	patch, err := DiffContents(root, contents, order, exists)
	if err != nil {
		return "", err
	}
	if patch == "" {
		return "", errors.New("the blocks change nothing")
	}
	return patch, nil
}

// DiffContents returns a unified diff with git-style headers from the files on disk to
// the given contents, in order; files that did not exist diff against /dev/null.
func DiffContents(root string, contents map[string]string, order []string, exists map[string]bool) (string, error) {
	dir, err := os.MkdirTemp("", "docgen-blocks-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	var patch strings.Builder
	for index, rel := range order {
		temp := filepath.Join(dir, fmt.Sprintf("%d", index))
		if err := os.WriteFile(temp, []byte(contents[rel]), 0o600); err != nil {
			return "", err
		}
		original := filepath.FromSlash(rel)
		if !exists[rel] {
			original = os.DevNull
		}
		diff, err := gitDiffNoIndex(root, original, temp)
		if err != nil {
			return "", fmt.Errorf("diffing %s: %w", rel, err)
		}
		patch.WriteString(rewriteDiffHeaders(diff, rel))
	}
	return patch.String(), nil
}

// ApplyBlocksContents applies blocks in memory: the new content per repo-relative path,
// the paths in first-seen order, and whether each existed before.
func ApplyBlocksContents(root string, blocks []Block) (contents map[string]string, order []string, exists map[string]bool, err error) {
	return ApplyBlocksOver(root, nil, blocks)
}

// ApplyBlocksOver applies blocks on top of earlier in-memory contents: a path already in
// current is edited as it stands there, any other path as it is on disk. current is not
// modified. The result holds every path of current plus those the blocks touch, with
// current's paths first; exists says which paths are on disk.
func ApplyBlocksOver(root string, current map[string]string, blocks []Block) (contents map[string]string, order []string, exists map[string]bool, err error) {
	contents = map[string]string{}
	exists = map[string]bool{}
	for _, rel := range sortedKeys(current) {
		contents[rel] = current[rel]
		_, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		exists[rel] = statErr == nil
		order = append(order, rel)
	}
	for _, block := range blocks {
		rel := filepath.ToSlash(filepath.Clean(filepath.FromSlash(block.Path)))
		if rel == "." || !filepath.IsLocal(filepath.FromSlash(rel)) {
			return nil, nil, nil, fmt.Errorf("block path is not allowed: %q", block.Path)
		}
		if _, seen := contents[rel]; !seen {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			switch {
			case err == nil:
				contents[rel] = string(data)
				exists[rel] = true
			case os.IsNotExist(err) && strings.TrimSpace(block.Search) == "":
				contents[rel] = ""
			case os.IsNotExist(err):
				return nil, nil, nil, fmt.Errorf("%s does not exist, but the block for it has a SEARCH part", rel)
			default:
				return nil, nil, nil, err
			}
			order = append(order, rel)
		}
		next, err := applyBlock(contents[rel], block)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", rel, err)
		}
		contents[rel] = next
	}
	return contents, order, exists, nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// applyBlock replaces one occurrence of the search text: exactly, then ignoring
// trailing whitespace per line, then ignoring indentation (re-indenting the
// replacement by the same amount). An empty search appends (or creates).
func applyBlock(content string, block Block) (string, error) {
	if strings.TrimSpace(block.Search) == "" {
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		return content + block.Replace, nil
	}
	if strings.Contains(content, block.Search) {
		return strings.Replace(content, block.Search, block.Replace, 1), nil
	}
	lines := strings.Split(content, "\n")
	search := strings.Split(strings.TrimSuffix(block.Search, "\n"), "\n")
	replace := strings.Split(strings.TrimSuffix(block.Replace, "\n"), "\n")
	if block.Replace == "" {
		replace = nil
	}
	try := func(equal func(a, b string) bool, reindent bool) (string, bool) {
		for start := 0; start+len(search) <= len(lines); start++ {
			match := true
			for offset := range search {
				if !equal(lines[start+offset], search[offset]) {
					match = false
					break
				}
			}
			if !match {
				continue
			}
			out := append([]string{}, lines[:start]...)
			if reindent {
				have := indentOf(lines[start])
				want := indentOf(search[0])
				for _, line := range replace {
					if strings.HasPrefix(line, want) {
						line = have + line[len(want):]
					}
					out = append(out, line)
				}
			} else {
				out = append(out, replace...)
			}
			out = append(out, lines[start+len(search):]...)
			return strings.Join(out, "\n"), true
		}
		return "", false
	}
	if result, ok := try(func(a, b string) bool { return strings.TrimRight(a, " \t") == strings.TrimRight(b, " \t") }, false); ok {
		return result, nil
	}
	if result, ok := try(func(a, b string) bool { return strings.TrimSpace(a) == strings.TrimSpace(b) }, true); ok {
		return result, nil
	}
	return "", fmt.Errorf("SEARCH text not found (first line: %q); copy the existing lines exactly", search[0])
}

func indentOf(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// gitDiffNoIndex diffs two files outside the index; git exits 1 when they differ.
func gitDiffNoIndex(root, original, modified string) (string, error) {
	command := exec.Command("git", "-C", root, "-c", "core.quotePath=false", "diff", "--no-index", "--src-prefix=a/", "--dst-prefix=b/", "--", original, modified)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return stdout.String(), nil
}

// rewriteDiffHeaders replaces the temp-file paths git printed with the repo path, so the
// patch applies at the repository root.
func rewriteDiffHeaders(diff, rel string) string {
	lines := strings.Split(diff, "\n")
	for index, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			lines[index] = fmt.Sprintf("diff --git a/%s b/%s", rel, rel)
		case strings.HasPrefix(line, "--- ") && !strings.HasPrefix(line, "--- /dev/null"):
			lines[index] = "--- a/" + rel
		case strings.HasPrefix(line, "+++ "):
			lines[index] = "+++ b/" + rel
		}
	}
	return strings.Join(lines, "\n")
}

// BlocksInstructions is the answer-format part of a prompt that asks for search/replace
// blocks, for commands that build their own prompts (docgen gen).
func BlocksInstructions() string { return blocksInstructions }

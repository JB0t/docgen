// Package code builds bounded source prompts and validates model-generated patches.
package code

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/JB0t/docgen/internal/config"
	"github.com/JB0t/docgen/internal/llm"
)

var sourceExtensions = map[string]bool{
	".bash": true, ".cs": true, ".go": true, ".hcl": true, ".json": true,
	".md": true, ".mod": true, ".proto": true, ".py": true, ".r": true,
	".rs": true, ".sh": true, ".tf": true, ".tfvars": true, ".toml": true, ".yaml": true, ".yml": true,
}

// Prompt collects source from scopes without exceeding maxToks (estimated with
// llm.EstimateTokens). Scopes are files or directories relative to root.
func Prompt(root string, scopes []string, request string, maxToks int, cfg *config.Config, format Format) (string, error) {
	paths, err := SourceFiles(root, scopes, cfg)
	if err != nil {
		return "", err
	}
	return PromptFiles(root, paths, request, maxToks, format)
}

// PromptFiles builds the request prompt over exactly these repo-relative files.
func PromptFiles(root string, paths []string, request string, maxToks int, format Format) (string, error) {
	var builder strings.Builder
	fmt.Fprintf(&builder, "REQUEST:\n%s\n\nSOURCE FILES:\n", request)
	used := llm.EstimateTokens(builder.String())
	for _, path := range paths {
		block, err := FileBlock(root, path)
		if err != nil {
			return "", err
		}
		if block == "" {
			continue
		}
		used += llm.EstimateTokens(block)
		if used > maxToks {
			return "", fmt.Errorf("source context is about %d tokens, over llm.max_source_toks (%d); narrow the scope, leave it out to let docgen pick related files, or raise the limit in %s", used, maxToks, config.FileName)
		}
		builder.WriteString(block)
	}
	if format == FormatBlocks {
		builder.WriteString(blocksInstructions)
	} else {
		builder.WriteString(diffInstructions)
	}
	return builder.String(), nil
}

const diffInstructions = "\nReturn only a unified diff with git-style headers (diff --git a/path b/path, then --- a/path and +++ b/path). " +
	"Copy context lines exactly, keeping tabs as tabs, and start each with a single space; give every hunk at least 3 lines of context. " +
	"Do not use Markdown fences, do not add explanations, and do not alter files outside the supplied scopes."

// FileBlock returns one file's prompt block, or "" for a file the model should not see
// (binary or invalid UTF-8).
func FileBlock(root, path string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return "", fmt.Errorf("reading source %s: %w", path, err)
	}
	if !utf8.Valid(data) {
		return "", nil
	}
	// Not "--- path ---": that reads like a diff's "--- a/path" header to the model.
	return fmt.Sprintf("\n<file path=%q>\n%s</file>\n", path, ensureNewline(string(data))), nil
}

func ensureNewline(text string) string {
	if strings.HasSuffix(text, "\n") {
		return text
	}
	return text + "\n"
}

// SourceFiles lists the source files under scopes (files or directories relative to
// root), excluding configured paths, non-source extensions and secret-looking names.
func SourceFiles(root string, scopes []string, cfg *config.Config) ([]string, error) {
	if len(scopes) == 0 {
		scopes = []string{"."}
	}
	unique := map[string]bool{}
	var paths []string
	for _, scope := range scopes {
		rel := filepath.Clean(filepath.FromSlash(scope))
		if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("scope must stay inside the repository: %q", scope)
		}
		full := filepath.Join(root, rel)
		info, err := os.Lstat(full)
		if err != nil {
			return nil, fmt.Errorf("reading scope %q: %w", scope, err)
		}
		add := func(path string, entry fs.DirEntry) error {
			if entry.IsDir() || !entry.Type().IsRegular() {
				return nil
			}
			repoPath, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			repoPath = filepath.ToSlash(repoPath)
			if cfg.Excluded(repoPath) || !sourceExtensions[filepath.Ext(repoPath)] || sensitiveName(filepath.Base(repoPath)) {
				return nil
			}
			if !unique[repoPath] {
				unique[repoPath] = true
				paths = append(paths, repoPath)
			}
			return nil
		}
		if info.IsDir() {
			err = filepath.WalkDir(full, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() {
					repoPath, relErr := filepath.Rel(root, path)
					if relErr != nil {
						return relErr
					}
					if path != full && cfg.Excluded(filepath.ToSlash(repoPath)) {
						return filepath.SkipDir
					}
					return nil
				}
				return add(path, entry)
			})
		} else {
			err = add(full, fs.FileInfoToDirEntry(info))
		}
		if err != nil {
			return nil, fmt.Errorf("reading scope %q: %w", scope, err)
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("no source files found in requested scope(s)")
	}
	return paths, nil
}

func sensitiveName(name string) bool {
	lower := strings.ToLower(name)
	return lower == ".env" || strings.HasPrefix(lower, ".env.") ||
		strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".key") ||
		strings.HasSuffix(lower, ".p12") || strings.HasSuffix(lower, ".pfx")
}

// CleanPatch removes an optional Markdown diff fence and rejects non-diff output.
// Trailing spaces are kept: a hunk's last line can be a blank context line (" "), and
// trimming it leaves the hunk a line short, which git reports as a corrupt patch.
func CleanPatch(answer string) (string, error) {
	answer = strings.ReplaceAll(answer, "\r\n", "\n")
	answer = strings.TrimLeft(answer, " \t\n")
	if strings.HasPrefix(answer, "```") {
		lines := strings.Split(strings.TrimRight(answer, " \t\n"), "\n")
		if len(lines) < 3 || !strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
			return "", fmt.Errorf("model returned an unclosed code fence")
		}
		answer = strings.Join(lines[1:len(lines)-1], "\n")
	}
	answer = strings.TrimRight(answer, "\n")
	if !strings.HasPrefix(answer, "diff --git ") {
		return "", fmt.Errorf("model did not return a unified diff")
	}
	return answer + "\n", nil
}

// RepairPatch fixes the mistakes models make in otherwise good diffs, which git rejects
// as "corrupt patch": hunk headers whose line counts do not match the hunk, blank
// context lines written without their leading space, and text after the last hunk.
// It returns the patch and a description of each fix.
func RepairPatch(patch string) (string, []string) {
	lines := strings.Split(strings.TrimRight(patch, "\n"), "\n")
	var out, fixes []string
	blanks, counts, dropped := 0, 0, 0
	for index := 0; index < len(lines); index++ {
		line := lines[index]
		header := hunkHeaderRE.FindStringSubmatch(line)
		if header == nil {
			if inHeaderSection(line) {
				out = append(out, line)
			} else if strings.TrimSpace(line) != "" {
				dropped++
			}
			continue
		}
		// Collect the hunk body up to the next hunk, file or non-diff line.
		var body []string
		oldCount, newCount := 0, 0
	hunk:
		for index+1 < len(lines) {
			next := lines[index+1]
			if strings.HasPrefix(next, "@@ ") || strings.HasPrefix(next, "diff --git ") {
				break
			}
			if next == "" {
				// A blank context line that lost its space, unless it is only the gap
				// before the next file or the end of the patch.
				if index+2 >= len(lines) || strings.HasPrefix(lines[index+2], "diff --git ") {
					index++
					continue
				}
				next = " "
				blanks++
			}
			switch next[0] {
			case ' ':
				oldCount++
				newCount++
			case '-':
				oldCount++
			case '+':
				newCount++
			case '\\': // "\ No newline at end of file"
			default:
				break hunk
			}
			body = append(body, next)
			index++
		}
		oldStart, newStart := header[1], header[3]
		fixed := fmt.Sprintf("@@ -%s,%d +%s,%d @@%s", oldStart, oldCount, newStart, newCount, header[5])
		if fixed != line && (atoiOr(header[2], 1) != oldCount || atoiOr(header[4], 1) != newCount) {
			counts++
			line = fixed
		}
		out = append(out, line)
		out = append(out, body...)
	}
	if counts > 0 {
		fixes = append(fixes, fmt.Sprintf("recounted %d hunk header(s)", counts))
	}
	if blanks > 0 {
		fixes = append(fixes, fmt.Sprintf("restored the leading space on %d blank context line(s)", blanks))
	}
	if dropped > 0 {
		fixes = append(fixes, fmt.Sprintf("dropped %d line(s) of text outside the diff", dropped))
	}
	return strings.Join(out, "\n") + "\n", fixes
}

// hunkHeaderRE matches "@@ -12,7 +12,8 @@ func name" (counts are optional).
var hunkHeaderRE = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$`)

// inHeaderSection reports whether a line belongs to a file's header, before its hunks.
func inHeaderSection(line string) bool {
	for _, prefix := range []string{"diff --git ", "--- ", "+++ ", "index ", "new file mode ", "deleted file mode ",
		"old mode ", "new mode ", "similarity index ", "rename from ", "rename to ", "Binary files "} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func atoiOr(text string, fallback int) int {
	if value, err := strconv.Atoi(text); err == nil {
		return value
	}
	return fallback
}

// ValidatePaths ensures every changed file is inside one of the requested scopes.
func ValidatePaths(patch string, scopes []string, cfg *config.Config) error {
	if len(scopes) == 0 {
		scopes = []string{"."}
	}
	count := 0
	for _, line := range strings.Split(patch, "\n") {
		if !strings.HasPrefix(line, "diff --git ") {
			continue
		}
		rest := strings.TrimPrefix(line, "diff --git ")
		separator := strings.LastIndex(rest, " b/")
		if !strings.HasPrefix(rest, "a/") || separator < 0 || strings.Contains(rest, "\"") {
			return fmt.Errorf("unsupported diff path header %q", line)
		}
		before, after := rest[:separator], rest[separator+1:]
		oldPath := strings.TrimPrefix(before, "a/")
		newPath := strings.TrimPrefix(after, "b/")
		if oldPath != newPath {
			return fmt.Errorf("renames are not supported: %s -> %s", oldPath, newPath)
		}
		path := filepath.ToSlash(filepath.Clean(filepath.FromSlash(oldPath)))
		if path == "." || !filepath.IsLocal(filepath.FromSlash(path)) || cfg.Excluded(path) {
			return fmt.Errorf("patch path is not allowed: %q", oldPath)
		}
		allowed := false
		for _, scope := range scopes {
			scopePath := filepath.ToSlash(filepath.Clean(filepath.FromSlash(scope)))
			if scopePath == "." || path == scopePath || strings.HasPrefix(path, strings.TrimSuffix(scopePath, "/")+"/") {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("patch changes %s outside requested scope(s)", path)
		}
		count++
	}
	if count == 0 {
		return fmt.Errorf("patch contains no file changes")
	}
	return nil
}

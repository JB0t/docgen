package specs

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxDocChunk = 2400 // characters per doc entry

var headingRE = regexp.MustCompile(`^(#{1,3})\s+(.*)$`)

// DocEntries cuts text into entries titled "<name>: <heading path>": Markdown at
// headings (levels 1-3), anything else at blank lines, and both at maxDocChunk
// characters, keeping fenced code blocks whole where possible.
func DocEntries(name, text string) []Entry {
	var entries []Entry
	var titles [3]string
	var current []string
	size := 0
	flush := func() {
		body := strings.TrimSpace(strings.Join(current, "\n"))
		current, size = nil, 0
		if len([]rune(body)) < 40 {
			return
		}
		title := name
		var path []string
		for _, heading := range titles {
			if heading != "" {
				path = append(path, heading)
			}
		}
		if len(path) > 0 {
			title += ": " + strings.Join(path, " > ")
		}
		entries = append(entries, Entry{Name: title, Type: "doc", Text: body})
	}
	inFence := false
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
		}
		if match := headingRE.FindStringSubmatch(line); match != nil && !inFence {
			flush()
			level := len(match[1])
			titles[level-1] = strings.TrimSpace(match[2])
			for deeper := level; deeper < 3; deeper++ {
				titles[deeper] = ""
			}
			continue
		}
		line = strings.TrimRight(line, " \t")
		if size+len(line) > maxDocChunk && size > 0 && (!inFence || size > 2*maxDocChunk) {
			flush()
		} else if line == "" && size > maxDocChunk/2 && !inFence {
			flush()
			continue
		}
		current = append(current, line)
		size += len(line) + 1
	}
	flush()
	// Number repeated titles, so every entry has its own name.
	seen := map[string]int{}
	for index := range entries {
		seen[entries[index].Name]++
		if count := seen[entries[index].Name]; count > 1 {
			entries[index].Name += " (" + itoa(count) + ")"
		}
	}
	return entries
}

func itoa(value int) string { return strconv.Itoa(value) }

// DocFiles reads the text files under paths (files or directories; hidden folders and
// binary or very large files are skipped) and returns path -> content.
func DocFiles(paths []string) (map[string]string, []string, error) {
	files := map[string]string{}
	var order []string
	for _, root := range paths {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if path != root && strings.HasPrefix(entry.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			info, err := entry.Info()
			if err != nil || info.Size() > 2<<20 || !info.Mode().IsRegular() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil || bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
				return nil
			}
			files[path] = string(data)
			order = append(order, path)
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return files, order, nil
}

// Package facts builds the fact sheet of a repository: its files and languages,
// declared symbols, environment variables, documentation files and CLI commands.
// Everything is derived from the code without AI, so it can be trusted as ground truth
// for generating and checking documentation.
package facts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JB0t/docgen/internal/config"
	"github.com/JB0t/docgen/internal/extract"
)

const Version = 3

type File struct {
	Path     string `json:"path"`
	Language string `json:"language,omitempty"`
	Lines    int    `json:"lines"`
	Hash     string `json:"hash"`
}

type Doc struct {
	Path     string   `json:"path"`
	Headings []string `json:"headings"`
}

type Facts struct {
	Version     int              `json:"version"`
	Root        string           `json:"root"`
	GeneratedAt time.Time        `json:"generated_at"`
	Languages   map[string]int   `json:"languages"` // files per language
	Files       []File           `json:"files"`
	Symbols     []extract.Symbol `json:"symbols"`
	EnvVars     []extract.EnvVar `json:"env_vars"`
	Docs        []Doc            `json:"docs"`
	CLIs        []CLI            `json:"clis,omitempty"`

	// tokens holds every identifier-like word in the code, for cheap "is this
	// mentioned anywhere" checks. Not serialised.
	tokens map[string]bool
}

var (
	tokenRE   = regexp.MustCompile(`[A-Za-z_$-][A-Za-z0-9_.$:/-]*`)
	headingRE = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)
)

// listFiles returns repo-relative paths: tracked and untracked-but-not-ignored files
// when root is a git repository, otherwise every file. tracked holds the paths git has
// committed or staged (nil outside git).
func listFiles(root string) (paths []string, tracked map[string]bool, err error) {
	command := exec.Command("git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if output, err := command.Output(); err == nil {
		for _, path := range bytes.Split(output, []byte{0}) {
			if len(path) > 0 {
				paths = append(paths, string(path))
			}
		}
		tracked = map[string]bool{}
		if output, err := exec.Command("git", "-C", root, "ls-files", "--cached", "-z").Output(); err == nil {
			for _, path := range bytes.Split(output, []byte{0}) {
				if len(path) > 0 {
					tracked[string(path)] = true
				}
			}
		}
		return paths, tracked, nil
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !entry.IsDir() && entry.Type().IsRegular() {
			rel, _ := filepath.Rel(root, path)
			paths = append(paths, rel)
		}
		return nil
	})
	return paths, nil, err
}

// Build scans the repository at root.
func Build(root string, cfg *config.Config) (*Facts, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	paths, tracked, err := listFiles(root)
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	facts := &Facts{Version: Version, Root: root, GeneratedAt: time.Now().UTC(), Languages: map[string]int{}, tokens: map[string]bool{}}
	extractors := extract.All()

	var pythonFiles []string
	type fileResult struct {
		file   File
		result extract.Result
		tokens []string
		doc    *Doc
	}
	results := make([]fileResult, len(paths))
	var group sync.WaitGroup
	limiter := make(chan struct{}, 8)
	for index, path := range paths {
		// A default exclude (bin, build, dist, ...) names build output; a file git tracks
		// there is source and stays in. The repository's own excludes always apply.
		if cfg.Excluded(path) && (!tracked[path] || cfg.ExcludedByRepo(path)) {
			continue
		}
		info, err := os.Lstat(filepath.Join(root, path))
		if err != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
			continue
		}
		group.Add(1)
		go func() {
			defer group.Done()
			limiter <- struct{}{}
			defer func() { <-limiter }()
			source, err := os.ReadFile(filepath.Join(root, path))
			if err != nil || bytes.IndexByte(source[:min(len(source), 8000)], 0) >= 0 { // binary
				return
			}
			sum := sha256.Sum256(source)
			head := source[:min(len(source), 200)]
			language := extract.Language(path, head)
			out := fileResult{file: File{Path: path, Language: language, Lines: bytes.Count(source, []byte("\n")) + 1, Hash: hex.EncodeToString(sum[:8])}}
			if cfg.IsDoc(path) {
				out.doc = &Doc{Path: path, Headings: headings(source)}
			} else if language != "" {
				out.tokens = tokenRE.FindAllString(string(source), -1)
				if extractor, ok := extractors[language]; ok && language != "python" {
					out.result = extractor.Extract(path, source)
				} else if language == "python" {
					out.result.EnvVars = extract.EnvVars("python", path, source)
				}
			}
			results[index] = out
		}()
	}
	group.Wait()
	for _, out := range results {
		if out.file.Path == "" {
			continue
		}
		facts.Files = append(facts.Files, out.file)
		if out.file.Language != "" {
			facts.Languages[out.file.Language]++
		}
		if out.doc != nil {
			facts.Docs = append(facts.Docs, *out.doc)
		}
		if out.file.Language == "python" && out.doc == nil {
			pythonFiles = append(pythonFiles, out.file.Path)
		}
		facts.Symbols = append(facts.Symbols, out.result.Symbols...)
		facts.EnvVars = append(facts.EnvVars, out.result.EnvVars...)
		for _, token := range out.tokens {
			facts.tokens[token] = true
		}
	}

	// Python goes through the ast extractor in one batch, or the regex fallback.
	if symbols, ok := extract.PythonAST(root, pythonFiles); ok {
		for _, path := range pythonFiles {
			facts.Symbols = append(facts.Symbols, symbols[path]...)
		}
	} else {
		for _, path := range pythonFiles {
			source, _ := os.ReadFile(filepath.Join(root, path))
			facts.Symbols = append(facts.Symbols, extractors["python"].Extract(path, source).Symbols...)
		}
	}

	for _, cli := range cfg.CLIs {
		scraped, err := facts.cachedCLI(root, cli, cfg.Rescan)
		if err != nil {
			return nil, err
		}
		facts.CLIs = append(facts.CLIs, scraped)
	}
	return facts, nil
}

// cliCacheTTL bounds how long a scraped command tree is reused: wrapped external tools
// (kubectl, helm, ...) can change without the repository changing.
const cliCacheTTL = 24 * time.Hour

// cachedCLI reuses a scraped command tree while the CLI's source files and its
// docgen.yaml entry are unchanged and the cache is younger than cliCacheTTL.
func (f *Facts) cachedCLI(root string, cli config.CLI, rescan bool) (CLI, error) {
	dir := cli.Dir
	if filepath.IsAbs(dir) {
		if rel, err := filepath.Rel(root, dir); err == nil && !strings.HasPrefix(rel, "..") {
			dir = rel
		}
	}
	settings, _ := json.Marshal(cli)
	sum := sha256.Sum256([]byte(fmt.Sprint(Version, "\x00", root, "\x00", string(settings), "\x00", f.FileHash(dir))))
	cacheDir := os.Getenv("XDG_CACHE_HOME")
	if cacheDir == "" {
		home, _ := os.UserHomeDir()
		cacheDir = filepath.Join(home, ".cache")
	}
	path := filepath.Join(cacheDir, "docgen", "clis", hex.EncodeToString(sum[:8])+".json")
	if info, err := os.Stat(path); err == nil && !rescan && time.Since(info.ModTime()) < cliCacheTTL {
		var cached CLI
		if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &cached) == nil && len(cached.Commands) > 0 {
			cached.prober = newProber(root, cli)
			return cached, nil
		}
	}
	scraped, err := ScrapeCLI(root, cli)
	if err != nil {
		return CLI{}, err
	}
	scraped.prober = newProber(root, cli)
	if data, err := json.Marshal(scraped); err == nil {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, data, 0o644)
	}
	return scraped, nil
}

func headings(source []byte) []string {
	var found []string
	inFence := false
	for _, line := range strings.Split(string(source), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
		}
		if match := headingRE.FindStringSubmatch(line); match != nil && !inFence {
			found = append(found, match[1]+" "+match[2])
		}
	}
	return found
}

// Mentioned reports whether a word appears anywhere in the source code.
func (f *Facts) Mentioned(word string) bool { return f.tokens[word] }

// HasSymbol reports whether a symbol with this name exists (also matching Parent.Name).
func (f *Facts) HasSymbol(name string) bool {
	for _, symbol := range f.Symbols {
		if symbol.Name == name || symbol.Parent+"."+symbol.Name == name {
			return true
		}
	}
	return false
}

// HasEnvVar reports whether the code reads this environment variable.
func (f *Facts) HasEnvVar(name string) bool {
	for _, env := range f.EnvVars {
		if env.Name == name {
			return true
		}
	}
	return false
}

// FileHash returns a combined hash of every non-documentation file under scope (a file
// or directory). Docs are left out so that writing a module's README does not make the
// sections in it stale.
func (f *Facts) FileHash(scope string) string {
	docs := map[string]bool{}
	for _, doc := range f.Docs {
		docs[doc.Path] = true
	}
	hash := sha256.New()
	found := false
	for _, file := range f.Files {
		if docs[file.Path] || strings.HasSuffix(strings.ToLower(file.Path), ".md") {
			continue
		}
		if InScope(file.Path, scope) {
			hash.Write([]byte(file.Path + ":" + file.Hash + "\n"))
			found = true
		}
	}
	if !found {
		return ""
	}
	return hex.EncodeToString(hash.Sum(nil)[:6])
}

// InScope reports whether a repo-relative path is the scope or under it. The scope "."
// (or "") is the whole repository: a module or CLI at the repository root.
func InScope(path, scope string) bool {
	scope = strings.TrimSuffix(filepath.ToSlash(scope), "/")
	scope = strings.TrimPrefix(scope, "./")
	if scope == "." || scope == "" {
		return true
	}
	return path == scope || strings.HasPrefix(path, scope+"/")
}

// Write saves the facts as JSON.
func (f *Facts) Write(path string) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

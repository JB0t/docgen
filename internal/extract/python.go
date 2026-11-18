package extract

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"os/exec"
	"path/filepath"
)

//go:embed python_ast.py
var pythonScript string

// PythonAST extracts symbols from many Python files in one python3 run, using the
// standard library's ast module (exact signatures and docstrings). It returns nil and
// false when python3 is not available; callers then use the regex extractor.
func PythonAST(root string, paths []string) (map[string][]Symbol, bool) {
	python, err := exec.LookPath("python3")
	if err != nil || len(paths) == 0 {
		return nil, err == nil
	}
	absolute := make([]string, len(paths))
	byAbsolute := map[string]string{}
	for index, path := range paths {
		absolute[index] = filepath.Join(root, path)
		byAbsolute[absolute[index]] = path
	}
	request, _ := json.Marshal(map[string]any{"files": absolute})
	command := exec.Command(python, "-c", pythonScript)
	command.Stdin = bytes.NewReader(request)
	output, err := command.Output()
	if err != nil {
		return nil, false
	}
	symbols := map[string][]Symbol{}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		var line struct {
			Symbol
			Error bool `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &line) != nil || line.Error {
			continue
		}
		symbol := line.Symbol
		symbol.File = byAbsolute[symbol.File]
		symbol.Language = "python"
		symbols[symbol.File] = append(symbols[symbol.File], symbol)
	}
	return symbols, true
}

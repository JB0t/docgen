package gen

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Stage is a throwaway view of the working tree with the generated files in place:
// every directory on the way to a generated file is a real directory, everything else
// in it is a symlink to the real file or folder. Validators run there, so they see the
// whole project (roles, ansible.cfg, .terraform, charts) without anything real changing.
type Stage struct {
	Root string // the real tree
	Dir  string // the staged copy
}

// NewStage builds a stage for contents (root-relative path -> new content).
func NewStage(root string, contents map[string]string) (*Stage, error) {
	dir, err := os.MkdirTemp("", "docgen-stage-")
	if err != nil {
		return nil, err
	}
	stage := &Stage{Root: root, Dir: dir}
	realDirs := map[string]bool{".": true}
	for rel := range contents {
		for parent := filepath.Dir(filepath.FromSlash(rel)); parent != "." && parent != string(filepath.Separator); parent = filepath.Dir(parent) {
			realDirs[filepath.ToSlash(parent)] = true
		}
	}
	for rel := range realDirs {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(rel)), 0o755); err != nil {
			stage.Close()
			return nil, err
		}
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			continue // a directory the generated files create
		}
		for _, entry := range entries {
			child := filepath.ToSlash(filepath.Join(filepath.FromSlash(rel), entry.Name()))
			if realDirs[child] || entry.Name() == ".git" {
				continue
			}
			if _, generated := contents[child]; generated {
				continue
			}
			if err := os.Symlink(filepath.Join(root, filepath.FromSlash(child)), filepath.Join(dir, filepath.FromSlash(child))); err != nil {
				stage.Close()
				return nil, err
			}
		}
	}
	for rel, content := range contents {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(content), 0o644); err != nil {
			stage.Close()
			return nil, err
		}
	}
	return stage, nil
}

// Close removes the stage (only the stage: symlinks are removed, not followed).
func (s *Stage) Close() { _ = os.RemoveAll(s.Dir) }

// Path is the staged location of a root-relative path.
func (s *Stage) Path(rel string) string { return filepath.Join(s.Dir, filepath.FromSlash(rel)) }

// Read returns a staged file's content (formatters rewrite them).
func (s *Stage) Read(rel string) (string, error) {
	data, err := os.ReadFile(s.Path(rel))
	return string(data), err
}

// Clean replaces the stage's location in tool output with paths relative to the root,
// so messages name the files as the model and the user know them.
func (s *Stage) Clean(output string) string {
	dirs := []string{s.Dir}
	if resolved, err := filepath.EvalSymlinks(s.Dir); err == nil && resolved != s.Dir {
		dirs = append(dirs, resolved)
	}
	for _, dir := range dirs {
		output = strings.ReplaceAll(output, dir+string(filepath.Separator), "")
		output = strings.ReplaceAll(output, dir, ".")
	}
	return output
}

// Run runs a command in a directory of the stage (root-relative) and returns its
// cleaned, combined output and whether it succeeded. A command that runs longer than
// timeout is stopped and fails.
func (s *Stage) Run(dir string, timeout time.Duration, name string, args ...string) (string, bool, error) {
	if _, err := exec.LookPath(name); err != nil {
		return "", false, err
	}
	command := exec.Command(name, args...)
	command.Dir = s.Path(dir)
	command.Env = append(os.Environ(), "NO_COLOR=1", "ANSIBLE_NOCOLOR=1", "ANSIBLE_FORCE_COLOR=0", "TF_IN_AUTOMATION=1")
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		return "", false, err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return s.Clean(output.String()), err == nil, nil
	case <-time.After(timeout):
		_ = command.Process.Kill()
		<-done
		return s.Clean(output.String()) + fmt.Sprintf("\n(stopped after %s)", timeout), false, nil
	}
}

// commonDir is the deepest directory containing every path (root-relative).
func commonDir(paths []string) string {
	if len(paths) == 0 {
		return "."
	}
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	first := strings.Split(filepath.ToSlash(filepath.Dir(sorted[0])), "/")
	last := strings.Split(filepath.ToSlash(filepath.Dir(sorted[len(sorted)-1])), "/")
	var common []string
	for index := 0; index < len(first) && index < len(last) && first[index] == last[index]; index++ {
		common = append(common, first[index])
	}
	if len(common) == 0 {
		return "."
	}
	return strings.Join(common, "/")
}

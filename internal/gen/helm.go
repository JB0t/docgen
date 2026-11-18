package gen

import (
	"os"
	"os/exec"
	"path"
	"sort"
)

// chartDirs are the charts (directories with Chart.yaml) holding generated files.
func chartDirs(ctx *CheckContext) []string {
	seen := map[string]bool{}
	for _, file := range ctx.Files {
		for dir := path.Dir(file); ; dir = path.Dir(dir) {
			if _, err := os.Stat(ctx.Stage.Path(path.Join(dir, "Chart.yaml"))); err == nil {
				seen[dir] = true
				break
			}
			if dir == "." || dir == "/" {
				break
			}
		}
	}
	dirs := make([]string, 0, len(seen))
	for dir := range seen {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs
}

var helmLintCheck = Check{Name: "helm lint", Tool: "helm", Run: func(ctx *CheckContext) []Problem {
	var problems []Problem
	for _, dir := range chartDirs(ctx) {
		output, ok, err := ctx.Stage.Run(".", toolTimeout, "helm", "lint", dir)
		if err != nil {
			problems = append(problems, Problem{File: dir, Message: err.Error()})
		} else if !ok {
			problems = append(problems, Problem{File: dir, Message: "helm lint failed:\n" + trimOutput(output)})
		}
	}
	return problems
}}

// helmTemplateCheck renders the chart with its default values; when kubeconform is
// installed the rendered manifests are also checked against the Kubernetes schemas.
var helmTemplateCheck = Check{Name: "helm template", Tool: "helm", Run: func(ctx *CheckContext) []Problem {
	var problems []Problem
	_, kubeconformErr := exec.LookPath("kubeconform")
	for _, dir := range chartDirs(ctx) {
		command := "helm template docgen-check " + shellQuote(dir) + " >/dev/null"
		if kubeconformErr == nil {
			command = "set -o pipefail; helm template docgen-check " + shellQuote(dir) + " | kubeconform -strict -summary -ignore-missing-schemas -"
		}
		output, ok, err := ctx.Stage.Run(".", toolTimeout, "bash", "-c", command)
		if err != nil {
			problems = append(problems, Problem{File: dir, Message: err.Error()})
		} else if !ok {
			problems = append(problems, Problem{File: dir, Message: "rendering failed:\n" + trimOutput(output)})
		}
	}
	return problems
}}

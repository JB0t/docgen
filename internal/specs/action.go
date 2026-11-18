package specs

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ActionRef is a GitHub Action reference: owner/repo[/path]@ref.
var ActionRef = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)((?:/[A-Za-z0-9_.-]+)*)@([A-Za-z0-9_./-]+)$`)

// FetchAction downloads an action's action.yml (or action.yaml) from GitHub.
func FetchAction(ref string) ([]byte, error) {
	match := ActionRef.FindStringSubmatch(ref)
	if match == nil {
		return nil, fmt.Errorf("%q is not an action reference (owner/repo[/path]@ref)", ref)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	var tried []string
	for _, file := range []string{"action.yml", "action.yaml"} {
		url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s%s/%s", match[1], match[2], match[4], match[3], file)
		tried = append(tried, url)
		response, err := client.Get(url)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		if response.StatusCode == http.StatusOK {
			return data, nil
		}
	}
	return nil, fmt.Errorf("no action.yml for %s (tried %s)", ref, strings.Join(tried, ", "))
}

// ParseAction reads an action.yml into an entry named ref.
func ParseAction(ref string, data []byte) (Entry, error) {
	var action struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Inputs      map[string]struct {
			Description        string `yaml:"description"`
			Required           any    `yaml:"required"`
			Default            any    `yaml:"default"`
			DeprecationMessage string `yaml:"deprecationMessage"`
		} `yaml:"inputs"`
		Outputs map[string]struct {
			Description string `yaml:"description"`
		} `yaml:"outputs"`
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		return Entry{}, fmt.Errorf("reading action.yml of %s: %w", ref, err)
	}
	entry := Entry{Name: ref, Type: "action", Summary: strings.TrimSpace(firstSentence(action.Description))}
	if action.Name != "" {
		entry.Summary = strings.TrimSpace(action.Name + ": " + entry.Summary)
	}
	for name, input := range action.Inputs {
		if input.DeprecationMessage != "" {
			continue
		}
		required := fmt.Sprint(input.Required) == "true"
		def := valueString(input.Default)
		entry.Options = append(entry.Options, Option{
			Name: name, Desc: firstSentence(input.Description), Default: def,
			// An input with a default can be left out even when marked required.
			Required: required && def == "",
		})
	}
	for name, output := range action.Outputs {
		entry.Outputs = append(entry.Outputs, Option{Name: name, Desc: firstSentence(output.Description)})
	}
	sort.Slice(entry.Options, func(i, j int) bool { return entry.Options[i].Name < entry.Options[j].Name })
	sort.Slice(entry.Outputs, func(i, j int) bool { return entry.Outputs[i].Name < entry.Outputs[j].Name })
	return entry, nil
}

// ActionRepo is owner/repo[/path] of a reference, without the ref.
func ActionRepo(ref string) string {
	repo, _, _ := strings.Cut(ref, "@")
	return strings.ToLower(repo)
}

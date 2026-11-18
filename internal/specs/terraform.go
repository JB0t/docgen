package specs

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

type tfSchema struct {
	ProviderSchemas map[string]struct {
		Resources   map[string]tfResource `json:"resource_schemas"`
		DataSources map[string]tfResource `json:"data_source_schemas"`
	} `json:"provider_schemas"`
}

type tfResource struct {
	Block tfBlock `json:"block"`
}

type tfBlock struct {
	Attributes map[string]tfAttribute `json:"attributes"`
	BlockTypes map[string]struct {
		NestingMode string  `json:"nesting_mode"`
		MinItems    int     `json:"min_items"`
		Block       tfBlock `json:"block"`
	} `json:"block_types"`
	Description string `json:"description"`
}

type tfAttribute struct {
	Type        any    `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Optional    bool   `json:"optional"`
	Computed    bool   `json:"computed"`
	Deprecated  bool   `json:"deprecated"`
	NestedType  *struct {
		Attributes  map[string]tfAttribute `json:"attributes"`
		NestingMode string                 `json:"nesting_mode"`
	} `json:"nested_type"`
}

// ParseTerraformSchema reads `terraform providers schema -json` output: one set per
// provider (named after its source, such as hashicorp/azurerm), one entry per resource
// and data source whose type matches one of only (path.Match globs; empty = all).
func ParseTerraformSchema(data []byte, only []string) ([]*Set, error) {
	var schema tfSchema
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("reading the provider schema JSON: %w", err)
	}
	if len(schema.ProviderSchemas) == 0 {
		return nil, fmt.Errorf("the schema lists no providers; run terraform init in the directory first")
	}
	var sets []*Set
	for source, provider := range schema.ProviderSchemas {
		name := strings.TrimPrefix(source, "registry.terraform.io/")
		set := &Set{Kind: KindTerraform, Name: name}
		add := func(kind string, resources map[string]tfResource) {
			for resourceType, resource := range resources {
				if !matchesAny(resourceType, only) {
					continue
				}
				set.Entries = append(set.Entries, Entry{
					Name:    resourceType,
					Type:    kind,
					Summary: firstSentence(resource.Block.Description),
					Options: tfOptions(resource.Block),
				})
			}
		}
		add("resource", provider.Resources)
		add("data", provider.DataSources)
		if len(set.Entries) > 0 {
			sets = append(sets, set)
		}
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].Name < sets[j].Name })
	if len(sets) == 0 {
		return nil, fmt.Errorf("no resource or data source matches %s", strings.Join(only, ", "))
	}
	return sets, nil
}

func matchesAny(name string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if ok, _ := path.Match(pattern, name); ok {
			return true
		}
	}
	return false
}

// tfOptions turns a schema block into options. Attributes Terraform reserves
// (id) and computed-only attributes are kept but marked, so checks can reject writing them.
func tfOptions(block tfBlock) []Option {
	var out []Option
	for name, attribute := range block.Attributes {
		if attribute.Deprecated && !attribute.Required {
			continue
		}
		option := Option{
			Name:     name,
			Type:     tfType(attribute.Type),
			Required: attribute.Required,
			Computed: attribute.Computed && !attribute.Optional && !attribute.Required,
			Desc:     firstSentence(attribute.Description),
		}
		if attribute.NestedType != nil {
			option.Type = attribute.NestedType.NestingMode + " of objects"
			option.Sub = tfOptions(tfBlock{Attributes: attribute.NestedType.Attributes})
		}
		out = append(out, option)
	}
	for name, nested := range block.BlockTypes {
		out = append(out, Option{
			Name:     name,
			Block:    true,
			Type:     nested.NestingMode,
			MinItems: nested.MinItems,
			Required: nested.MinItems > 0,
			Desc:     firstSentence(nested.Block.Description),
			Sub:      tfOptions(nested.Block),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Block != out[j].Block {
			return !out[i].Block
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// tfType renders a cty type from the schema JSON: "string", ["list","string"],
// ["map","string"], ["object",{...}].
func tfType(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		if len(typed) == 2 {
			if kind, ok := typed[0].(string); ok {
				if kind == "object" {
					return "object"
				}
				return kind + "(" + tfType(typed[1]) + ")"
			}
		}
	}
	return ""
}

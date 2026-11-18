// Package specs keeps reference material for code generation: the exact interface of
// the modules, resources and actions a generated file may use (Ansible module options,
// Terraform provider schemas, GitHub Action inputs) and free-form docs. Specs are
// stored per user, not per repository, because they describe tools, not code:
//
//	$DOCGEN_SPECS or $XDG_DATA_HOME/docgen/specs/<kind>/<set>.json
//
// A set is what one 'docgen spec add' made (a collection, a provider, an action, a
// folder of docs); each entry in it is one module, resource, action or doc section.
// Entries are retrieved for a request by keywords and, when an embedding server is
// available, by meaning; the same entries drive the mechanical checks of generated files.
package specs

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Spec kinds.
const (
	KindAnsible   = "ansible"   // Ansible modules (ansible-doc -j)
	KindTerraform = "terraform" // Terraform resources and data sources (providers schema -json)
	KindAction    = "action"    // GitHub Actions (action.yml)
	KindDocs      = "docs"      // free-form text: Markdown, command output
)

// Kinds lists the spec kinds in display order.
var Kinds = []string{KindAnsible, KindTerraform, KindAction, KindDocs}

// Option is one setting of an entry: an Ansible module option, a Terraform attribute or
// nested block, or an action input.
type Option struct {
	Name     string   `json:"name"`
	Aliases  []string `json:"aliases,omitempty"`
	Type     string   `json:"type,omitempty"`
	Elements string   `json:"elements,omitempty"` // list element type
	Required bool     `json:"required,omitempty"`
	Choices  []string `json:"choices,omitempty"`
	Default  string   `json:"default,omitempty"`
	Desc     string   `json:"desc,omitempty"`
	// Computed marks a Terraform attribute the provider sets: it cannot be written.
	Computed bool `json:"computed,omitempty"`
	// Block marks a Terraform nested block (written as name { ... }, not name = ...);
	// MinItems is how many the schema requires.
	Block    bool     `json:"block,omitempty"`
	MinItems int      `json:"min_items,omitempty"`
	Sub      []Option `json:"sub,omitempty"`
}

// Entry is one retrievable spec.
type Entry struct {
	Name    string   `json:"name"`              // FQCN, resource type, owner/repo@ref, or doc title
	Type    string   `json:"type"`              // module, resource, data, action, doc
	Summary string   `json:"summary,omitempty"` // one line
	Options []Option `json:"options,omitempty"`
	Outputs []Option `json:"outputs,omitempty"` // action outputs
	Example string   `json:"example,omitempty"` // a usage example from the source docs
	Text    string   `json:"text,omitempty"`    // doc text (docs kind)
	Hash    string   `json:"hash"`
	Vector  vector   `json:"vector,omitempty"`
}

// Set is one source of specs.
type Set struct {
	Kind    string   `json:"kind"`
	Name    string   `json:"name"`
	For     string   `json:"for,omitempty"`    // docs: the gen kind they are for ("" = any)
	Source  string   `json:"source"`           // how the set was made, to refresh it
	Added   string   `json:"added"`            // when (RFC 3339)
	Model   string   `json:"model,omitempty"`  // embedding model of the entries' vectors
	Common  []Option `json:"common,omitempty"` // options (nearly) every module accepts, shown once
	Entries []Entry  `json:"entries"`
	path    string
}

// Dir is the spec store: $DOCGEN_SPECS, else $XDG_DATA_HOME/docgen/specs, else
// ~/.local/share/docgen/specs.
func Dir() string {
	if dir := os.Getenv("DOCGEN_SPECS"); dir != "" {
		return dir
	}
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "docgen", "specs")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "docgen", "specs")
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_.@-]+`)

// FileName is the file a set is stored in, under its kind's folder.
func FileName(name string) string {
	return strings.Trim(unsafeName.ReplaceAllString(name, "_"), "_") + ".json"
}

// Save writes the set to the store (atomically), filling in entry hashes.
func (s *Set) Save(dir string) error {
	if s.Kind == "" || s.Name == "" {
		return errors.New("a spec set needs a kind and a name")
	}
	for index := range s.Entries {
		s.Entries[index].Hash = s.Entries[index].contentHash()
	}
	sort.SliceStable(s.Entries, func(i, j int) bool { return s.Entries[i].Name < s.Entries[j].Name })
	if s.Added == "" {
		s.Added = time.Now().Format(time.RFC3339)
	}
	if s.path == "" {
		s.path = filepath.Join(dir, s.Kind, FileName(s.Name))
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	temp := s.path + ".tmp"
	if err := os.WriteFile(temp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(temp, s.path)
}

// Path is where the set is stored.
func (s *Set) Path() string { return s.path }

// KeepVectors copies vectors from an older version of the set for entries whose content
// did not change, so refreshing a set only embeds what is new.
func (s *Set) KeepVectors(old *Set) {
	if old == nil {
		return
	}
	known := map[string]vector{}
	for _, entry := range old.Entries {
		if len(entry.Vector) > 0 {
			known[entry.Hash] = entry.Vector
		}
	}
	for index := range s.Entries {
		entry := &s.Entries[index]
		if vector, ok := known[entry.contentHash()]; ok {
			entry.Vector = vector
		}
	}
	s.Model = old.Model
}

func (e Entry) contentHash() string {
	data, _ := json.Marshal([]any{e.Name, e.Type, e.Summary, e.Options, e.Outputs, e.Example, e.Text})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// Load reads every set in the store, sorted by kind then name.
func Load(dir string) ([]*Set, error) {
	var sets []*Set
	for _, kind := range Kinds {
		paths, _ := filepath.Glob(filepath.Join(dir, kind, "*.json"))
		for _, path := range paths {
			set, err := loadSet(path)
			if err != nil {
				return nil, err
			}
			sets = append(sets, set)
		}
	}
	return sets, nil
}

func loadSet(path string) (*Set, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	set := &Set{}
	if err := json.Unmarshal(data, set); err != nil {
		return nil, fmt.Errorf("reading spec set %s: %w", path, err)
	}
	set.path = path
	return set, nil
}

// Find returns the stored set of a kind and name, or nil.
func Find(sets []*Set, kind, name string) *Set {
	for _, set := range sets {
		if set.Kind == kind && set.Name == name {
			return set
		}
	}
	return nil
}

// Remove deletes a set from the store.
func Remove(set *Set) error { return os.Remove(set.path) }

// vector stores float32s as base64 to keep the store small.
type vector []float32

func (v vector) MarshalJSON() ([]byte, error) {
	buffer := make([]byte, 4*len(v))
	for index, value := range v {
		binary.LittleEndian.PutUint32(buffer[4*index:], math.Float32bits(value))
	}
	return json.Marshal(base64.StdEncoding.EncodeToString(buffer))
}

func (v *vector) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	buffer, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return err
	}
	*v = make(vector, len(buffer)/4)
	for index := range *v {
		(*v)[index] = math.Float32frombits(binary.LittleEndian.Uint32(buffer[4*index:]))
	}
	return nil
}

func normalise(values []float32) vector {
	var sum float64
	for _, value := range values {
		sum += float64(value) * float64(value)
	}
	norm := float32(math.Sqrt(sum))
	out := make(vector, len(values))
	for index, value := range values {
		if norm > 0 {
			out[index] = value / norm
		}
	}
	return out
}

// Embedder turns texts into vectors.
type Embedder func(texts []string) ([][]float32, error)

// embedText is what an entry is embedded from: its name, summary and option names,
// short enough for one batch of the embedding model (nomic-embed-text expects the
// search_document prefix).
func (e Entry) embedText() string {
	var text strings.Builder
	text.WriteString("search_document: " + e.Name + "\n" + e.Summary + "\n")
	if e.Text != "" {
		text.WriteString(e.Text)
	}
	for _, option := range e.Options {
		text.WriteString(option.Name + " ")
	}
	runes := []rune(text.String())
	if len(runes) > 1400 {
		runes = runes[:1400]
	}
	return string(runes)
}

// Embed gives every entry without a vector for model one, in batches; vectors from
// another model are dropped first. It returns how many entries it embedded.
func (s *Set) Embed(model string, embed Embedder) (int, error) {
	if s.Model != model {
		for index := range s.Entries {
			s.Entries[index].Vector = nil
		}
		s.Model = model
	}
	var pending []int
	for index, entry := range s.Entries {
		if len(entry.Vector) == 0 {
			pending = append(pending, index)
		}
	}
	const batch = 16
	for start := 0; start < len(pending); start += batch {
		end := min(start+batch, len(pending))
		var texts []string
		for _, index := range pending[start:end] {
			texts = append(texts, s.Entries[index].embedText())
		}
		vectors, err := embed(texts)
		if err != nil {
			return start, err
		}
		for offset, index := range pending[start:end] {
			s.Entries[index].Vector = normalise(vectors[offset])
		}
	}
	return len(pending), nil
}

// Unembedded counts entries without a vector for model.
func (s *Set) Unembedded(model string) int {
	if s.Model != model {
		return len(s.Entries)
	}
	count := 0
	for _, entry := range s.Entries {
		if len(entry.Vector) == 0 {
			count++
		}
	}
	return count
}

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/JB0t/docgen/internal/code"
	"github.com/JB0t/docgen/internal/config"
	"github.com/JB0t/docgen/internal/gen"
	"github.com/JB0t/docgen/internal/specs"
)

// genConfig loads docgen.yaml from the repository (or the current directory outside
// one): spec and gen commands work in any directory.
func genConfig() (root string, cfg *config.Config, err error) {
	root, err = workRoot()
	if err != nil {
		return "", nil, err
	}
	cfg, err = config.Load(root)
	if err != nil {
		return "", nil, fmt.Errorf("reading %s: %w", config.FileName, err)
	}
	return root, cfg, nil
}

// workRoot is the repository root, or the current directory outside a repository.
func workRoot() (string, error) {
	if output, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		return strings.TrimSpace(string(output)), nil
	}
	return os.Getwd()
}

// openEmbedder starts (if configured) the embedding server and returns a function for
// it, or nil and a note when specs can only be searched by keywords.
func openEmbedder(cfg *config.Config) (*modelSession, specs.Embedder, string) {
	if cfg.LLM.EmbedURL == "none" {
		return nil, nil, "embeddings are off (llm.embed_url: none)"
	}
	embedCfg := *cfg
	embedCfg.LLM.URL, embedCfg.LLM.Model = cfg.LLM.EmbedURL, cfg.LLM.EmbedModel
	embedCfg.LLM.Start, embedCfg.LLM.Stop = cfg.LLM.EmbedStart, cfg.LLM.EmbedStop
	session := newModelSession(&embedCfg)
	session.Client.Progress = nil
	if err := session.Ensure(); err != nil {
		session.Stop()
		return nil, nil, fmt.Sprintf("no embedding server (%v)", err)
	}
	return session, session.Client.Embed, ""
}

// embedSets gives vectors to entries that lack them and saves the sets it changed.
func embedSets(sets []*specs.Set, model string, embed specs.Embedder) error {
	for _, set := range sets {
		pending := set.Unembedded(model)
		if pending == 0 {
			continue
		}
		fmt.Fprintf(os.Stderr, "Embedding %d entries of %s...\n", pending, set.Name)
		if _, err := set.Embed(model, embed); err != nil {
			return fmt.Errorf("embedding %s: %w", set.Name, err)
		}
		if err := set.Save(specs.Dir()); err != nil {
			return err
		}
	}
	return nil
}

// searchSpecs ranks the store's entries for a request, by meaning when the embedding
// server is available.
func searchSpecs(cfg *config.Config, sets []*specs.Set, filter specs.Filter, request string, limit int) ([]specs.Hit, string, error) {
	terms := code.Terms(request)
	var query []float32
	note := ""
	session, embed, why := openEmbedder(cfg)
	if embed != nil {
		defer session.Stop()
		var searched []*specs.Set
		for _, set := range sets {
			if filter.Allows(set) {
				searched = append(searched, set)
			}
		}
		if err := embedSets(searched, cfg.LLM.EmbedModel, embed); err != nil {
			return nil, "", err
		}
		vectors, err := embed([]string{"search_query: " + request})
		if err != nil {
			note = "searching by keywords only: " + err.Error()
		} else {
			query = vectors[0]
		}
	} else {
		note = "searching by keywords only: " + why
	}
	return specs.Search(sets, filter, terms, query, limit), note, nil
}

func newSpecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spec",
		Short: "Manage the reference specs docgen gen retrieves (Ansible modules, Terraform schemas, Actions, docs)",
		Long: `Specs are the exact interface of what generated files may use: Ansible module options
(from ansible-doc), Terraform resource schemas (from terraform providers schema), GitHub
Action inputs (from action.yml), and free-form docs or command output. docgen gen
retrieves the ones a request needs and checks generated files against them.

They are stored per user in ` + "`$DOCGEN_SPECS`" + ` (default ~/.local/share/docgen/specs), one
file per set. Adding a set again refreshes it. When the embedding server is available
(llm-server's embed preset), entries are also searched by meaning; without it, by keywords.`,
	}
	cmd.AddCommand(newSpecAddCmd(), newSpecListCmd(), newSpecShowCmd(), newSpecSearchCmd(), newSpecRemoveCmd(), newSpecEmbedCmd())
	return cmd
}

func newSpecAddCmd() *cobra.Command {
	var only []string
	var command, forKind string
	cmd := &cobra.Command{
		Use:   "add ansible|terraform|action|docs SOURCE...",
		Short: "Add (or refresh) a set of specs",
		Long: `ansible COLLECTION|MODULE...  modules from ansible-doc (the collection must be installed:
                              ansible-galaxy collection install COLLECTION); --only filters
                              module names (globs on the short name)
terraform DIR|SCHEMA.json     resources and data sources of every provider in an initialised
                              directory (terraform init), or a saved providers schema -json;
                              --only filters types (azurerm_kubernetes_*)
action OWNER/REPO@REF...      a GitHub Action's inputs and outputs (action.yml, from GitHub)
docs NAME [PATH...]           Markdown or text files and folders, and with --cmd a command's
                              output (helm show values, kubectl explain); --for limits them
                              to one gen kind`,
		Example: `  docgen spec add ansible azure.azcollection
  docgen spec add ansible azure.azcollection --only 'azure_rm_aks*'
  docgen spec add ansible ansible.builtin
  docgen spec add terraform ./infra --only 'azurerm_kubernetes_*'
  docgen spec add action actions/checkout@v4 azure/login@v2
  docgen spec add docs ingress-nginx-values --cmd 'helm show values ingress-nginx/ingress-nginx' --for helm
  docgen spec add docs team-ansible-guide ~/notes/ansible-style.md --for ansible`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, sources := args[0], args[1:]
			_, cfg, err := genConfig()
			if err != nil {
				return err
			}
			var sets []*specs.Set
			switch kind {
			case specs.KindAnsible:
				if len(sources) == 0 {
					return fmt.Errorf("name a collection (azure.azcollection) or modules")
				}
				sets, err = ansibleSets(sources, only)
			case specs.KindTerraform:
				if len(sources) != 1 {
					return fmt.Errorf("give one initialised directory or schema JSON file")
				}
				sets, err = terraformSets(sources[0], only)
			case specs.KindAction:
				if len(sources) == 0 {
					return fmt.Errorf("name actions as owner/repo@ref")
				}
				for _, ref := range sources {
					data, fetchErr := specs.FetchAction(ref)
					if fetchErr != nil {
						return fetchErr
					}
					entry, parseErr := specs.ParseAction(ref, data)
					if parseErr != nil {
						return parseErr
					}
					sets = append(sets, &specs.Set{Kind: specs.KindAction, Name: ref, Source: "action " + ref, Entries: []specs.Entry{entry}})
				}
			case specs.KindDocs:
				if len(sources) == 0 {
					return fmt.Errorf("give the set a name, then files, folders or --cmd")
				}
				if forKind != "" {
					found, findErr := gen.FindKind(forKind)
					if findErr != nil {
						return findErr
					}
					forKind = found.Name
				}
				var set *specs.Set
				set, err = docsSet(sources[0], sources[1:], command, forKind)
				sets = []*specs.Set{set}
			default:
				return fmt.Errorf("unknown spec kind %q (use: %s)", kind, strings.Join(specs.Kinds, ", "))
			}
			if err != nil {
				return err
			}
			store := specs.Dir()
			existing, err := specs.Load(store)
			if err != nil {
				return err
			}
			for _, set := range sets {
				set.KeepVectors(specs.Find(existing, set.Kind, set.Name))
				set.Added = time.Now().Format(time.RFC3339)
				if err := set.Save(store); err != nil {
					return err
				}
				fmt.Printf("Saved %s %s: %d entries (%s)\n", set.Kind, set.Name, len(set.Entries), set.Path())
			}
			session, embed, why := openEmbedder(cfg)
			if embed == nil {
				fmt.Fprintf(os.Stderr, "Not embedded yet (%s): search uses keywords until 'docgen spec embed' runs with the embedding server.\n", why)
				return nil
			}
			defer session.Stop()
			return embedSets(sets, cfg.LLM.EmbedModel, embed)
		},
	}
	cmd.Flags().StringSliceVar(&only, "only", nil, "ansible/terraform: only entries whose (short) name matches these globs")
	cmd.Flags().StringVar(&command, "cmd", "", "docs: also store this shell command's output")
	cmd.Flags().StringVar(&forKind, "for", "", "docs: retrieve them only for this gen kind (default: every kind)")
	return cmd
}

// ansibleSets makes one set per collection (all its modules, or those matching only)
// and one per named module.
func ansibleSets(sources, only []string) ([]*specs.Set, error) {
	var sets []*specs.Set
	for _, source := range sources {
		parts := strings.Split(source, ".")
		var modules []string
		if len(parts) == 2 {
			listed, err := specs.AnsibleModules(source)
			if err != nil {
				return nil, err
			}
			for name := range listed {
				short := name[strings.LastIndex(name, ".")+1:]
				if len(only) == 0 || matchAny(short, only) || matchAny(name, only) {
					modules = append(modules, name)
				}
			}
			sort.Strings(modules)
			if len(modules) == 0 {
				return nil, fmt.Errorf("no module of %s matches %s", source, strings.Join(only, ", "))
			}
		} else {
			modules = []string{source}
		}
		fmt.Fprintf(os.Stderr, "Reading %d module docs with ansible-doc...\n", len(modules))
		entries, err := specs.AnsibleDocs(modules, func(done int) {
			if len(modules) > 40 {
				fmt.Fprintf(os.Stderr, "\r  %d/%d", done, len(modules))
			}
		})
		if len(modules) > 40 {
			fmt.Fprintln(os.Stderr)
		}
		if err != nil {
			return nil, err
		}
		set := &specs.Set{Kind: specs.KindAnsible, Name: source, Source: "ansible " + source, Entries: entries}
		if len(only) > 0 {
			set.Source += " --only " + strings.Join(only, ",")
		}
		set.Common = specs.CommonOptions(entries)
		sets = append(sets, set)
	}
	return sets, nil
}

func matchAny(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if ok, _ := filepath.Match(pattern, name); ok {
			return true
		}
	}
	return false
}

func terraformSets(source string, only []string) ([]*specs.Set, error) {
	info, err := os.Stat(source)
	if err != nil {
		return nil, err
	}
	var data []byte
	if info.IsDir() {
		fmt.Fprintf(os.Stderr, "Reading provider schemas with terraform providers schema -json in %s...\n", source)
		command := exec.Command("terraform", "-chdir="+source, "providers", "schema", "-json")
		command.Stderr = os.Stderr
		data, err = command.Output()
		if err != nil {
			return nil, fmt.Errorf("terraform providers schema -json in %s (run terraform init there first): %w", source, err)
		}
	} else if data, err = os.ReadFile(source); err != nil {
		return nil, err
	}
	sets, err := specs.ParseTerraformSchema(data, only)
	for _, set := range sets {
		set.Source = "terraform " + source
		if len(only) > 0 {
			set.Source += " --only " + strings.Join(only, ",")
		}
	}
	return sets, err
}

func docsSet(name string, paths []string, command, forKind string) (*specs.Set, error) {
	set := &specs.Set{Kind: specs.KindDocs, Name: name, For: forKind, Source: "docs " + strings.Join(paths, " ")}
	files, order, err := specs.DocFiles(paths)
	if err != nil {
		return nil, err
	}
	for _, path := range order {
		set.Entries = append(set.Entries, specs.DocEntries(name+": "+filepath.Base(path), files[path])...)
	}
	if command != "" {
		output, err := exec.Command("sh", "-c", command).Output()
		if err != nil {
			return nil, fmt.Errorf("running %q: %w", command, err)
		}
		set.Entries = append(set.Entries, specs.DocEntries(name, string(output))...)
		set.Source += " --cmd " + command
	}
	if len(set.Entries) == 0 {
		return nil, fmt.Errorf("no text found for %s", name)
	}
	return set, nil
}

func newSpecListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [SET]",
		Short: "List stored spec sets, or the entries of one",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sets, err := specs.Load(specs.Dir())
			if err != nil {
				return err
			}
			writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			defer writer.Flush()
			if len(args) == 1 {
				found := false
				for _, set := range sets {
					if set.Name != args[0] {
						continue
					}
					found = true
					for _, entry := range set.Entries {
						fmt.Fprintf(writer, "%s\t%s\t%s\n", entry.Name, entry.Type, entry.Summary)
					}
				}
				if !found {
					return fmt.Errorf("no spec set %s", args[0])
				}
				return nil
			}
			if len(sets) == 0 {
				fmt.Printf("No specs in %s yet; add some with docgen spec add.\n", specs.Dir())
				return nil
			}
			fmt.Fprintln(writer, "KIND\tSET\tENTRIES\tEMBEDDED\tFOR\tADDED")
			for _, set := range sets {
				embedded := "no"
				if set.Model != "" {
					embedded = fmt.Sprintf("%d/%d %s", len(set.Entries)-set.Unembedded(set.Model), len(set.Entries), set.Model)
				}
				forKind := set.For
				if forKind == "" {
					forKind = "-"
				}
				fmt.Fprintf(writer, "%s\t%s\t%d\t%s\t%s\t%s\n", set.Kind, set.Name, len(set.Entries), embedded, forKind, strings.SplitN(set.Added, "T", 2)[0])
			}
			return nil
		},
	}
}

func newSpecShowCmd() *cobra.Command {
	var compact bool
	cmd := &cobra.Command{
		Use:   "show NAME...",
		Short: "Print entries as a prompt shows them",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sets, err := specs.Load(specs.Dir())
			if err != nil {
				return err
			}
			for _, name := range args {
				hits, err := specs.Lookup(sets, name)
				if err != nil {
					return err
				}
				for _, hit := range hits {
					fmt.Println(specs.Render(hit.Set, hit.Entry, compact))
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&compact, "compact", false, "without descriptions and examples")
	return cmd
}

func newSpecSearchCmd() *cobra.Command {
	var kindName string
	var limit int
	cmd := &cobra.Command{
		Use:     "search QUERY",
		Short:   "Show which specs a request would retrieve, and their scores",
		Example: `  docgen spec search "provision aks node pools in a resource group" --kind ansible`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, cfg, err := genConfig()
			if err != nil {
				return err
			}
			sets, err := specs.Load(specs.Dir())
			if err != nil {
				return err
			}
			filter := specs.Filter{}
			if kindName != "" {
				kind, err := gen.FindKind(kindName)
				if err != nil {
					return err
				}
				filter = specs.Filter{Kinds: kind.SpecKinds, For: kind.Name}
			}
			hits, note, err := searchSpecs(cfg, sets, filter, args[0], limit)
			if err != nil {
				return err
			}
			if note != "" {
				fmt.Fprintln(os.Stderr, note)
			}
			writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			defer writer.Flush()
			fmt.Fprintln(writer, "SCORE\tMEANING\tKEYWORDS\tNAME\tSUMMARY")
			for _, hit := range hits {
				fmt.Fprintf(writer, "%.2f\t%.2f\t%.2f\t%s\t%s\n", hit.Score, hit.Meaning, hit.Keyword, hit.Entry.Name, hit.Entry.Summary)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kindName, "kind", "", "search the specs this gen kind uses (default: all)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 10, "how many results")
	return cmd
}

func newSpecRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm SET...",
		Short: "Remove spec sets",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sets, err := specs.Load(specs.Dir())
			if err != nil {
				return err
			}
			for _, name := range args {
				removed := false
				for _, set := range sets {
					if set.Name == name {
						if err := specs.Remove(set); err != nil {
							return err
						}
						fmt.Printf("Removed %s %s\n", set.Kind, set.Name)
						removed = true
					}
				}
				if !removed {
					return fmt.Errorf("no spec set %s", name)
				}
			}
			return nil
		},
	}
}

func newSpecEmbedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "embed",
		Short: "Embed every stored entry that has no vector yet (search and gen also do this as needed)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, cfg, err := genConfig()
			if err != nil {
				return err
			}
			sets, err := specs.Load(specs.Dir())
			if err != nil {
				return err
			}
			session, embed, why := openEmbedder(cfg)
			if embed == nil {
				return fmt.Errorf("cannot embed: %s", why)
			}
			defer session.Stop()
			return embedSets(sets, cfg.LLM.EmbedModel, embed)
		},
	}
}

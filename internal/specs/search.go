package specs

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Hit is a retrieved entry.
type Hit struct {
	Set     *Set
	Entry   *Entry
	Score   float64
	Keyword float64 // keyword part of the score, 0..1
	Meaning float64 // cosine similarity, when vectors were compared
}

// Filter picks the sets a search looks at: spec kinds, and the gen kind docs are for.
type Filter struct {
	Kinds []string // spec kinds to search; empty = all
	For   string   // docs sets tagged for another gen kind are skipped
	Sets  []string // only these sets (by name); empty = all
}

// Allows reports whether a search with this filter looks at set.
func (f Filter) Allows(set *Set) bool {
	if len(f.Kinds) > 0 && !contains(f.Kinds, set.Kind) {
		return false
	}
	if len(f.Sets) > 0 && !contains(f.Sets, set.Name) {
		return false
	}
	return set.Kind != KindDocs || set.For == "" || f.For == "" || set.For == f.For
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

var wordRE = regexp.MustCompile(`[a-z0-9]+`)

// Search ranks entries for a request: terms are its search words (see code.Terms), and
// query, when not nil, its embedding (compared with the entries' vectors). Without
// vectors the ranking is by keywords alone.
func Search(sets []*Set, filter Filter, terms []string, query []float32, limit int) []Hit {
	var normalised vector
	if len(query) > 0 {
		normalised = normalise(query)
	}
	wantsInfo := false
	for _, term := range terms {
		switch term {
		case "info", "facts", "gather", "query", "lookup", "inspect", "fetch":
			wantsInfo = true
		}
	}
	var hits []Hit
	maxKeyword := 0.0
	for _, set := range sets {
		if !filter.Allows(set) {
			continue
		}
		for index := range set.Entries {
			entry := &set.Entries[index]
			hit := Hit{Set: set, Entry: entry, Keyword: keywordScore(entry, terms)}
			if len(normalised) > 0 && len(entry.Vector) == len(normalised) && set.Model != "" {
				var dot float32
				for position, value := range entry.Vector {
					dot += value * normalised[position]
				}
				hit.Meaning = float64(dot)
			}
			// Read-only modules (_info, _facts) and data sources rank lower unless asked for.
			if !wantsInfo && (strings.HasSuffix(entry.Name, "_info") || strings.HasSuffix(entry.Name, "_facts") || entry.Type == "data") {
				hit.Keyword *= 0.5
				hit.Meaning *= 0.9
			}
			maxKeyword = max(maxKeyword, hit.Keyword)
			hits = append(hits, hit)
		}
	}
	for index := range hits {
		if maxKeyword > 0 {
			hits[index].Keyword /= maxKeyword
		}
		if hits[index].Meaning != 0 {
			hits[index].Score = hits[index].Meaning + 0.25*hits[index].Keyword
		} else {
			hits[index].Score = hits[index].Keyword
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Entry.Name < hits[j].Entry.Name
	})
	var kept []Hit
	for _, hit := range hits {
		if hit.Score > 0 {
			kept = append(kept, hit)
		}
	}
	if len(kept) > limit {
		kept = kept[:limit]
	}
	return kept
}

// keywordScore matches search terms (and their singular forms) against an entry's name
// (whole words and parts of run-together words like aksagentpool), summary, option
// names and text.
func keywordScore(entry *Entry, terms []string) float64 {
	name := strings.ToLower(entry.Name)
	if entry.Type == "module" {
		// The collection prefix (azure.azcollection.) matches every module; score the short name.
		name = name[strings.LastIndex(name, ".")+1:]
	}
	nameWords := wordSet(name)
	summaryWords := wordSet(strings.ToLower(entry.Summary))
	optionWords := map[string]bool{}
	for _, option := range entry.Options {
		for word := range wordSet(strings.ToLower(option.Name)) {
			optionWords[word] = true
		}
	}
	text := strings.ToLower(entry.Text)
	score := 0.0
	for _, term := range terms {
		forms := []string{term}
		if strings.HasSuffix(term, "s") && len(term) > 4 {
			forms = append(forms, strings.TrimSuffix(term, "s"))
		}
		best := 0.0
		for _, form := range forms {
			switch {
			case nameWords[form]:
				best = max(best, 3)
			case len(form) >= 3 && strings.Contains(name, form):
				best = max(best, 2)
			}
			if summaryWords[form] {
				best = max(best, best+1)
			}
			if optionWords[form] {
				best = max(best, best+0.3)
			}
			if text != "" && strings.Contains(text, form) {
				best = max(best, best+0.5)
			}
		}
		score += best
	}
	return score
}

func wordSet(text string) map[string]bool {
	words := map[string]bool{}
	for _, word := range wordRE.FindAllString(text, -1) {
		words[word] = true
	}
	return words
}

// Lookup finds entries by name for an explicit --spec: an exact entry name, the last
// part of a module name (azure_rm_aks), or a set name (all its entries, for small sets).
func Lookup(sets []*Set, name string) ([]Hit, error) {
	var hits []Hit
	for _, set := range sets {
		for index := range set.Entries {
			entry := &set.Entries[index]
			if entry.Name == name {
				return []Hit{{Set: set, Entry: entry, Score: 1}}, nil
			}
			if entry.Type == "module" && strings.HasSuffix(entry.Name, "."+name) {
				hits = append(hits, Hit{Set: set, Entry: entry, Score: 1})
			}
		}
	}
	if len(hits) > 0 {
		return hits[:1], nil
	}
	for _, set := range sets {
		if set.Name == name {
			if len(set.Entries) > 8 {
				return nil, fmt.Errorf("spec set %s has %d entries; name the ones you want (docgen spec list %s)", name, len(set.Entries), name)
			}
			for index := range set.Entries {
				hits = append(hits, Hit{Set: set, Entry: &set.Entries[index], Score: 1})
			}
			return hits, nil
		}
	}
	var close []string
	for _, set := range sets {
		for _, entry := range set.Entries {
			if strings.Contains(entry.Name, name) || Distance(shortName(entry.Name), name) <= 3 {
				close = append(close, entry.Name)
			}
		}
	}
	if len(close) > 5 {
		close = close[:5]
	}
	if len(close) > 0 {
		return nil, fmt.Errorf("no spec named %s; close: %s", name, strings.Join(close, ", "))
	}
	return nil, fmt.Errorf("no spec named %s (docgen spec list shows what is stored)", name)
}

func shortName(name string) string { return name[strings.LastIndex(name, ".")+1:] }

// Distance is the Levenshtein distance between two strings.
func Distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	previous := make([]int, len(rb)+1)
	current := make([]int, len(rb)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		current[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(rb)]
}

// Closest returns the candidate nearest to name, if it is near enough to be a likely
// typo or a renamed option. Edit distance decides, with a shared prefix and suffix
// counting in a candidate's favour (node_count is nearer count than max_count; aksnodepool
// is nearer aksagentpool than aks), and containment counting as close.
func Closest(name string, candidates []string) string {
	best, bestScore := "", 1e9
	limit := max(2, len([]rune(name))/2)
	for _, candidate := range candidates {
		distance := Distance(name, candidate)
		if strings.Contains(candidate, name) || strings.Contains(name, candidate) {
			gap := len([]rune(candidate)) - len([]rune(name))
			distance = min(distance, max(2, abs(gap)/2))
		}
		if distance > limit {
			continue
		}
		score := float64(distance) - 0.5*float64(sharedPrefix(name, candidate)+sharedSuffix(name, candidate))
		if score < bestScore {
			best, bestScore = candidate, score
		}
	}
	return best
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func sharedPrefix(a, b string) int {
	count := 0
	for count < len(a) && count < len(b) && a[count] == b[count] {
		count++
	}
	return count
}

func sharedSuffix(a, b string) int {
	count := 0
	for count < len(a) && count < len(b) && a[len(a)-1-count] == b[len(b)-1-count] {
		count++
	}
	return count
}

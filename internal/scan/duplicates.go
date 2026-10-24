package scan

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// duplicates finds cheap, deterministic hints that projects overlap. The
// agent reading the report decides whether they really should be merged.
func duplicates(projects []*Project) []Duplicate {
	var out []Duplicate
	byRemote := map[string][]string{}
	byName := map[string][]string{}
	for _, p := range projects {
		// Only origin counts: forks share their upstream remote by design.
		if p.Git != nil && p.Git.Origin != "" {
			n := normalizeRemote(p.Git.Origin)
			byRemote[n] = append(byRemote[n], p.Rel)
		}
		byName[normalizeName(filepath.Base(p.Path))] = append(byName[normalizeName(filepath.Base(p.Path))], p.Rel)
	}
	paired := map[string]bool{}
	add := func(reason string, rels []string) {
		sort.Strings(rels)
		key := strings.Join(rels, "\x00")
		if paired[key] {
			return
		}
		paired[key] = true
		out = append(out, Duplicate{Reason: reason, Projects: rels})
	}
	for _, k := range sortedKeys(byRemote) {
		if len(byRemote[k]) > 1 {
			add("same git remote "+k, byRemote[k])
		}
	}
	for _, k := range sortedKeys(byName) {
		if len(byName[k]) > 1 && k != "" && !genericNames[k] {
			add("same name", byName[k])
		}
	}
	// Pairwise name-token overlap, e.g. "paper-bot" and "paper-bot-v2".
	for i := 0; i < len(projects); i++ {
		ti := tokens(filepath.Base(projects[i].Path))
		if len(ti) == 0 {
			continue
		}
		for j := i + 1; j < len(projects); j++ {
			tj := tokens(filepath.Base(projects[j].Path))
			if s := jaccard(ti, tj); s >= 0.6 && normalizeName(filepath.Base(projects[i].Path)) != normalizeName(filepath.Base(projects[j].Path)) {
				add(fmt.Sprintf("similar names (%.0f%% token overlap)", s*100), []string{projects[i].Rel, projects[j].Rel})
			}
		}
	}
	return out
}

// genericNames are too common to suggest overlap on their own.
var genericNames = map[string]bool{
	"docs": true, "doc": true, "papers": true, "paper": true, "book": true, "web": true,
	"src": true, "test": true, "tests": true, "data": true, "code": true, "archive": true,
	"versioned": true, "notes": true, "slides": true, "examples": true, "demo": true,
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var remoteRe = regexp.MustCompile(`^(?:[a-z+]+://)?(?:[^@/]+@)?([^:/]+)[:/](.+?)(?:\.git)?/?$`)

func normalizeRemote(r string) string {
	r = strings.ToLower(strings.TrimSpace(r))
	if m := remoteRe.FindStringSubmatch(r); m != nil {
		return m[1] + "/" + m[2]
	}
	return r
}

var (
	splitRe   = regexp.MustCompile(`[^a-z0-9]+`)
	versionRe = regexp.MustCompile(`^v?\d+$`)
)

func normalizeName(s string) string {
	return strings.Join(tokens(s), "")
}

// tokens splits a directory name into lower-case words, dropping pure
// version numbers ("v2", "2024") and very short tokens.
func tokens(s string) []string {
	s = camelSplit(s)
	var out []string
	for _, t := range splitRe.Split(strings.ToLower(s), -1) {
		if len(t) >= 2 && !versionRe.MatchString(t) {
			out = append(out, t)
		}
	}
	return out
}

func camelSplit(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' && s[i-1] >= 'a' && s[i-1] <= 'z' {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	sa := map[string]bool{}
	for _, t := range a {
		sa[t] = true
	}
	inter := 0
	union := len(sa)
	seen := map[string]bool{}
	for _, t := range b {
		if seen[t] {
			continue
		}
		seen[t] = true
		if sa[t] {
			inter++
		} else {
			union++
		}
	}
	return float64(inter) / float64(union)
}

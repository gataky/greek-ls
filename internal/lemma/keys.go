// Package lemma reads the lemma Greek-English SQLite dictionary (schema
// version 2). It is a port of the Python reference in sqlite-database.md,
// itself a port of lemma's src/greek_keys.rs, src/english_index.rs and
// src/lookup.rs.
package lemma

import (
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// ---------------------------------------------------------------- Greek keys

const (
	accentGrave       = '̀'
	accentAcute       = '́'
	accentPerispomeni = '͂'
	diaeresis         = '̈'
)

var voiceless = map[rune]bool{'θ': true, 'κ': true, 'ξ': true, 'π': true, 'σ': true, 'τ': true, 'φ': true, 'χ': true, 'ψ': true}

var single = map[rune]string{
	'α': "a", 'β': "v", 'γ': "g", 'δ': "d", 'ε': "e", 'ζ': "z", 'η': "i",
	'θ': "8", 'ι': "i", 'κ': "k", 'λ': "l", 'μ': "m", 'ν': "n", 'ξ': "ks",
	'ο': "o", 'π': "p", 'ρ': "r", 'σ': "s", 'τ': "t", 'υ': "i", 'φ': "f",
	'χ': "h", 'ψ': "ps", 'ω': "o",
}

type pair struct{ a, b rune }

var vowelPairs = map[pair]string{{'ο', 'υ'}: "u", {'ε', 'ι'}: "i", {'ο', 'ι'}: "i", {'υ', 'ι'}: "i", {'α', 'ι'}: "e"}

var consonantPairs = map[pair]string{{'μ', 'π'}: "b", {'ν', 'τ'}: "d", {'γ', 'κ'}: "g", {'γ', 'γ'}: "g"}

// Greeklish spelling -> readings, most common first (index = penalty).
// Order matters: the first entry that matches at a position wins.
var greeklish = []struct {
	spelling string
	readings []string
}{
	{"nth", []string{"n8"}}, {"mph", []string{"mf"}},
	{"th", []string{"8"}}, {"ch", []string{"h"}}, {"ph", []string{"f"}},
	{"ou", []string{"u"}}, {"ei", []string{"i"}}, {"oi", []string{"i", "oi"}}, {"ui", []string{"i"}},
	{"ai", []string{"e", "ai"}},
	{"au", []string{"af", "av"}}, {"ay", []string{"af", "av"}},
	{"eu", []string{"ef", "ev"}}, {"ey", []string{"ef", "ev"}},
	{"mp", []string{"b"}}, {"nt", []string{"d"}}, {"gk", []string{"g"}}, {"gg", []string{"g"}},
	{"a", []string{"a"}}, {"b", []string{"b", "v"}}, {"c", []string{"k"}}, {"d", []string{"d"}},
	{"e", []string{"e"}}, {"f", []string{"f"}}, {"g", []string{"g"}}, {"h", []string{"i", "h"}},
	{"i", []string{"i"}}, {"j", []string{"g"}}, {"k", []string{"k"}}, {"l", []string{"l"}}, {"m", []string{"m"}},
	{"n", []string{"n"}}, {"o", []string{"o"}}, {"p", []string{"p"}}, {"q", []string{"k"}}, {"r", []string{"r"}},
	{"s", []string{"s"}}, {"t", []string{"t"}}, {"u", []string{"u", "i"}}, {"v", []string{"v"}},
	{"w", []string{"o"}}, {"x", []string{"h", "ks"}}, {"y", []string{"i", "g"}}, {"z", []string{"z"}},
	{"8", []string{"8"}}, {"3", []string{"ks"}},
}

// MaxQueryKeys caps the Greeklish candidates kept per query.
const MaxQueryKeys = 64

func isMark(r rune) bool { return unicode.Is(unicode.M, r) }

// IsGreek reports whether s contains a Greek or Greek Extended letter.
func IsGreek(s string) bool {
	for _, r := range s {
		if (r >= 0x0370 && r <= 0x03FF) || (r >= 0x1F00 && r <= 0x1FFF) {
			return true
		}
	}
	return false
}

// Norm lowercases, strips all diacritics and folds final sigma: Σπίτι -> σπιτι.
func Norm(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if isMark(r) {
			continue
		}
		r = unicode.ToLower(r)
		if r == 'ς' {
			r = 'σ'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func collapse(s string) string {
	var b strings.Builder
	last := rune(-1)
	for _, r := range s {
		if r != last {
			b.WriteRune(r)
			last = r
		}
	}
	return b.String()
}

type letter struct {
	base                 rune
	accent, hasDiaeresis bool
}

func letters(s string) []letter {
	var out []letter
	for _, r := range norm.NFD.String(s) {
		switch {
		case r == accentGrave || r == accentAcute || r == accentPerispomeni:
			if len(out) > 0 {
				out[len(out)-1].accent = true
			}
		case r == diaeresis:
			if len(out) > 0 {
				out[len(out)-1].hasDiaeresis = true
			}
		case isMark(r):
		default:
			r = unicode.ToLower(r)
			if r == 'ς' {
				r = 'σ'
			}
			out = append(out, letter{base: r})
		}
	}
	return out
}

// Phonetic is the sound-based key of a Greek word: σπίτι -> spiti, θάλασσα -> 8alasa.
func Phonetic(s string) string {
	ls := letters(s)
	var out strings.Builder
	for i := 0; i < len(ls); {
		a := ls[i]
		if i+1 < len(ls) {
			b := ls[i+1]
			// An accent on the first vowel (τσάι) or a diaeresis on the second
			// (κοροϊδεύω) means two separate vowels, not a digraph.
			if !a.accent && !b.hasDiaeresis {
				if p, ok := vowelPairs[pair{a.base, b.base}]; ok {
					out.WriteString(p)
					i += 2
					continue
				}
				if (a.base == 'α' || a.base == 'ε') && b.base == 'υ' {
					if a.base == 'α' {
						out.WriteByte('a')
					} else {
						out.WriteByte('e')
					}
					if i+2 >= len(ls) || voiceless[ls[i+2].base] {
						out.WriteByte('f')
					} else {
						out.WriteByte('v')
					}
					i += 2
					continue
				}
			}
			if p, ok := consonantPairs[pair{a.base, b.base}]; ok {
				out.WriteString(p)
				i += 2
				continue
			}
		}
		out.WriteString(single[a.base])
		i++
	}
	return collapse(out.String())
}

// QueryKey is a candidate phonetic key and its penalty (0 = most common reading).
type QueryKey struct {
	Key     string
	Penalty int
}

func greeklishKeys(s string) []QueryKey {
	var lb strings.Builder
	for _, r := range norm.NFD.String(s) {
		if !isMark(r) {
			lb.WriteRune(unicode.ToLower(r))
		}
	}
	latin := lb.String()

	candidates := []QueryKey{{"", 0}}
	for i := 0; i < len(latin); {
		matched := -1
		for g := range greeklish {
			if strings.HasPrefix(latin[i:], greeklish[g].spelling) {
				matched = g
				break
			}
		}
		if matched < 0 {
			// Not Greeklish (spaces, punctuation): skip one character.
			_, size := firstRune(latin[i:])
			i += size
			continue
		}
		i += len(greeklish[matched].spelling)
		expanded := make([]QueryKey, 0, len(candidates)*len(greeklish[matched].readings))
		for _, c := range candidates {
			for rank, reading := range greeklish[matched].readings {
				expanded = append(expanded, QueryKey{c.Key + reading, c.Penalty + rank})
			}
		}
		sort.SliceStable(expanded, func(a, b int) bool { return expanded[a].Penalty < expanded[b].Penalty })
		if len(expanded) > MaxQueryKeys {
			expanded = expanded[:MaxQueryKeys]
		}
		candidates = expanded
	}

	var keys []QueryKey
	seen := map[string]bool{}
	for _, c := range candidates {
		k := collapse(c.Key)
		if k != "" && !seen[k] {
			seen[k] = true
			keys = append(keys, QueryKey{k, c.Penalty})
		}
	}
	return keys
}

func firstRune(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 0
}

// QueryKeys returns the phonetic keys for a Greek or Greeklish query, lowest
// penalty first.
func QueryKeys(s string) []QueryKey {
	if IsGreek(s) {
		if k := Phonetic(s); k != "" {
			return []QueryKey{{k, 0}}
		}
		return nil
	}
	return greeklishKeys(s)
}

// -------------------------------------------------------------- English keys

var irregularPlurals = map[string]string{
	"children": "child", "people": "person", "men": "man", "women": "woman",
	"feet": "foot", "teeth": "tooth", "mice": "mouse", "geese": "goose",
	"knives": "knife", "wives": "wife", "lives": "life", "leaves": "leaf",
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// Stem singularizes an English word: houses -> house, cities -> city.
func Stem(w string) string {
	if s, ok := irregularPlurals[w]; ok {
		return s
	}
	n := len([]rune(w))
	if n <= 3 || !isASCII(w) {
		return w
	}
	switch {
	case strings.HasSuffix(w, "ies") && n > 4:
		return w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "sses"):
		return w[:len(w)-2]
	case strings.HasSuffix(w, "ss"), strings.HasSuffix(w, "us"), strings.HasSuffix(w, "is"):
		return w
	case strings.HasSuffix(w, "shes"), strings.HasSuffix(w, "ches"), strings.HasSuffix(w, "xes"), strings.HasSuffix(w, "zes"):
		return w[:len(w)-2]
	case strings.HasSuffix(w, "s"):
		return w[:len(w)-1]
	}
	return w
}

// IsAlnum matches Python's str.isalnum for one character.
func IsAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }

func trim(w string) string {
	return strings.TrimFunc(w, func(r rune) bool { return !IsAlnum(r) && r != '-' && r != '\'' })
}

// NormalizeTerm makes the English lookup term: "To Say" -> "say", "Houses" -> "house".
func NormalizeTerm(s string) string {
	var words []string
	for w := range strings.FieldsSeq(strings.ToLower(s)) {
		if t := trim(w); t != "" {
			words = append(words, t)
		}
	}
	if len(words) > 1 {
		switch words[0] {
		case "to", "a", "an", "the":
			words = words[1:]
		}
	}
	if !strings.ContainsFunc(strings.Join(words, ""), unicode.IsLetter) {
		return ""
	}
	for i, w := range words {
		words[i] = Stem(w)
	}
	return strings.Join(words, " ")
}

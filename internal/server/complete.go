package server

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gataky/greek-ls/internal/lemma"
)

// Rows fetched per key before grouping by headword. Inflected forms share their
// headword's frequency, so one common word can fill a small window by itself.
const fetchPerKey = 300

const formsByPrefix = `
    SELECT f.form, h.word, h.frequency, h.id, f.%[1]s
    FROM form f JOIN headword h ON h.id = f.headword_id
    WHERE f.%[1]s >= ? AND f.%[1]s < ? || char(1114111)
    ORDER BY h.frequency DESC, f.form = h.word DESC
    LIMIT ?`

// Suggestion is one completion candidate.
type Suggestion struct {
	Form       string // to insert
	Word       string // its headword
	HeadwordID int64
	Frequency  int64
}

type lookup struct {
	col, key      string
	kind, penalty int
}

type rank struct{ kind, penalty int }

func (a rank) less(b rank) bool {
	return a.kind < b.kind || (a.kind == b.kind && a.penalty < b.penalty)
}

type ranked struct {
	rank rank
	s    Suggestion
}

// Complete suggests words for a partly typed word, best first.
//
// Greek input matches by norm prefix, then by sound as spelling help.
// Latin input matches by the sound of every Greeklish reading.
func Complete(d *lemma.Dict, typed string, greeklish bool, limit int) ([]Suggestion, error) {
	// kind 0 = the key matches a whole form, 1 = prefix of a form,
	// +2 for sound-alike matches of Greek input.
	var lookups []lookup
	if lemma.IsGreek(typed) {
		lookups = append(lookups, lookup{"norm", lemma.Norm(typed), 0, 0})
		if sound := lemma.Phonetic(typed); sound != "" && utf8.RuneCountInString(typed) >= 3 {
			lookups = append(lookups, lookup{"phonetic", sound, 2, 0})
		}
	} else if greeklish && strings.ContainsFunc(typed, unicode.IsLetter) {
		for _, k := range lemma.QueryKeys(typed) {
			lookups = append(lookups, lookup{"phonetic", k.Key, 0, k.Penalty})
		}
	}

	best := map[string]ranked{}
	for _, l := range lookups {
		rows, err := d.Query(fmt.Sprintf(formsByPrefix, l.col), l.key, l.key, fetchPerKey)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var s Suggestion
			var storedKey string
			if err := rows.Scan(&s.Form, &s.Word, &s.Frequency, &s.HeadwordID, &storedKey); err != nil {
				rows.Close()
				return nil, err
			}
			// Skip phrases and proverbs, and capitalized forms of lowercase
			// words (sentence-initial copies; matchCase recreates those).
			if strings.Contains(s.Form, " ") || (startsUpper(s.Form) && !startsUpper(s.Word)) {
				continue
			}
			r := rank{l.kind, l.penalty}
			if storedKey != l.key {
				r.kind++
			}
			if cur, ok := best[s.Form]; !ok || r.less(cur.rank) {
				best[s.Form] = ranked{r, s}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	// Within a headword, prefer the headword itself, then shorter forms. The
	// first form of every headword ranks ahead of any headword's second form,
	// so one word's inflections do not crowd out other words.
	byHeadword := map[int64][]ranked{}
	for _, r := range best {
		byHeadword[r.s.HeadwordID] = append(byHeadword[r.s.HeadwordID], r)
	}
	type entry struct {
		ranked
		nth int
	}
	var all []entry
	for _, forms := range byHeadword {
		sort.Slice(forms, func(i, j int) bool {
			a, b := forms[i], forms[j]
			if a.rank != b.rank {
				return a.rank.less(b.rank)
			}
			if ah, bh := a.s.Form == a.s.Word, b.s.Form == b.s.Word; ah != bh {
				return ah
			}
			if la, lb := runeLen(a.s.Form), runeLen(b.s.Form); la != lb {
				return la < lb
			}
			return a.s.Form < b.s.Form
		})
		for nth, r := range forms {
			all = append(all, entry{r, nth})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		switch {
		case a.rank.kind != b.rank.kind:
			return a.rank.kind < b.rank.kind
		case a.nth != b.nth:
			return a.nth < b.nth
		case a.rank.penalty != b.rank.penalty:
			return a.rank.penalty < b.rank.penalty
		case a.s.Frequency != b.s.Frequency:
			return a.s.Frequency > b.s.Frequency
		case runeLen(a.s.Form) != runeLen(b.s.Form):
			return runeLen(a.s.Form) < runeLen(b.s.Form)
		}
		return a.s.Form < b.s.Form
	})

	out := make([]Suggestion, 0, min(limit, len(all)))
	for _, e := range all[:min(limit, len(all))] {
		out = append(out, e.s)
	}
	return out, nil
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

func startsUpper(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsUpper(r)
}

// matchCase capitalizes a suggestion when the typed word is capitalized.
func matchCase(word, typed string) string {
	r, size := utf8.DecodeRuneInString(word)
	if startsUpper(typed) && unicode.IsLower(r) {
		return string(unicode.ToUpper(r)) + word[size:]
	}
	return word
}

// ------------------------------------------------------------------ markdown

func shortGloss(d *lemma.Dict, headwordID int64, width int) (string, error) {
	gloss, err := d.Gloss(headwordID)
	if r := []rune(gloss); len(r) > width {
		gloss = string(r[:width-1]) + "…"
	}
	return gloss, err
}

func entryMarkdown(d *lemma.Dict, headwordID int64, word, matchedForm string) (string, error) {
	entries, err := d.Entries(headwordID)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if matchedForm != "" && matchedForm != word {
		fmt.Fprintf(&b, "%s → ", matchedForm)
	}
	fmt.Fprintf(&b, "**%s**\n", word)
	for _, e := range entries {
		fmt.Fprintf(&b, "\n_%s_", e.PosDisplay)
		if e.Head != "" {
			fmt.Fprintf(&b, " · %s", e.Head)
		}
		b.WriteString("\n\n")
		for n, s := range e.Senses {
			fmt.Fprintf(&b, "%d. %s\n", n+1, s.Gloss)
			if s.Example != "" {
				example := s.Example
				if s.ExampleTranslation != "" {
					example += " — " + s.ExampleTranslation
				}
				fmt.Fprintf(&b, "   > %s\n", example)
			}
		}
		if e.Etymology != "" {
			fmt.Fprintf(&b, "\nEtymology: %s\n", e.Etymology)
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func englishMarkdown(hits []lemma.EnglishHit, term string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** →\n", term)
	for _, h := range hits {
		fmt.Fprintf(&b, "\n- **%s**", h.Greek)
		if h.Gloss != "" {
			fmt.Fprintf(&b, " %s", h.Gloss)
		}
		if h.Note != "" {
			fmt.Fprintf(&b, " [%s]", h.Note)
		}
	}
	return b.String()
}

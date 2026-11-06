package lemma

import (
	"os"
	"path/filepath"
	"testing"
)

// dbPath is the repo's dictionary; GREEK_LS_DB (set by .envrc) overrides it.
func dbPath(t testing.TB) string {
	if p := os.Getenv("GREEK_LS_DB"); p != "" {
		return p
	}
	p, err := filepath.Abs("../../lemma_greek_en.db")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func openTest(t testing.TB) *Dict {
	t.Helper()
	d, err := Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// Test vectors from sqlite-database.md, "Verifying a port".

func TestNorm(t *testing.T) {
	for in, want := range map[string]string{"Σπίτι": "σπιτι", "λόγος": "λογοσ", "ἄνθρωπος": "ανθρωποσ", "Ϊ": "ι"} {
		if got := Norm(in); got != want {
			t.Errorf("Norm(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPhonetic(t *testing.T) {
	for in, want := range map[string]string{
		"σπίτι": "spiti", "σπήτι": "spiti", "καλημέρα": "kalimera", "θάλασσα": "8alasa",
		"παιδί": "pedi", "ούτε": "ute", "γεια": "gia", "μπαμπάς": "babas", "πόλη": "poli",
		"πολύ": "poli", "τσάι": "tsai", "κοροϊδεύω": "koroidevo", "αϋπνία": "aipnia",
		"αυτός": "aftos", "αύριο": "avrio", "ευχαριστώ": "efharisto", "Ευρώπη": "evropi", "πάντα": "pada",
	} {
		if got := Phonetic(in); got != want {
			t.Errorf("Phonetic(%q) = %q, want %q", in, got, want)
		}
	}
}

func penaltyFor(keys []QueryKey, key string) (int, bool) {
	for _, k := range keys {
		if k.Key == key {
			return k.Penalty, true
		}
	}
	return 0, false
}

func TestQueryKeys(t *testing.T) {
	for in, word := range map[string]string{
		"spiti": "σπίτι", "kalhmera": "καλημέρα", "thalassa": "θάλασσα", "8alasa": "θάλασσα",
		"efharisto": "ευχαριστώ", "eyxaristw": "ευχαριστώ", "xeri": "χέρι", "xerw": "ξέρω",
		"geia": "γεια", "yia": "γεια", "mpampas": "μπαμπάς", "tsai": "τσάι", "ellhnika": "ελληνικά",
		"Spíti": "σπίτι", "anthropos": "άνθρωπος", "nymphi": "νύμφη",
	} {
		if _, ok := penaltyFor(QueryKeys(in), Phonetic(word)); !ok {
			t.Errorf("QueryKeys(%q) lacks %q (%s)", in, Phonetic(word), word)
		}
	}
	for _, c := range []struct {
		in, word string
		penalty  int
	}{{"xeri", "χέρι", 0}, {"xeri", "ξέρει", 1}, {"kalhmera", "καλημέρα", 0}} {
		if p, _ := penaltyFor(QueryKeys(c.in), Phonetic(c.word)); p != c.penalty {
			t.Errorf("QueryKeys(%q) penalty for %s = %d, want %d", c.in, c.word, p, c.penalty)
		}
	}
	if got := QueryKeys("σπητι"); len(got) != 1 || got[0] != (QueryKey{"spiti", 0}) {
		t.Errorf(`QueryKeys("σπητι") = %v`, got)
	}
}

func TestNormalizeTerm(t *testing.T) {
	for in, want := range map[string]string{
		"To Say": "say", "a House": "house", "living rooms.": "living room", "to": "to", "...": "",
	} {
		if got := NormalizeTerm(in); got != want {
			t.Errorf("NormalizeTerm(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"houses": "house", "cities": "city", "boxes": "box", "glasses": "glass", "bus": "bus",
		"thing": "thing", "bed": "bed", "children": "child",
	} {
		if got := Stem(in); got != want {
			t.Errorf("Stem(%q) = %q, want %q", in, got, want)
		}
	}
}

// The strongest check: the stored keys of every form match ours.
func TestStoredKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("scans every form")
	}
	d := openTest(t)
	rows, err := d.db.Query("SELECT form, norm, phonetic FROM form")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n, bad := 0, 0
	for rows.Next() {
		var form, nm, ph string
		if err := rows.Scan(&form, &nm, &ph); err != nil {
			t.Fatal(err)
		}
		n++
		if Norm(form) != nm || Phonetic(form) != ph {
			if bad++; bad <= 10 {
				t.Errorf("%q: norm %q/%q phonetic %q/%q", form, Norm(form), nm, Phonetic(form), ph)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if bad > 0 {
		t.Errorf("%d of %d forms differ", bad, n)
	}
}

func TestSearchGreek(t *testing.T) {
	d := openTest(t)
	for _, c := range []struct {
		in, word, form string
		kind           int
	}{
		{"σπίτι", "σπίτι", "σπίτι", Exact},
		{"σπιτι", "σπίτι", "σπίτι", NormMatch},
		{"σπητι", "σπίτι", "σπίτι", PhoneticMatch},
		{"spiti", "σπίτι", "σπίτι", PhoneticMatch},
		{"είπα", "λέω", "είπα", Exact},
		{"ἄνθρωπος", "άνθρωπος", "άνθρωπος", NormMatch},
		{"xeri", "χέρι", "χέρι", PhoneticMatch},
	} {
		hits, err := d.SearchGreek(c.in, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) == 0 || hits[0].Word != c.word || hits[0].MatchedForm != c.form || hits[0].Kind != c.kind {
			t.Errorf("SearchGreek(%q)[0] = %+v, want %s ← %s kind %d", c.in, hits, c.word, c.form, c.kind)
		}
	}
}

func TestSearchEnglish(t *testing.T) {
	d := openTest(t)
	hits, err := d.SearchEnglish("bank", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 2 || hits[0].Greek != "τράπεζα" || hits[0].Note != "institution" || hits[1].Greek != "όχθη" {
		t.Errorf("SearchEnglish(bank) = %+v", hits)
	}
	// No en_index row, so this comes from the full-text fallback
	hits, err = d.SearchEnglish("olive oil press", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || !hits[0].FullText || hits[0].Greek != "ελαιόλαδο" {
		t.Errorf("SearchEnglish(olive oil press) = %+v", hits)
	}
}

func TestEntries(t *testing.T) {
	d := openTest(t)
	hits, _ := d.SearchGreek("σπίτι", 1)
	entries, err := d.Entries(hits[0].HeadwordID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 || entries[0].PosDisplay != "noun" || len(entries[0].Senses) == 0 {
		t.Errorf("Entries(σπίτι) = %+v", entries)
	}
}

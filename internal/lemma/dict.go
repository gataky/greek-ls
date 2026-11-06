package lemma

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// SchemaVersion is the dictionary schema this package reads.
const SchemaVersion = "2"

// Dict is an open, read-only dictionary.
type Dict struct {
	db *sql.DB
}

// Open opens the dictionary read-only and checks its schema version.
func Open(path string) (*Dict, error) {
	// SQLite only says "unable to open database file"; name the path instead.
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("dictionary not found: %w", err)
	}
	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	var version string
	if err := db.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(&version); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if version != SchemaVersion {
		db.Close()
		return nil, fmt.Errorf("open %s: expected schema_version %s, got %s", path, SchemaVersion, version)
	}
	return &Dict{db: db}, nil
}

func (d *Dict) Close() error { return d.db.Close() }

// Query runs raw SQL against the dictionary, for lookups this package does
// not wrap.
func (d *Dict) Query(query string, args ...any) (*sql.Rows, error) { return d.db.Query(query, args...) }

// -------------------------------------------------------------------- search

// Match kinds of a Greek search hit, best first.
const (
	Exact = iota
	NormMatch
	PhoneticMatch
)

// GreekHit is one headword found by SearchGreek.
type GreekHit struct {
	HeadwordID  int64
	Word        string
	MatchedForm string
	Kind        int
	Penalty     int
	Frequency   int64
}

const selectForms = `
    SELECT f.headword_id, f.form, h.word, h.frequency, f.phonetic
    FROM form f JOIN headword h ON h.id = f.headword_id`

const preferHeadword = " ORDER BY f.form = h.word DESC"

// SearchGreek looks up a Greek or Greeklish query, best first.
func (d *Dict) SearchGreek(query string, limit int) ([]GreekHit, error) {
	query = strings.TrimSpace(query)
	best := map[int64]GreekHit{}
	var order []int64 // first-seen order, for stable ties
	keep := func(h GreekHit) {
		cur, ok := best[h.HeadwordID]
		if !ok {
			order = append(order, h.HeadwordID)
		}
		if !ok || h.Kind < cur.Kind || (h.Kind == cur.Kind && h.Penalty < cur.Penalty) {
			best[h.HeadwordID] = h
		}
	}
	scan := func(rows *sql.Rows, hit func(GreekHit, string)) error {
		defer rows.Close()
		for rows.Next() {
			var h GreekHit
			var phonetic string
			if err := rows.Scan(&h.HeadwordID, &h.MatchedForm, &h.Word, &h.Frequency, &phonetic); err != nil {
				return err
			}
			hit(h, phonetic)
		}
		return rows.Err()
	}

	if IsGreek(query) {
		rows, err := d.db.Query(selectForms+" WHERE f.norm = ?"+preferHeadword, Norm(query))
		if err != nil {
			return nil, err
		}
		err = scan(rows, func(h GreekHit, _ string) {
			h.Kind = NormMatch
			if h.MatchedForm == query {
				h.Kind = Exact
			}
			keep(h)
		})
		if err != nil {
			return nil, err
		}
	}

	keys := QueryKeys(query)
	if len(keys) > 0 {
		penalties := map[string]int{}
		args := make([]any, len(keys))
		for i, k := range keys {
			penalties[k.Key] = k.Penalty
			args[i] = k.Key
		}
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(keys)), ", ")
		rows, err := d.db.Query(selectForms+" WHERE f.phonetic IN ("+marks+")"+preferHeadword, args...)
		if err != nil {
			return nil, err
		}
		err = scan(rows, func(h GreekHit, phonetic string) {
			h.Kind, h.Penalty = PhoneticMatch, penalties[phonetic]
			keep(h)
		})
		if err != nil {
			return nil, err
		}
	}

	hits := make([]GreekHit, 0, len(order))
	for _, id := range order {
		hits = append(hits, best[id])
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Penalty != b.Penalty {
			return a.Penalty < b.Penalty
		}
		if a.Frequency != b.Frequency {
			return a.Frequency > b.Frequency
		}
		return a.Word < b.Word
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// EnglishHit is one Greek candidate for an English query.
type EnglishHit struct {
	Greek      string
	HeadwordID sql.NullInt64 // invalid when the Greek has no entry
	Score      int
	Gloss      string
	Note       string
	FullText   bool // from the full-text fallback
}

const selectEnglish = `
    SELECT e.greek, e.headword_id, e.score,
           COALESCE(s.gloss, (
               SELECT s2.gloss FROM entry e2 JOIN sense s2 ON s2.entry_id = e2.id
               WHERE e2.headword_id = e.headword_id
               ORDER BY s2.is_reference, e2.ord, s2.ord LIMIT 1)),
           e.note
    FROM en_index e
    LEFT JOIN headword h ON h.id = e.headword_id
    LEFT JOIN sense s ON s.id = e.sense_id
    WHERE e.term = ?
    ORDER BY e.score DESC, COALESCE(h.frequency, 0) DESC, e.greek
    LIMIT ?`

const selectEnglishFullText = `
    SELECT h.word, h.id, s.gloss
    FROM sense_fts
    JOIN sense s ON s.id = sense_fts.rowid
    JOIN entry en ON en.id = s.entry_id
    JOIN headword h ON h.id = en.headword_id
    WHERE sense_fts MATCH ?
    ORDER BY bm25(sense_fts), h.frequency DESC
    LIMIT ?`

// SearchEnglish finds Greek candidates for an English query, best first.
func (d *Dict) SearchEnglish(query string, limit int) ([]EnglishHit, error) {
	term := NormalizeTerm(query)
	if term == "" {
		return nil, nil
	}
	rows, err := d.db.Query(selectEnglish, term, limit)
	if err != nil {
		return nil, err
	}
	var hits []EnglishHit
	for rows.Next() {
		var h EnglishHit
		var gloss, note sql.NullString
		if err := rows.Scan(&h.Greek, &h.HeadwordID, &h.Score, &gloss, &note); err != nil {
			rows.Close()
			return nil, err
		}
		h.Gloss, h.Note = gloss.String, note.String
		hits = append(hits, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(hits) > 0 {
		return hits, err
	}

	// Quote each word so FTS5 syntax in user input is taken literally.
	var quoted []string
	for w := range strings.FieldsSeq(query) {
		if w = strings.ReplaceAll(w, `"`, ""); w != "" {
			quoted = append(quoted, `"`+w+`"`)
		}
	}
	if len(quoted) == 0 {
		return nil, nil
	}
	rows, err = d.db.Query(selectEnglishFullText, strings.Join(quoted, " "), limit*5)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[int64]bool{}
	for rows.Next() && len(hits) < limit {
		var h EnglishHit
		var id int64
		if err := rows.Scan(&h.Greek, &id, &h.Gloss); err != nil {
			return nil, err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		h.HeadwordID = sql.NullInt64{Int64: id, Valid: true}
		h.FullText = true
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// Sense is one numbered meaning of an entry.
type Sense struct {
	Gloss              string
	Example            string
	ExampleTranslation string
	IsReference        bool
}

// Entry is one part of speech of a headword.
type Entry struct {
	PosDisplay string
	Head       string
	Etymology  string
	Senses     []Sense
}

// Entries returns a headword's entries with their senses, in display order.
func (d *Dict) Entries(headwordID int64) ([]Entry, error) {
	rows, err := d.db.Query(`
        SELECT e.id, e.pos_display, e.head, e.etymology,
               s.gloss, s.example, s.example_translation, s.is_reference
        FROM entry e LEFT JOIN sense s ON s.entry_id = e.id
        WHERE e.headword_id = ?
        ORDER BY e.ord, s.ord`, headwordID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	lastID := int64(-1)
	for rows.Next() {
		var id int64
		var pos string
		var head, etymology, gloss, example, translation sql.NullString
		var isRef sql.NullBool
		if err := rows.Scan(&id, &pos, &head, &etymology, &gloss, &example, &translation, &isRef); err != nil {
			return nil, err
		}
		if id != lastID {
			out = append(out, Entry{PosDisplay: pos, Head: head.String, Etymology: etymology.String})
			lastID = id
		}
		if gloss.Valid {
			e := &out[len(out)-1]
			e.Senses = append(e.Senses, Sense{gloss.String, example.String, translation.String, isRef.Bool})
		}
	}
	return out, rows.Err()
}

// Gloss returns one representative English meaning of a headword, or "".
func (d *Dict) Gloss(headwordID int64) (string, error) {
	var gloss string
	err := d.db.QueryRow(`
        SELECT s.gloss FROM entry e JOIN sense s ON s.entry_id = e.id
        WHERE e.headword_id = ?
        ORDER BY s.is_reference, e.ord, s.ord LIMIT 1`, headwordID).Scan(&gloss)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return gloss, err
}

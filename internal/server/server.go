// Package server is a language server offering Greek word completion from the
// lemma dictionary.
//
// Typing Greek (with or without accents, or misspelled by sound) or Greeklish
// suggests Greek words; accepting one replaces the typed word. Hover shows the
// dictionary entry for a Greek word, or Greek translations for an English one.
package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/gataky/greek-ls/internal/lemma"
)

// ToggleGreeklish is the workspace/executeCommand that flips Greeklish input.
const ToggleGreeklish = "greek_ls.toggleGreeklish"

// LSP constants
const (
	completionKindText = 1
	textSyncFull       = 1
	errMethodNotFound  = -32601
	errInternal        = -32603
)

// Options are the server settings, read from initializationOptions.
type Options struct {
	DB        string `json:"db"`        // path to lemma_greek_en.db
	Greeklish bool   `json:"greeklish"` // offer Greek words for Latin input
	MinChars  int    `json:"min_chars"` // shortest word that gets suggestions
	MaxItems  int    `json:"max_items"` // suggestions per request
}

// DefaultOptions are used for any option the client leaves out.
func DefaultOptions(db string) Options {
	return Options{DB: db, Greeklish: true, MinChars: 2, MaxItems: 30}
}

// Server holds one client session.
type Server struct {
	opts     Options
	dict     *lemma.Dict
	docs     map[string]string
	encoding string
	out      *bufio.Writer
	shutdown bool
}

func New(opts Options) *Server {
	return &Server{opts: opts, docs: map[string]string{}, encoding: "utf-16"}
}

// ----------------------------------------------------------------- transport

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

func (m *message) isRequest() bool { return len(m.ID) > 0 && m.Method != "" }

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) send(v any) {
	body, err := json.Marshal(v)
	if err != nil {
		panic(err) // only our own types are sent
	}
	fmt.Fprintf(s.out, "Content-Length: %d\r\n\r\n", len(body))
	s.out.Write(body)
	s.out.Flush()
}

func (s *Server) respond(id json.RawMessage, result any) {
	s.send(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{"2.0", id, result})
}

func (s *Server) respondError(id json.RawMessage, code int, msg string) {
	s.send(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   rpcError        `json:"error"`
	}{"2.0", id, rpcError{code, msg}})
}

func (s *Server) log(text string) {
	s.send(map[string]any{
		"jsonrpc": "2.0", "method": "window/logMessage",
		"params": map[string]any{"type": 3, "message": "greek_ls: " + text},
	})
}

func readMessages(in io.Reader, inbox chan<- *message) {
	defer close(inbox)
	r := bufio.NewReader(in)
	for {
		length := -1
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
				length, _ = strconv.Atoi(strings.TrimSpace(value))
			}
		}
		if length < 0 {
			continue
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		var m message
		if err := json.Unmarshal(body, &m); err != nil {
			continue
		}
		inbox <- &m
	}
}

// Run serves LSP on in/out until the client sends exit or closes in, and
// returns the process exit code.
func (s *Server) Run(in io.Reader, out io.Writer) int {
	s.out = bufio.NewWriter(out)
	inbox := make(chan *message, 64)
	go readMessages(in, inbox)

	exitCode := func() int {
		if s.shutdown {
			return 0
		}
		return 1
	}
	for {
		m, ok := <-inbox
		if !ok {
			return exitCode()
		}
		batch := []*message{m}
	drain:
		for {
			select {
			case m, ok := <-inbox:
				if !ok {
					break drain
				}
				batch = append(batch, m)
			default:
				break drain
			}
		}
		// Answer a completion only if no newer one is already waiting:
		// typing fast queues one per keystroke and only the last matters.
		lastCompletion := -1
		for i, m := range batch {
			if m.Method == "textDocument/completion" {
				lastCompletion = i
			}
		}
		for i, m := range batch {
			if m.Method == "exit" {
				return exitCode()
			}
			if m.Method == "textDocument/completion" && i < lastCompletion {
				s.respond(m.ID, completionList{IsIncomplete: true, Items: []completionItem{}})
				continue
			}
			s.handle(m)
		}
	}
}

// ------------------------------------------------------------------ dispatch

type handler func(s *Server, params json.RawMessage) (any, error)

var handlers = map[string]handler{
	"initialize":               (*Server).initialize,
	"initialized":              noop,
	"shutdown":                 func(s *Server, _ json.RawMessage) (any, error) { s.shutdown = true; return nil, nil },
	"$/cancelRequest":          noop, // stale completions are skipped in Run
	"textDocument/didOpen":     (*Server).didOpen,
	"textDocument/didChange":   (*Server).didChange,
	"textDocument/didClose":    (*Server).didClose,
	"textDocument/completion":  (*Server).completion,
	"completionItem/resolve":   (*Server).resolve,
	"textDocument/hover":       (*Server).hover,
	"workspace/executeCommand": (*Server).executeCommand,
}

func noop(*Server, json.RawMessage) (any, error) { return nil, nil }

func (s *Server) handle(m *message) {
	h, ok := handlers[m.Method]
	if !ok {
		if m.isRequest() {
			s.respondError(m.ID, errMethodNotFound, "unhandled: "+m.Method)
		}
		return
	}
	result, err := h(s, m.Params)
	switch {
	case err != nil && m.isRequest():
		s.respondError(m.ID, errInternal, err.Error())
	case err != nil:
		s.log(m.Method + " failed: " + err.Error())
	case m.isRequest():
		s.respond(m.ID, result)
	}
}

// ----------------------------------------------------------------- lifecycle

func (s *Server) initialize(params json.RawMessage) (any, error) {
	var p struct {
		InitializationOptions json.RawMessage `json:"initializationOptions"`
		Capabilities          struct {
			General struct {
				PositionEncodings []string `json:"positionEncodings"`
			} `json:"general"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	// Decoding over the defaults keeps every option the client leaves out
	if len(p.InitializationOptions) > 0 && string(p.InitializationOptions) != "null" {
		if err := json.Unmarshal(p.InitializationOptions, &s.opts); err != nil {
			return nil, fmt.Errorf("initializationOptions: %w", err)
		}
	}
	dict, err := lemma.Open(expandHome(s.opts.DB))
	if err != nil {
		return nil, fmt.Errorf("%w (run `make install` in the greek-ls checkout, or set init_options.db or $GREEK_LS_DB)", err)
	}
	s.dict = dict

	s.encoding = "utf-16"
	for _, enc := range []string{"utf-32", "utf-16", "utf-8"} {
		if slices.Contains(p.Capabilities.General.PositionEncodings, enc) {
			s.encoding = enc
			break
		}
	}
	return map[string]any{
		"capabilities": map[string]any{
			"positionEncoding":       s.encoding,
			"textDocumentSync":       map[string]any{"openClose": true, "change": textSyncFull},
			"completionProvider":     map[string]any{"resolveProvider": true},
			"hoverProvider":          true,
			"executeCommandProvider": map[string]any{"commands": []string{ToggleGreeklish}},
		},
		"serverInfo": map[string]any{"name": "greek_ls"},
	}, nil
}

func expandHome(path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return path
}

// ----------------------------------------------------------------- documents

type textDocument struct {
	URI string `json:"uri"`
}

func (s *Server) didOpen(params json.RawMessage) (any, error) {
	var p struct {
		TextDocument struct {
			URI  string `json:"uri"`
			Text string `json:"text"`
		} `json:"textDocument"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	s.docs[p.TextDocument.URI] = p.TextDocument.Text
	return nil, nil
}

func (s *Server) didChange(params json.RawMessage) (any, error) {
	var p struct {
		TextDocument   textDocument `json:"textDocument"`
		ContentChanges []struct {
			Text string `json:"text"`
		} `json:"contentChanges"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if n := len(p.ContentChanges); n > 0 {
		s.docs[p.TextDocument.URI] = p.ContentChanges[n-1].Text
	}
	return nil, nil
}

func (s *Server) didClose(params json.RawMessage) (any, error) {
	var p struct {
		TextDocument textDocument `json:"textDocument"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	delete(s.docs, p.TextDocument.URI)
	return nil, nil
}

type position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspRange struct {
	Start position `json:"start"`
	End   position `json:"end"`
}

type positionParams struct {
	TextDocument textDocument `json:"textDocument"`
	Position     position     `json:"position"`
}

// lineAt returns the line under the position, as runes, and the cursor's
// index in it.
func (s *Server) lineAt(params json.RawMessage) (int, []rune, int, error) {
	var p positionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return 0, nil, 0, err
	}
	lines := strings.Split(s.docs[p.TextDocument.URI], "\n")
	var line []rune
	if p.Position.Line < len(lines) {
		line = []rune(strings.TrimSuffix(lines[p.Position.Line], "\r"))
	}
	return p.Position.Line, line, toIndex(line, p.Position.Character, s.encoding), nil
}

func (s *Server) span(lnum int, line []rune, start, end int) lspRange {
	return lspRange{
		Start: position{lnum, toCharacter(line, start, s.encoding)},
		End:   position{lnum, toCharacter(line, end, s.encoding)},
	}
}

// ------------------------------------------------------------ position units

// toIndex converts an LSP character offset to a rune index into line.
func toIndex(line []rune, character int, encoding string) int {
	if encoding == "utf-32" {
		return min(character, len(line))
	}
	count := 0
	for i, r := range line {
		if count >= character {
			return i
		}
		if encoding == "utf-16" {
			count += utf16.RuneLen(r)
		} else {
			count += utf8.RuneLen(r)
		}
	}
	return len(line)
}

// toCharacter converts a rune index into line to an LSP character offset.
func toCharacter(line []rune, index int, encoding string) int {
	prefix := line[:index]
	switch encoding {
	case "utf-32":
		return len(prefix)
	case "utf-16":
		n := 0
		for _, r := range prefix {
			n += utf16.RuneLen(r)
		}
		return n
	}
	return len(string(prefix))
}

// ------------------------------------------------------------------ features

func isWordChar(r rune) bool { return lemma.IsAlnum(r) || unicode.Is(unicode.M, r) }

// wordBefore returns the start of the word ending at index col.
func wordBefore(line []rune, col int) int {
	start := col
	for start > 0 && isWordChar(line[start-1]) {
		start--
	}
	return start
}

// wordAt returns the bounds of the word under or just before index col.
func wordAt(line []rune, col int) (int, int) {
	start, end := wordBefore(line, col), col
	for end < len(line) && isWordChar(line[end]) {
		end++
	}
	return start, end
}

type completionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []completionItem `json:"items"`
}

type completionItem struct {
	Label        string         `json:"label"`
	LabelDetails map[string]any `json:"labelDetails,omitempty"`
	Kind         int            `json:"kind"`
	FilterText   string         `json:"filterText"`
	SortText     string         `json:"sortText"`
	TextEdit     textEdit       `json:"textEdit"`
	Data         itemData       `json:"data"`
}

type textEdit struct {
	Range   lspRange `json:"range"`
	NewText string   `json:"newText"`
}

type itemData struct {
	HeadwordID int64  `json:"headword_id"`
	Word       string `json:"word"`
	Form       string `json:"form"`
}

func (s *Server) completion(params json.RawMessage) (any, error) {
	lnum, line, col, err := s.lineAt(params)
	if err != nil {
		return nil, err
	}
	start := wordBefore(line, col)
	typed := string(line[start:col])
	// Incomplete: the client must ask again on every keystroke, because the
	// suggestions for "spit" are not a filtered subset of those for "spi" by
	// any text the client can see.
	list := completionList{IsIncomplete: true, Items: []completionItem{}}
	if col-start < s.opts.MinChars {
		return list, nil
	}

	suggestions, err := Complete(s.dict, typed, s.opts.Greeklish, s.opts.MaxItems)
	if err != nil {
		return nil, err
	}
	rng := s.span(lnum, line, start, col)
	for i, sug := range suggestions {
		gloss, err := shortGloss(s.dict, sug.HeadwordID, 50)
		if err != nil {
			return nil, err
		}
		details := map[string]any{"description": gloss}
		if sug.Form != sug.Word {
			details["detail"] = "← " + sug.Word
		}
		text := matchCase(sug.Form, typed)
		list.Items = append(list.Items, completionItem{
			Label:        text,
			LabelDetails: details,
			Kind:         completionKindText,
			// Echo the typed text so clients that filter by prefix keep every
			// item; the order is ours, so sort by position.
			FilterText: typed,
			SortText:   fmt.Sprintf("%04d", i),
			TextEdit:   textEdit{rng, text},
			Data:       itemData{sug.HeadwordID, sug.Word, sug.Form},
		})
	}
	return list, nil
}

func (s *Server) resolve(params json.RawMessage) (any, error) {
	// Keep every field the client sent; only add documentation.
	var item map[string]any
	if err := json.Unmarshal(params, &item); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(item["data"])
	var data itemData
	if json.Unmarshal(raw, &data) == nil && data.HeadwordID != 0 {
		doc, err := entryMarkdown(s.dict, data.HeadwordID, data.Word, data.Form)
		if err != nil {
			return nil, err
		}
		item["documentation"] = map[string]any{"kind": "markdown", "value": doc}
	}
	return item, nil
}

func (s *Server) hover(params json.RawMessage) (any, error) {
	lnum, line, col, err := s.lineAt(params)
	if err != nil {
		return nil, err
	}
	start, end := wordAt(line, col)
	word := string(line[start:end])
	if !strings.ContainsFunc(word, unicode.IsLetter) {
		return nil, nil
	}

	var value string
	if lemma.IsGreek(word) {
		value, err = s.greekHover(word)
	} else {
		var hits []lemma.EnglishHit
		hits, err = s.dict.SearchEnglish(word, 8)
		if len(hits) > 0 {
			value = englishMarkdown(hits, word)
		}
	}
	if err != nil || value == "" {
		return nil, err
	}
	return map[string]any{
		"contents": map[string]any{"kind": "markdown", "value": value},
		"range":    s.span(lnum, line, start, end),
	}, nil
}

// greekHover shows the exact matches of word, else its accent-insensitive
// matches, else its best sound-alike.
func (s *Server) greekHover(word string) (string, error) {
	all, err := s.dict.SearchGreek(word, 5)
	if err != nil {
		return "", err
	}
	var exact, near []lemma.GreekHit
	for _, h := range all {
		if h.Kind == lemma.Exact {
			exact = append(exact, h)
		}
		if h.Kind <= lemma.NormMatch {
			near = append(near, h)
		}
	}
	hits := exact
	if len(hits) == 0 {
		hits = near
	}
	if len(hits) == 0 && len(all) > 0 {
		hits = all[:1]
	}
	var parts []string
	for _, h := range hits[:min(3, len(hits))] {
		md, err := entryMarkdown(s.dict, h.HeadwordID, h.Word, h.MatchedForm)
		if err != nil {
			return "", err
		}
		parts = append(parts, md)
	}
	return strings.Join(parts, "\n\n---\n\n"), nil
}

func (s *Server) executeCommand(params json.RawMessage) (any, error) {
	var p struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if p.Command != ToggleGreeklish {
		return nil, fmt.Errorf("unknown command %s", p.Command)
	}
	s.opts.Greeklish = !s.opts.Greeklish
	return map[string]any{"greeklish": s.opts.Greeklish}, nil
}

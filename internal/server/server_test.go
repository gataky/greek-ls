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
	"testing"

	"github.com/gataky/greek-ls/internal/lemma"
)

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

func openDict(t testing.TB) *lemma.Dict {
	t.Helper()
	d, err := lemma.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func forms(t *testing.T, d *lemma.Dict, typed string, greeklish bool) []string {
	t.Helper()
	got, err := Complete(d, typed, greeklish, 30)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range got {
		out = append(out, s.Form)
	}
	return out
}

func first(t *testing.T, d *lemma.Dict, typed string) string {
	t.Helper()
	f := forms(t, d, typed, true)
	if len(f) == 0 {
		t.Fatalf("no suggestions for %q", typed)
	}
	return f[0]
}

func TestComplete(t *testing.T) {
	d := openDict(t)
	for typed, want := range map[string]string{
		"σπιτ": "σπίτι", // no accents
		"σπητ": "σπίτι", // misspelled by sound
		"spit": "σπίτι", "kalim": "καλημέρα", "thal": "θάλασσα", "eyxar": "ευχαριστώ",
		"efx": "ευχαριστώ", "anthr": "άνθρωπος", "mpamp": "μπαμπάς", "8alas": "θάλασσα",
		"xeri": "χέρι", // whole-word match first
	} {
		if got := first(t, d, typed); got != want {
			t.Errorf("Complete(%q)[0] = %s, want %s", typed, got, want)
		}
	}
}

func TestCompleteInflectedForm(t *testing.T) {
	d := openDict(t)
	got, _ := Complete(d, "σπιτιο", true, 30)
	if len(got) == 0 || got[0].Form != "σπιτιού" || got[0].Word != "σπίτι" {
		t.Errorf("Complete(σπιτιο) = %+v", got)
	}
}

func TestCompleteSoundAlikesIncluded(t *testing.T) {
	d := openDict(t)
	if !slices.Contains(forms(t, d, "xeri", true), "ξέρει") {
		t.Error("xeri should also offer ξέρει")
	}
}

func TestCompleteSpreadsAcrossHeadwords(t *testing.T) {
	d := openDict(t)
	got, _ := Complete(d, "σπ", true, 8)
	seen := map[int64]bool{}
	for _, s := range got {
		seen[s.HeadwordID] = true
	}
	if len(seen) != 8 {
		t.Errorf("σπ: top 8 come from %d headwords: %+v", len(seen), got)
	}
}

func TestCompleteSkipsPhrasesAndStrayCapitals(t *testing.T) {
	d := openDict(t)
	for _, typed := range []string{"ειπ", "καλη", "xe", "geia"} {
		for _, f := range forms(t, d, typed, true) {
			if strings.Contains(f, " ") {
				t.Errorf("%s: phrase %q", typed, f)
			}
		}
	}
	if slices.Contains(forms(t, d, "ειπ", true), "Ειπώθηκα") {
		t.Error("ειπ offers Ειπώθηκα")
	}
}

func TestCompleteGreeklishOff(t *testing.T) {
	d := openDict(t)
	if f := forms(t, d, "spit", false); len(f) != 0 {
		t.Errorf("Greeklish off: %v", f)
	}
	if f := forms(t, d, "σπιτ", false); len(f) == 0 || f[0] != "σπίτι" {
		t.Errorf("Greek with Greeklish off: %v", f)
	}
}

func TestWords(t *testing.T) {
	line := []rune("το σπίτι μου")
	if s := wordBefore(line, 7); string(line[s:7]) != "σπίτ" {
		t.Errorf("wordBefore = %q", string(line[s:7]))
	}
	if s, e := wordAt(line, 5); string(line[s:e]) != "σπίτι" {
		t.Errorf("wordAt = %q", string(line[s:e]))
	}
	line = []rune("hello, spi")
	if s := wordBefore(line, 10); s != 7 {
		t.Errorf("wordBefore after comma = %d", s)
	}
}

func TestPositionUnits(t *testing.T) {
	line := []rune("😀 σπίτι") // the emoji is two UTF-16 units, four UTF-8 bytes
	for _, c := range []struct {
		enc              string
		index, character int
	}{{"utf-16", 2, 3}, {"utf-8", 2, 5}, {"utf-32", 2, 2}} {
		if got := toCharacter(line, c.index, c.enc); got != c.character {
			t.Errorf("%s toCharacter = %d, want %d", c.enc, got, c.character)
		}
		if got := toIndex(line, c.character, c.enc); got != c.index {
			t.Errorf("%s toIndex = %d, want %d", c.enc, got, c.index)
		}
	}
}

// ------------------------------------------------------------------ protocol

type client struct {
	t      *testing.T
	in     *io.PipeWriter
	out    *bufio.Reader
	nextID int
	done   chan int
}

func startServer(t *testing.T) *client {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &client{t: t, in: inW, out: bufio.NewReader(outR), done: make(chan int, 1)}
	go func() {
		c.done <- New(DefaultOptions(dbPath(t))).Run(inR, outW)
		outW.Close()
	}()
	t.Cleanup(func() {
		c.request("shutdown", nil)
		c.notify("exit", nil)
		if code := <-c.done; code != 0 {
			t.Errorf("exit code %d", code)
		}
		inW.Close()
	})
	return c
}

func (c *client) write(msg map[string]any) {
	msg["jsonrpc"] = "2.0"
	body, _ := json.Marshal(msg)
	fmt.Fprintf(c.in, "Content-Length: %d\r\n\r\n%s", len(body), body)
}

func (c *client) read() map[string]any {
	length := 0
	for {
		line, err := c.out.ReadString('\n')
		if err != nil {
			c.t.Fatal(err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if v, ok := strings.CutPrefix(line, "Content-Length: "); ok {
			length, _ = strconv.Atoi(v)
		}
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(c.out, body); err != nil {
		c.t.Fatal(err)
	}
	var msg map[string]any
	if err := json.Unmarshal(body, &msg); err != nil {
		c.t.Fatal(err)
	}
	return msg
}

func (c *client) request(method string, params any) map[string]any {
	c.nextID++
	c.write(map[string]any{"id": c.nextID, "method": method, "params": params})
	for {
		msg := c.read()
		if id, ok := msg["id"].(float64); ok && int(id) == c.nextID {
			if e, ok := msg["error"]; ok {
				c.t.Fatalf("%s: %v", method, e)
			}
			return msg
		}
	}
}

func (c *client) notify(method string, params any) {
	c.write(map[string]any{"method": method, "params": params})
}

func (c *client) open(text string, encodings ...string) map[string]any {
	if len(encodings) == 0 {
		encodings = []string{"utf-16"}
	}
	init := c.request("initialize", map[string]any{
		"capabilities":          map[string]any{"general": map[string]any{"positionEncodings": encodings}},
		"initializationOptions": map[string]any{"db": dbPath(c.t)},
	})
	c.notify("initialized", map[string]any{})
	c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{
		"uri": "file:///t.md", "languageId": "markdown", "version": 1, "text": text}})
	return init["result"].(map[string]any)
}

func at(line, character int) map[string]any {
	return map[string]any{
		"textDocument": map[string]any{"uri": "file:///t.md"},
		"position":     map[string]any{"line": line, "character": character},
	}
}

// get walks a decoded JSON value: get(v, "items", 0, "label").
func get(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			v = v.(map[string]any)[k]
		case int:
			v = v.([]any)[k]
		}
	}
	return v
}

func TestProtocolCompletionReplacesGreeklish(t *testing.T) {
	c := startServer(t)
	c.open("Kalimer")
	result := c.request("textDocument/completion", at(0, 7))["result"]
	if get(result, "isIncomplete") != true {
		t.Error("list should be incomplete")
	}
	item := get(result, "items", 0)
	if got := get(item, "textEdit", "newText"); got != "Καλημέρα" { // capitalized like the input
		t.Errorf("newText = %v", got)
	}
	if got := get(item, "filterText"); got != "Kalimer" {
		t.Errorf("filterText = %v", got)
	}
	if got := get(item, "textEdit", "range", "start", "character"); got != 0.0 {
		t.Errorf("range start = %v", got)
	}

	resolved := c.request("completionItem/resolve", item)["result"]
	if doc := get(resolved, "documentation", "value").(string); !strings.Contains(strings.ToLower(doc), "good morning") {
		t.Errorf("documentation = %q", doc)
	}
	if get(resolved, "label") != "Καλημέρα" {
		t.Error("resolve should keep the item's fields")
	}
}

func TestProtocolUTF16AfterEmoji(t *testing.T) {
	c := startServer(t)
	if enc := c.open("😀 spit")["capabilities"].(map[string]any)["positionEncoding"]; enc != "utf-16" {
		t.Errorf("encoding = %v", enc)
	}
	result := c.request("textDocument/completion", at(0, 7))["result"]
	if got := get(result, "items", 0, "textEdit", "range", "start", "character"); got != 3.0 {
		t.Errorf("range start = %v", got)
	}
}

func TestProtocolPrefersUTF8(t *testing.T) {
	c := startServer(t)
	caps := c.open("x", "utf-16", "utf-32", "utf-8")["capabilities"].(map[string]any)
	if caps["positionEncoding"] != "utf-8" {
		t.Errorf("encoding = %v", caps["positionEncoding"])
	}
	c = startServer(t)
	caps = c.open("x", "utf-16", "utf-32")["capabilities"].(map[string]any)
	if caps["positionEncoding"] != "utf-32" {
		t.Errorf("without utf-8, encoding = %v", caps["positionEncoding"])
	}
}

// A second word after Greek text: offsets are bytes, as Neovim negotiates
// (σπίτι is 10 bytes), and the range covers only the word being typed.
func TestProtocolWordAfterGreek(t *testing.T) {
	c := startServer(t)
	c.open("σπίτι spit", "utf-8", "utf-16", "utf-32")
	result := c.request("textDocument/completion", at(0, 15))["result"]
	if got := get(result, "items", 0, "textEdit", "newText"); got != "σπίτι" {
		t.Errorf("newText = %v", got)
	}
	if got := get(result, "items", 0, "textEdit", "range", "start", "character"); got != 11.0 {
		t.Errorf("range start = %v, want byte 11", got)
	}
}

func TestProtocolHover(t *testing.T) {
	c := startServer(t)
	c.open("είπα\nbank\n")
	greek := get(c.request("textDocument/hover", at(0, 1))["result"], "contents", "value").(string)
	if !strings.Contains(greek, "είπα → **λέω**") {
		t.Errorf("Greek hover = %q", greek)
	}
	english := get(c.request("textDocument/hover", at(1, 1))["result"], "contents", "value").(string)
	if !strings.Contains(english, "τράπεζα") {
		t.Errorf("English hover = %q", english)
	}
}

func TestProtocolToggleGreeklish(t *testing.T) {
	c := startServer(t)
	c.open("spit")
	toggled := c.request("workspace/executeCommand", map[string]any{"command": ToggleGreeklish})["result"]
	if get(toggled, "greeklish") != false {
		t.Errorf("toggle = %v", toggled)
	}
	if items := get(c.request("textDocument/completion", at(0, 4))["result"], "items").([]any); len(items) != 0 {
		t.Errorf("items with Greeklish off = %v", items)
	}
}

func TestProtocolUnknownMethod(t *testing.T) {
	c := startServer(t)
	c.open("")
	c.nextID++
	c.write(map[string]any{"id": c.nextID, "method": "textDocument/definition", "params": at(0, 0)})
	msg := c.read()
	if code := get(msg, "error", "code"); code != float64(errMethodNotFound) {
		t.Errorf("error = %v", msg)
	}
}

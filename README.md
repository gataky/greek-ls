# greek-ls

A language server that suggests Greek words as you type, from the
`lemma_greek_en.db` Greek-English dictionary.

| You type | You get |
|---|---|
| `σπιτ` (no accents) | σπίτι, σπιτικό, … |
| `σπητ` (misspelled by sound) | σπίτι, … |
| `spit`, `kalimer`, `eyxar` (Greeklish) | σπίτι, καλημέρα, ευχαριστώ |
| `σπιτιο` | σπιτιού ← σπίτι (inflected forms) |
| `Kalimer` | Καλημέρα (capitalization follows yours) |

Accepting a suggestion replaces the typed word. The completion info window and
hover (`K`) show the dictionary entry. Hover on an English word lists Greek
translations.

## Install

Requires Go 1.27 or newer.

```sh
make install
```

This installs:

- the server to `~/.local/bin/greek-ls`
- the dictionary to `~/.local/share/greek-ls/lemma_greek_en.db`
  (`$XDG_DATA_HOME/greek-ls` if you set `XDG_DATA_HOME`)

The server finds the dictionary there on its own, so there's nothing to
configure. Make sure `~/.local/bin` is on your `PATH`. `make install` warns if
it isn't.

To install somewhere else, override `BINDIR` and `DATADIR`:

```sh
make install BINDIR=/usr/local/bin DATADIR=/usr/local/share/greek-ls
```

If you change `DATADIR`, tell the server where the dictionary is, with
`init_options.db` or `$GREEK_LS_DB` (see [Server options](#server-options)).

Run `make install` again after pulling changes. The dictionary is only copied
again when it has changed. `make uninstall` removes both files.

## Neovim

Neovim 0.11 or newer. Add this to your config:

```lua
vim.lsp.config('greek_ls', {
    cmd = { 'greek-ls' },
    filetypes = { 'markdown', 'text' },
})
vim.lsp.enable('greek_ls')
```

Suggestions appear through your LSP completion plugin. It has been tested with
[mini.completion](https://github.com/nvim-mini/mini.nvim). Other plugins that
re-request on `isIncomplete` and filter by `filterText`, like nvim-cmp and
blink.cmp, should work but haven't been tried.

If Neovim doesn't find `greek-ls` (a GUI-launched Neovim may not have
`~/.local/bin` on its `PATH`), use the full path:
`cmd = { vim.fn.expand('~/.local/bin/greek-ls') }`.

### Toggling Greeklish

Greeklish suggestions get in the way when you write English. The server has a
command to turn them off and on:

```lua
vim.api.nvim_create_autocmd('LspAttach', {
    callback = function(args)
        local client = vim.lsp.get_client_by_id(args.data.client_id)
        if client.name ~= 'greek_ls' then
            return
        end
        vim.keymap.set('n', '<leader>gl', function()
            client:request('workspace/executeCommand', { command = 'greek_ls.toggleGreeklish' }, function(err, result)
                if not err then
                    print('Greeklish ' .. (result.greeklish and 'ON' or 'OFF'))
                end
            end, args.buf)
        end, { buffer = args.buf, desc = 'Toggle Greeklish suggestions' })
    end,
})
```

Typing in Greek letters works at any time. Vim's built-in Greek keymap
(`:set keymap=greek`, toggled with `<C-^>` in insert mode) is one way.

## Server options

Passed as `init_options`:

| key | default | |
|---|---|---|
| `db` | see below | path to the dictionary |
| `greeklish` | `true` | suggest Greek for Latin input |
| `min_chars` | `2` | shortest word that gets suggestions |
| `max_items` | `30` | suggestions per request |

Without `db`, the server uses the first of:

1. the `-db` flag
2. `$GREEK_LS_DB`
3. `lemma_greek_en.db` one level above the binary's directory, so a checkout's
   `bin/greek-ls` uses the checkout's dictionary
4. `$XDG_DATA_HOME/greek-ls/lemma_greek_en.db` (default
   `~/.local/share/greek-ls`), where `make install` puts it

## How it ranks suggestions

1. The typed text matches a whole form (`xeri` → χέρι).
2. Prefix matches, each headword's best form first. One word's inflections
   can't crowd out other words: `σπ` gives σπίτι, σπαθί, σπάνια … before σπίτια.
3. Within those, the more common Greeklish reading comes first, then frequency.
4. For Greek input, sound-alike matches (spelling help) come after the
   accent-insensitive ones.

Multi-word headwords (proverbs) are skipped. So are capitalized forms of
lowercase words.

### How it works with completion plugins

The server echoes the typed text as each item's `filterText`, so prefix filters
keep `σπίτι` for the input `spit`. It also sends a `textEdit` that replaces the
typed word, and marks every list `isIncomplete`, so the client asks again on
each keystroke. If several requests queue up while you type fast, only the
newest one is answered.

## Development

Go is pinned in `.tool-versions` for asdf. The optional `.envrc` (run
`direnv allow` once) puts `bin/` on `PATH` and points `GREEK_LS_DB` at the
checkout's dictionary.

```sh
make build   # bin/greek-ls
make test    # includes recomputing the stored key of all 317,514 forms
make help    # everything else
```

- `cmd/greek-ls`: the binary (LSP over stdio).
- `internal/lemma`: the dictionary. It is a port of the Python reference in
  `sqlite-database.md`: lookup keys, Greek/English search, entries. Its tests
  run every test vector from the guide and check that it reproduces every
  stored `norm`/`phonetic` key.
- `internal/server`: completion ranking, hover, and the LSP protocol.

SQLite is [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite), a pure-Go
driver, so the build needs no cgo or C compiler.

## License

The dictionary content comes from Wiktionary via kaikki.org (CC BY-SA 4.0).
Word frequencies come from FrequencyWords (MIT).

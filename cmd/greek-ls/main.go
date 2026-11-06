// Command greek-ls is a language server suggesting Greek words, from Greek or
// Greeklish input, out of the lemma Greek-English dictionary. It speaks LSP
// over stdio.
//
// The dictionary path is the first of:
//   - initializationOptions.db
//   - the -db flag
//   - $GREEK_LS_DB
//   - lemma_greek_en.db one level above the binary's directory, if present
//     (a checkout's bin/greek-ls finds the checkout's dictionary)
//   - $XDG_DATA_HOME/greek-ls/lemma_greek_en.db, where `make install` puts it
//     ($XDG_DATA_HOME defaults to ~/.local/share)
package main

import (
	"flag"
	"os"
	"path/filepath"

	"github.com/gataky/greek-ls/internal/server"
)

const dbName = "lemma_greek_en.db"

func defaultDB() string {
	if p := os.Getenv("GREEK_LS_DB"); p != "" {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		if p := filepath.Join(filepath.Dir(filepath.Dir(exe)), dbName); fileExists(p) {
			return p
		}
	}
	return filepath.Join(dataHome(), "greek-ls", dbName)
}

func dataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".local", "share")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func main() {
	db := flag.String("db", defaultDB(), "path to "+dbName)
	flag.Parse()
	os.Exit(server.New(server.DefaultOptions(*db)).Run(os.Stdin, os.Stdout))
}

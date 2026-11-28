package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Meldungen, die schon englisch bzw. sprachneutral sind und keine Übersetzung brauchen.
var englishOK = map[string]bool{
	"Worker %d Panic: %v": true,
	"EmptyClipboard: %v":  true, "GlobalAlloc: %v": true, "GlobalLock: %v": true, "SetClipboardData: %v": true,
}

// messageSinks: Aufrufe, deren Text in Log, Fehlermeldung oder Oberfläche landet.
var messageSinks = map[string]bool{"Log": true, "LogSecret": true, "logFn": true, "Errorf": true, "New": true}

// messageTargets: Felder/Variablen, deren Inhalt die Oberfläche zeigt.
var messageTargets = map[string]bool{"Error": true, "ConnNote": true, "PhaseLabel": true, "phaseLabel": true, "label": true, "msg": true}

func callName(e ast.Expr) string {
	switch f := e.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if f.Sel.Name == "New" {
			if x, ok := f.X.(*ast.Ident); !ok || x.Name != "errors" {
				return ""
			}
		}
		return f.Sel.Name
	}
	return ""
}

// messageLiterals liefert die Text-Literale eines Ausdrucks (direkt, in Sprintf/Errorf, in append(notes, ...)).
func messageLiterals(e ast.Expr) []*ast.BasicLit {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			return []*ast.BasicLit{v}
		}
	case *ast.CallExpr:
		switch callName(v.Fun) {
		case "Sprintf", "Errorf":
			if len(v.Args) > 0 {
				return messageLiterals(v.Args[0])
			}
		case "append":
			var out []*ast.BasicLit
			for _, a := range v.Args[1:] {
				out = append(out, messageLiterals(a)...)
			}
			return out
		}
	case *ast.BinaryExpr:
		return append(messageLiterals(v.X), messageLiterals(v.Y)...)
	}
	return nil
}

func lastName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	}
	return ""
}

var hasWord = regexp.MustCompile(`[A-Za-zÄÖÜäöüß]{3,}`)

// Jede Meldung, die in Log, Fehlern oder Oberfläche landet, braucht eine englische Fassung.
// Gefunden wird über die Aufrufe (Log, logFn, Errorf, errors.New, ...), nicht über eine Wortliste:
// so fällt auch eine neue Meldung ohne Umlaut oder typische Wörter auf.
func TestAllMessagesHaveEnglish(t *testing.T) {
	fset := token.NewFileSet()
	checked := 0
	check := func(lit *ast.BasicLit) {
		s, err := strconv.Unquote(lit.Value)
		if err != nil || !hasWord.MatchString(s) || englishOK[s] {
			return
		}
		checked++
		sample := fmtVerb.ReplaceAllStringFunc(s, func(v string) string {
			if v == "%%" {
				return "%"
			}
			return "X"
		})
		if toEN(sample) == sample {
			t.Errorf("%s: keine englische Fassung für %q", fset.Position(lit.Pos()), s)
		}
	}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "web") {
			return filepath.SkipDir
		}
		path = filepath.ToSlash(path)
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || path == "i18n.go" {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CallExpr:
				name := callName(v.Fun)
				if name == "snapshot" && len(v.Args) == 2 {
					for _, l := range messageLiterals(v.Args[1]) {
						check(l)
					}
				}
				if !messageSinks[name] || len(v.Args) == 0 {
					return true
				}
				args := v.Args
				if name == "LogSecret" || name == "Errorf" || name == "New" {
					args = args[:1] // LogSecret: zweiter Text geht nur in die Konsole
				}
				for _, a := range args {
					for _, l := range messageLiterals(a) {
						check(l)
					}
				}
			case *ast.AssignStmt:
				for i, lhs := range v.Lhs {
					if i < len(v.Rhs) && (messageTargets[lastName(lhs)] || lastName(lhs) == "notes") {
						for _, l := range messageLiterals(v.Rhs[i]) {
							check(l)
						}
					}
				}
			case *ast.KeyValueExpr:
				if messageTargets[lastName(v.Key)] {
					for _, l := range messageLiterals(v.Value) {
						check(l)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 100 {
		t.Fatalf("nur %d Meldungen geprüft, Suche kaputt?", checked)
	}
}

func TestToENNestedAndFragments(t *testing.T) {
	for in, want := range map[string]string{
		"Abbruch (INPUT): Die Datei ist leer.":                         "Aborted (INPUT): The file is empty.",
		"Starte ParPar (10% Redundanz, 4M Slices)...":                  "Starting ParPar (10% redundancy, 4M slices)...",
		"Übersprungen: .DS_Store, leer.txt (leer), a (Verknüpfung)":    "Skipped: .DS_Store, leer.txt (empty), a (link)",
		"Abbruch (UPLOAD): Upload abgebrochen: i/o timeout":            "Aborted (UPLOAD): Upload cancelled: i/o timeout",
		"Arbeitsordner: /home/x/work (/tmp liegt im Arbeitsspeicher).": "Work folder: /home/x/work (/tmp is in RAM).",
		"unbekannt bleibt": "unbekannt bleibt",
	} {
		if got := toEN(in); got != want {
			t.Errorf("toEN(%q) = %q, want %q", in, got, want)
		}
	}
}

// Jedes deutsche Format nur einmal (bei einer Liste prüft das der Compiler nicht mehr).
func TestEnFormatsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range enFormats {
		if seen[p[0]] {
			t.Errorf("doppelt: %q", p[0])
		}
		seen[p[0]] = true
	}
}

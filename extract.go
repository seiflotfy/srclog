package srclog

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// callKind describes how a recognized call carries its message.
type callKind int

const (
	kindPrintf  callKind = iota // format string with verbs (Printf, Errorf, Msgf)
	kindPrint                   // fmt.Sprint semantics: extra operands appended, no separator (Print, logrus.Info)
	kindPrintln                 // fmt.Sprintln semantics: extra operands appended, space-separated
	kindMsg                     // constant message; extra args are structured fields
)

// levelByBase maps a method base name (suffixes stripped) to a log level.
var levelByBase = map[string]string{
	"Trace":   "trace",
	"Debug":   "debug",
	"Info":    "info",
	"Warn":    "warn",
	"Warning": "warn",
	"Error":   "error",
	"Fatal":   "fatal",
	"Panic":   "panic",
	"DPanic":  "panic",
	"Print":   "info",
}

// skipRecv lists package idents whose Error/Print-family functions are not
// logging (fmt.Errorf builds errors, http.Error writes responses, zap.Error
// constructs a field — zap logs only through logger values, never the package).
var skipRecv = map[string]bool{"fmt": true, "http": true, "errors": true, "testing": true, "zap": true}

// Extract walks dir for Go files (skipping _test.go, vendor/, testdata/ and
// dot-dirs), parses them syntax-only, and returns the deduplicated log
// template manifest. Commit is left empty for the caller to fill.
func Extract(dir string) (*Manifest, error) {
	return extractManifest(dir, visitCall)
}

// ExtractErrors walks dir like Extract but harvests error-construction sites
// (fmt.Errorf, errors.New/Wrap/Wrapf/Errorf, grpc status.Error/Errorf)
// instead of log calls, producing a suffix-anchored dictionary manifest.
// Point it at vendor/ or a module checkout to build a dictionary for the
// services a repo actually depends on.
func ExtractErrors(dir string) (*Manifest, error) {
	m, err := extractManifest(dir, visitErrCall)
	if err != nil {
		return nil, err
	}
	m.Anchor = "suffix"
	// The constructing package (fmt, errors, status) says nothing about the
	// service; label entries with the scanned module instead when known.
	if lib := shortModule(m.Module); lib != "" {
		for _, t := range m.Templates {
			t.Lib = lib
		}
	}
	return m, nil
}

// shortModule reduces a module path to a service-ish name:
// github.com/jackc/pgx/v5 → pgx, github.com/lib/pq → pq.
func shortModule(module string) string {
	parts := strings.Split(module, "/")
	for len(parts) > 1 {
		last := parts[len(parts)-1]
		if len(last) > 1 && last[0] == 'v' && strings.TrimLeft(last[1:], "0123456789") == "" {
			parts = parts[:len(parts)-1]
			continue
		}
		return strings.TrimSuffix(last, ".go") // nats.go-style module names
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return ""
}

type visitFunc func(fset *token.FileSet, file string, call *ast.CallExpr, byKey map[string]*Template, stats *Stats)

func extractManifest(dir string, visit visitFunc) (*Manifest, error) {
	fset := token.NewFileSet()
	byKey := map[string]*Template{} // level + NUL + template
	imports := map[string]bool{}
	var stats Stats

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == dir {
				return nil
			}
			name := d.Name()
			if name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			stats.ParseErrors++
			return nil
		}
		stats.Files++
		for _, imp := range f.Imports {
			if p, err := strconv.Unquote(imp.Path.Value); err == nil {
				imports[p] = true
			}
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			rel = path
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			visit(fset, rel, call, byKey, &stats)
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	templates := make([]*Template, 0, len(byKey))
	for _, t := range byKey {
		sort.Slice(t.Locations, func(i, j int) bool {
			if t.Locations[i].File != t.Locations[j].File {
				return t.Locations[i].File < t.Locations[j].File
			}
			return t.Locations[i].Line < t.Locations[j].Line
		})
		templates = append(templates, t)
	}
	sort.Slice(templates, func(i, j int) bool {
		if templates[i].Template != templates[j].Template {
			return templates[i].Template < templates[j].Template
		}
		return templates[i].Level < templates[j].Level
	})

	return &Manifest{
		Version:          1,
		Module:           moduleName(dir),
		Stats:            stats,
		Templates:        templates,
		RecommendedDicts: recommendDicts(imports),
	}, nil
}

// dictByImportPrefix maps client-library import prefixes to the shipped
// dictionary (dicts/<name>.json) that covers their service's errors.
var dictByImportPrefix = map[string]string{
	"github.com/lib/pq":              "postgres",
	"github.com/jackc/pgx":           "postgres",
	"github.com/redis/go-redis":      "redis",
	"github.com/go-redis/redis":      "redis",
	"github.com/go-sql-driver/mysql": "mysql",
	"google.golang.org/grpc":         "grpc",
	"github.com/IBM/sarama":          "kafka",
	"github.com/Shopify/sarama":      "kafka",
	"github.com/segmentio/kafka-go":  "kafka",
	"github.com/nats-io/nats.go":     "nats",
	"github.com/aws/aws-sdk-go":      "aws",
	"github.com/aws/aws-sdk-go-v2":   "aws",
}

// recommendDicts derives dictionary names from the import graph the walk
// already saw. stdlib is always included — every Go program wraps its errors.
func recommendDicts(imports map[string]bool) []string {
	set := map[string]bool{"stdlib": true}
	for imp := range imports {
		for prefix, dict := range dictByImportPrefix {
			if imp == prefix || strings.HasPrefix(imp, prefix+"/") {
				set[dict] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// visitCall recognizes one call expression as a log call site and records its
// template. Recognition is by method name only — no type checking — so any
// receiver with an Errorf/Info/Msg-shaped method is treated as a logger.
// Over-approximation is harmless here: extra templates just sit unmatched.
func visitCall(fset *token.FileSet, file string, call *ast.CallExpr, byKey map[string]*Template, stats *Stats) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	if id, ok := sel.X.(*ast.Ident); ok && skipRecv[id.Name] {
		return
	}

	name := sel.Sel.Name
	var (
		base string
		kind callKind
	)
	msgIdx := 0
	if strings.HasSuffix(name, "Context") {
		name = strings.TrimSuffix(name, "Context")
		msgIdx = 1 // ctx comes first (slog.InfoContext, ...)
	}
	switch {
	case strings.HasSuffix(name, "f"):
		base, kind = name[:len(name)-1], kindPrintf
	case strings.HasSuffix(name, "ln"):
		base, kind = name[:len(name)-2], kindPrintln
	case strings.HasSuffix(name, "w"):
		base, kind = name[:len(name)-1], kindMsg // zap sugared Infow etc.
	default:
		base, kind = name, kindPrint
	}

	level, isLevel := levelByBase[base]
	isZerologMsg := base == "Msg"
	if !isLevel && !isZerologMsg {
		return
	}
	if isZerologMsg {
		level = chainLevel(sel.X)
		if kind == kindPrint {
			kind = kindMsg
		}
	}
	if len(call.Args) <= msgIdx {
		return // e.g. zerolog's Error() chain root — the Msg call carries the message
	}

	extra := call.Args[msgIdx+1:]
	if kind == kindPrint && (identIs(sel.X, "slog") || (len(extra) > 0 && allZapFields(extra))) {
		kind = kindMsg // slog.Info / zap.Logger.Error: extra args are fields, not message
	}

	stats.Calls++
	tmpl, hasLit := stringTemplate(call.Args[msgIdx])
	if !hasLit {
		stats.Dynamic++
		return
	}
	switch kind {
	case kindPrintf:
		tmpl = normalizeFormat(tmpl)
	case kindPrint:
		// fmt.Sprint inserts a space only between two non-string operands,
		// which is unknowable statically; the bare placeholder's (.*?) also
		// absorbs a leading space, so unspaced is correct for both cases.
		for range extra {
			tmpl += Placeholder
		}
	case kindPrintln:
		for range extra {
			tmpl += " " + Placeholder
		}
	}

	// A template with no literal text ("%v", "%s %d", ...) is dynamic in all
	// but name — and its regex would match every line as a catch-all.
	if isZeroLiteral(tmpl) {
		stats.Dynamic++
		return
	}

	record(fset, file, call, byKey, level, libGuess(sel, base, kind), tmpl)
}

// record deduplicates by level+template and stores the call site.
func record(fset *token.FileSet, file string, call *ast.CallExpr, byKey map[string]*Template, level, lib, tmpl string) {
	pos := fset.Position(call.Pos())
	key := level + "\x00" + tmpl
	if t, ok := byKey[key]; ok {
		t.Locations = append(t.Locations, Location{File: file, Line: pos.Line})
		return
	}
	byKey[key] = &Template{
		ID:        templateID(level, tmpl),
		Template:  tmpl,
		Level:     level,
		Lib:       lib,
		Locations: []Location{{File: file, Line: pos.Line}},
	}
}

func identIs(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// isZeroLiteral reports whether a template's literal part carries no real
// information. Whitespace and punctuation alone don't count: "%s: %w" leaves
// literal ":" and would compile to a near-catch-all like (?:^|: )(.*?): (.*?)$.
// The bar is at least two letters or digits.
func isZeroLiteral(tmpl string) bool {
	alnum := 0
	for _, r := range strings.ReplaceAll(tmpl, Placeholder, "") {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			alnum++
			if alnum >= 2 {
				return false
			}
		}
	}
	return true
}

// stringTemplate folds an expression into a template string. String literals
// keep their value, non-constant parts of a concatenation become Placeholder.
// ok is false when no part of the expression is a string literal.
func stringTemplate(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			if s, err := strconv.Unquote(v.Value); err == nil {
				return s, true
			}
		}
	case *ast.BinaryExpr:
		if v.Op == token.ADD {
			l, lok := stringTemplate(v.X)
			r, rok := stringTemplate(v.Y)
			if lok || rok {
				if !lok {
					l = Placeholder
				}
				if !rok {
					r = Placeholder
				}
				return l + r, true
			}
		}
	case *ast.ParenExpr:
		return stringTemplate(v.X)
	}
	return "", false
}

// chainLevel walks a zerolog-style call chain (log.Error().Str(...).Msg(...))
// back to the zero-arg level method that started it.
func chainLevel(e ast.Expr) string {
	for {
		call, ok := e.(*ast.CallExpr)
		if !ok {
			return "unknown"
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return "unknown"
		}
		if lvl, ok := levelByBase[sel.Sel.Name]; ok && len(call.Args) == 0 {
			return lvl
		}
		e = sel.X
	}
}

// libGuess best-effort identifies the logging library. Genuinely metadata
// only: the result is stored on the template and never consulted again.
func libGuess(sel *ast.SelectorExpr, base string, kind callKind) string {
	if base == "Msg" {
		return "zerolog"
	}
	if id, ok := sel.X.(*ast.Ident); ok {
		switch id.Name {
		case "slog":
			return "slog"
		case "log":
			return "log"
		case "logrus":
			return "logrus"
		}
	}
	if kind == kindMsg {
		return "zap"
	}
	return ""
}

// allZapFields reports whether every expression is a zap field constructor
// call like zap.String(...) or zap.Error(...).
func allZapFields(args []ast.Expr) bool {
	for _, a := range args {
		call, ok := a.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "zap" {
			return false
		}
	}
	return true
}

// moduleName reads the module path from dir/go.mod, or "".
func moduleName(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

package srclog

import (
	"go/ast"
	"go/token"
)

// errFunc describes one error-constructing function.
type errFunc struct {
	msgIdx int
	printf bool // message is a format string
	wraps  bool // the built error appends ": <cause>" (pkg/errors Wrap/Wrapf)
}

// errFuncs maps package ident → method → shape. Recognition is by ident name,
// same trade-off as log extraction: "errors" covers both stdlib and
// pkg/errors, "status" covers google.golang.org/grpc/status.
var errFuncs = map[string]map[string]errFunc{
	"fmt": {
		"Errorf": {msgIdx: 0, printf: true},
	},
	"errors": {
		"New":    {msgIdx: 0},
		"Errorf": {msgIdx: 0, printf: true},
		"Wrap":   {msgIdx: 1, wraps: true},
		"Wrapf":  {msgIdx: 1, printf: true, wraps: true},
	},
	"status": {
		"Error":  {msgIdx: 1},
		"Errorf": {msgIdx: 1, printf: true},
	},
}

// visitErrCall recognizes one call expression as an error-construction site
// and records its template for a suffix-anchored dictionary.
func visitErrCall(fset *token.FileSet, file string, call *ast.CallExpr, byKey map[string]*Template, stats *Stats) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return
	}
	ef, ok := errFuncs[pkg.Name][sel.Sel.Name]
	if !ok {
		return
	}
	if len(call.Args) <= ef.msgIdx {
		return
	}

	stats.Calls++
	tmpl, hasLit := stringTemplate(call.Args[ef.msgIdx])
	if !hasLit {
		stats.Dynamic++
		return
	}
	if ef.printf {
		tmpl = normalizeFormat(tmpl)
	}
	if ef.wraps {
		tmpl += ": " + Placeholder // Wrap renders "msg: cause"
	}
	if isZeroLiteral(tmpl) {
		stats.Dynamic++
		return
	}
	record(fset, file, call, byKey, "", pkg.Name, tmpl)
}

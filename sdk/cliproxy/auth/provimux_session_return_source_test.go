package auth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Provimux: row 15 of the design's 15.6, settled by reading this package's source as the compiler does. The rule sits
// in Pick, one line before the binding is looked up; that is enough only if Pick is the one place a named
// conversation's binding in the session cache decides an account. Every read of the cache that can hand an account back
// is listed here with the function it is in. A read upstream adds, or moves, fails this test, and whoever syncs reads
// it again.

// provimuxCacheReads is every place in this package, outside Provimux's own files and tests, that reads a binding out
// of the session cache, by function. Pick honours it; LookupAffinity only observes (it selects nothing); and
// boundAuthForRequest is used only where session-affinity-rebind-on is "never", where the rule is not in effect.
var provimuxCacheReads = map[string][]string{
	"GetAndRefresh":       {"selector.go:Pick"},
	"Get":                 {"selector.go:LookupAffinity", "selector.go:Pick", "selector.go:boundAuthForRequest"},
	"latestSessionAuth":   {"selector.go:Pick"},
	"boundAuthForRequest": {"conductor_execution.go:normalizeSessionAffinityNeverTerminalError", "conductor_home.go:findAllAntigravityCreditsCandidateAuths", "conductor_selection.go:shouldRetryAfterErrorWithAttempted"},
}

func TestProvimuxSessionReturnPickIsWhereBindingsAreHonoured(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || strings.HasPrefix(name, "provimux_") {
			continue
		}
		src, errRead := os.ReadFile(name)
		if errRead != nil {
			t.Fatal(errRead)
		}
		file, errParse := parser.ParseFile(fset, name, src, 0)
		if errParse != nil {
			t.Fatal(errParse)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				method := sel.Sel.Name
				onCache := false
				if inner, okInner := sel.X.(*ast.SelectorExpr); okInner && inner.Sel.Name == "cache" {
					onCache = true
				}
				switch {
				case method == "boundAuthForRequest":
				case onCache && (method == "Get" || method == "GetAndRefresh" || method == "latestSessionAuth"):
				default:
					return true
				}
				where := name + ":" + fn.Name.Name
				got[method] = append(got[method], where)
				if method == "boundAuthForRequest" {
					before := string(src[fset.Position(fn.Body.Pos()).Offset:fset.Position(call.Pos()).Offset])
					if !strings.Contains(before, "rebindOnNever") {
						t.Errorf("%s reads a binding with boundAuthForRequest without testing rebindOnNever first", where)
					}
				}
				return true
			})
		}
	}
	for method, places := range got {
		sort.Strings(places)
		got[method] = dedupe(places)
	}
	for method, want := range provimuxCacheReads {
		if strings.Join(got[method], ",") != strings.Join(want, ",") {
			t.Errorf("%s is read in %v; the rule was placed for %v. Read the new place: if it honours a named conversation's binding, the rule must decide there too", method, got[method], want)
		}
	}
	for method := range got {
		if _, known := provimuxCacheReads[method]; !known {
			t.Errorf("unexpected read %s in %v", method, got[method])
		}
	}
}

func dedupe(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// TestProvimuxSessionReturnTwoLines: the two lines of 15.3 are where the design puts them: the field is the selector's,
// and in Pick the rule is the statement right before the binding is looked up, with Pick's own values.
func TestProvimuxSessionReturnTwoLines(t *testing.T) {
	fset := token.NewFileSet()
	src, err := os.ReadFile("selector.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(fset, "selector.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	text := func(n ast.Node) string {
		return string(src[fset.Position(n.Pos()).Offset:fset.Position(n.End()).Offset])
	}
	foundField, foundCall := false, false
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name.Name != "SessionAffinitySelector" {
					continue
				}
				for _, field := range ts.Type.(*ast.StructType).Fields.List {
					if len(field.Names) == 1 && field.Names[0].Name == "provimux" && text(field.Type) == "provimuxSessionReturn" {
						foundField = true
					}
				}
			}
		case *ast.FuncDecl:
			if d.Name.Name != "Pick" || d.Recv == nil || !strings.Contains(text(d.Recv), "SessionAffinitySelector") {
				continue
			}
			for i, stmt := range d.Body.List {
				ifStmt, ok := stmt.(*ast.IfStmt)
				if !ok || ifStmt.Init == nil || !strings.Contains(text(ifStmt.Init), "s.cache.GetAndRefresh(cacheKey)") {
					continue
				}
				if i > 0 && text(d.Body.List[i-1]) == `s.provimuxReturn(explicitID != "" && !isSubagent && !isFork, cacheKey, fallbackKey, available, now)` {
					foundCall = true
				}
			}
		}
	}
	if !foundField || !foundCall {
		t.Fatalf("selector.go: the field found %v, the call right before the look-up found %v", foundField, foundCall)
	}
}

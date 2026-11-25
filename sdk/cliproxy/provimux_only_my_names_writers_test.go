package cliproxy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// R12 (only-my-names.md 8.2): every way into the registry outside tests and the registry itself is
// one of the writers that apply models.only-my-names. An upstream release that adds a fourth way
// fails here, naming that section, instead of serving held-back names.
func TestProvimuxOnlyMyNamesRegistryWriters(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	writers := map[string]bool{"RegisterClient": true, "RegisterClientIfEpochCurrent": true, "ReplaceClientModels": true, "publishModels": true}
	// file, function, call -> whether the function applies the rule itself (false: a fenced helper
	// whose only caller does).
	type call struct{ file, function, method string }
	var found []call
	applies := map[call]bool{}
	errWalk := filepath.WalkDir(root, func(path string, d fs.DirEntry, errEntry error) error {
		if errEntry != nil {
			return errEntry
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".git" || rel == "internal/registry" || strings.HasPrefix(rel, ".github") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		file, errParse := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if errParse != nil {
			return errParse
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			rule := false
			var calls []call
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				expr, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var name string
				switch fun := expr.Fun.(type) {
				case *ast.SelectorExpr:
					name = fun.Sel.Name
				case *ast.Ident:
					name = fun.Name
				}
				if name == "ProvimuxOnlyMyNames" || name == "provimuxOnlyMyNames" {
					rule = true
				}
				if writers[name] {
					calls = append(calls, call{rel, fn.Name.Name, name})
				}
				return true
			})
			for _, c := range calls {
				found = append(found, c)
				applies[c] = rule
			}
		}
		return nil
	})
	if errWalk != nil {
		t.Fatal(errWalk)
	}
	want := []call{
		{"examples/custom-provider/main.go", "main", "RegisterClient"},                                // an SDK example, not part of the gateway
		{"internal/pluginhost/adapters_executors.go", "commitExecutorState", "RegisterClient"},        // a plugin executor's own models
		{"sdk/cliproxy/antigravity_models.go", "applyAntigravityModelHints", "ReplaceClientModels"},   // the Antigravity catalog
		{"sdk/cliproxy/model_registry.go", "publishModels", "RegisterClientIfEpochCurrent"},           // the fence, below...
		{"sdk/cliproxy/service_executors.go", "registerResolvedModelsForAuthFenced", "publishModels"}, // ...its only caller
	}
	key := func(c call) string { return c.file + " " + c.function + " " + c.method }
	slices.SortFunc(found, func(a, b call) int { return strings.Compare(key(a), key(b)) })
	slices.SortFunc(want, func(a, b call) int { return strings.Compare(key(a), key(b)) })
	if !slices.Equal(found, want) {
		var lines []string
		for _, c := range found {
			lines = append(lines, key(c))
		}
		t.Fatalf("the ways into the registry changed (only-my-names.md 8.2: each must apply ProvimuxOnlyMyNames):\n%s", strings.Join(lines, "\n"))
	}
	for _, c := range want {
		if c.file == "examples/custom-provider/main.go" || c.function == "publishModels" {
			continue
		}
		if !applies[c] {
			t.Errorf("%s does not apply the rule before %s", key(c), c.method)
		}
	}
}

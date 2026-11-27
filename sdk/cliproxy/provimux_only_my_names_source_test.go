package cliproxy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Source tests of models.only-my-names (only-my-names.md 16.7, K3, K4 and K5). They read the tree, so an
// upstream release that adds a reader of the registry, a read method, or moves a gate line fails the sync
// check here, naming the section, instead of serving a name that is not the owner's.

func namesRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// namesGoFiles calls visit for every non-test Go file of the gateway (not .git, .github or examples).
func namesGoFiles(t *testing.T, root string, visit func(rel string, file *ast.File)) {
	t.Helper()
	errWalk := filepath.WalkDir(root, func(path string, d fs.DirEntry, errEntry error) error {
		if errEntry != nil {
			return errEntry
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".git" || rel == "examples" || strings.HasPrefix(rel, ".github") {
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
		visit(rel, file)
		return nil
	})
	if errWalk != nil {
		t.Fatal(errWalk)
	}
}

// namesRegistryReaders classifies every use of the registry's eleven read methods outside tests and the
// registry's own file, called or taken as a method value (file, function, method -> class). list: it reads a gated loop (G1). after the door: it
// runs for a request providersForExecution let through, or decides a capability and executes nothing. no
// client name: management, state and cooldown reconciliation, the scheduler's copy, Muse Code's relay,
// verification, the translators' metadata lookups.
var namesRegistryReaders = map[string]string{
	"internal/api/handlers/management/auth_files.go GetAuthFileModels GetModelsForClient":                                                                               "no client name",
	"internal/api/handlers/management/model_definitions.go pluginModelDefinitionsByChannel GetAvailableModelsByProvider":                                                "no client name",
	"internal/api/handlers/management/provimux_key_model_lists.go provimuxKeyModelListRows GetModelsForClient":                                                          "no client name",
	"internal/api/handlers/management/provimux_live_lists.go ProvimuxLiveLists GetModelsForClient":                                                                      "no client name",
	"internal/api/handlers/management/provimux_model_lists.go ProvimuxModelLists GetModelsForClient":                                                                    "no client name",
	"internal/api/handlers/management/provimux_model_routes.go provimuxBuildModelRoutes GetModelInfo":                                                                   "no client name",
	"internal/api/handlers/management/provimux_model_routes.go provimuxBuildModelRoutes GetModelsForClient":                                                             "no client name", // gated by ProvimuxServes in the layer's file
	"internal/api/server_routes.go handleGrokModels GetAvailableModelInfos":                                                                                             "list",
	"internal/client/codex/optimize-multi-agent-v2/optimize_multi_agent_v2.go codexSpawnAgentModelsAndMarkdownForRequest GetAvailableModels":                            "list",
	"internal/client/musecode/models.go ModelInputsFromGlobalRegistry GetAvailableModelInfos":                                                                           "list",
	"internal/client/musecode/models.go ModelInputsFromGlobalRegistry GetModelInfo":                                                                                     "list", // for the ids of the gated list
	"internal/client/musecode/models.go ModelInputsFromGlobalRegistry GetModelProviders":                                                                                "list", // for the ids of the gated list
	"internal/client/codex/models/provimux_devin_name.go provimuxServedBeyondDevin GetModelProviders":                                                                   "list", // a method value: providers of the ids of the gated list (Codex client catalog names)
	"sdk/api/handlers/openai/codex_client_models.go codexClientModelsResponse GetModelProviders":                                                                        "list", // a method value: metadata of the ids of the gated list
	"internal/client/musecode/relay.go metaCandidateModels GetModelsForClient":                                                                                          "no client name",
	"internal/pluginhost/adapters_executors.go modelHasNativeExecutor GetModelProviders":                                                                                "after the door",
	"internal/pluginhost/auth_callbacks.go buildHostAuthModels GetModelsForClient":                                                                                      "no client name",
	"internal/registry/model_definitions.go AntigravityWebSearchModelFor GetAvailableModelsByProvider":                                                                  "no client name",
	"internal/runtime/executor/claude_executor_cloaking.go ensureModelMaxTokensDoc GetModelInfo":                                                                        "after the door",
	"internal/runtime/executor/claude_executor_cloaking.go ensureModelMaxTokensDoc GetModelProviders":                                                                   "after the door",
	"internal/translator/antigravity/openai/chat-completions/antigravity_openai_request.go antigravitySupportsNativeOpenAIWebSearch GetAvailableModelsByProvider":       "no client name",
	"internal/translator/antigravity/openai/responses/antigravity_openai-responses_request.go antigravitySupportsNativeResponsesWebSearch GetAvailableModelsByProvider": "no client name",
	"internal/util/provider.go GetProviderName GetModelProviders":                                                                                                       "after the door", // RegisteredModelProviders, behind providersForExecution and the websocket's pinned-login match
	"internal/util/provider.go ResolveAutoModel GetFirstAvailableModel":                                                                                                 "list",
	"sdk/api/handlers/claude/code_handlers.go Models GetAvailableModels":                                                                                                "list",
	"sdk/api/handlers/gemini/gemini_handlers.go Models GetAvailableModels":                                                                                              "list",
	"sdk/api/handlers/openai/openai_handlers.go Models GetAvailableModels":                                                                                              "list",
	"sdk/api/handlers/openai/openai_images_handlers.go registeredXAIImagesModel GetModelProviders":                                                                      "after the door", // decides the image route's model; executes through the door
	"sdk/cliproxy/auth/provimux_session_route.go provimuxListed GetModelsForClient":                                                                                     "after the door", // session-route: the name a show asks about passed the door first (internal/api/provimux_session_route.go)
	"sdk/api/handlers/openai/openai_models_accounts.go modelAccounts GetModelsForClient":                                                                                "list",           // annotates the ids of the gated list
	"sdk/api/handlers/openai/openai_responses_handlers.go Models GetAvailableModels":                                                                                    "list",
	"sdk/api/handlers/openai/openai_responses_websocket_session.go responsesWebsocketAuthMatchesModel ClientSupportsModel":                                              "after the door",
	"internal/registry/provimux_only_my_names.go ProvimuxFirstServedUnder GetModelCount":                                                                                "list", // auto under the request's rule value: candidates the rule serves
	"sdk/cliproxy/auth/conductor_cooldown.go markResult GetModelsAndEpochForClient":                                                                                     "no client name",
	"sdk/cliproxy/auth/conductor_cooldown.go ResetQuota GetModelsAndEpochForClient":                                                                                     "no client name",
	"sdk/cliproxy/auth/conductor_cooldown.go modelsForRegisteredAuth GetModelsForClient":                                                                                "no client name",
	"sdk/cliproxy/auth/conductor_cooldown.go registryModelsForAuthAndModel GetModelsForClient":                                                                          "no client name",
	"sdk/cliproxy/auth/conductor_execution.go resolveVerifyInferenceRouteModel GetModelsForClient":                                                                      "no client name",
	"sdk/cliproxy/auth/conductor_models.go publishedModelRouteForAuth LookupClientModelRoute":                                                                           "after the door",
	"sdk/cliproxy/auth/conductor_refresh.go refreshAuthForRequestAtVersion GetModelsAndEpochForClient":                                                                  "after the door",
	"sdk/cliproxy/auth/conductor_selection.go ReconcileRegistryModelStates GetModelsAndEpochForClient":                                                                  "no client name",
	"sdk/cliproxy/auth/conductor_selection.go authSupportsRouteModel ClientSupportsModel":                                                                               "after the door",
	"sdk/cliproxy/auth/conductor_selection.go migrateAuthAliasModelStatesLocked GetModelsForClient":                                                                     "no client name",
	"sdk/cliproxy/auth/conductor_selection.go publishOAuthModelAliasAndReconcileStates GetModelsAndEpochForClient":                                                      "no client name",
	"sdk/cliproxy/auth/conductor_selection.go resolveMarkResultModelKeyLocked GetModelsForClient":                                                                       "after the door",
	"sdk/cliproxy/auth/conductor_selection.go routeAwareSelectionRequired GetModelsForClient":                                                                           "after the door",
	"sdk/cliproxy/auth/execution_auth_refresh.go withExecutionAuthRefresh GetModelsAndEpochForClient":                                                                   "after the door",
	"sdk/cliproxy/auth/native_tool_search_capability.go nativeToolSearchServingAuths ClientSupportsModel":                                                               "after the door",
	"sdk/cliproxy/auth/scheduler.go supportedModelSetForAuth GetModelsForClient":                                                                                        "no client name",
	"sdk/cliproxy/provimux_key_list_status.go keyModelListModels GetModelsForClient":                                                                                    "no client name",
	"sdk/cliproxy/provimux_model_list_status.go ModelListLogin GetModelsForClient":                                                                                      "no client name",
}

// namesRegistryMethods are the exported methods of ModelRegistry the test knows. A new one fails K3 until it
// is classified: a read of names is gated or listed above.
var namesRegistryMethods = map[string]string{
	"GetAvailableModels": "read: gated (G1)", "GetAvailableModelInfos": "read: gated (G1)", "GetFirstAvailableModel": "read: of the gated list",
	"GetAvailableModelsByProvider": "read: not gated, callers above", "GetModelCount": "read: callers above", "GetModelProviders": "read: callers above",
	"GetModelInfo": "read: callers above", "GetModelsAndEpochForClient": "read: callers above", "GetModelsForClient": "read: callers above",
	"ClientSupportsModel": "read: callers above", "LookupClientModelRoute": "read: callers above",
	"ClientModelRoute": "read: one account's route metadata, after the door", "GetResponsesWebSearchCapability": "read: a capability",
	"IsModelQuotaExceededForClient": "read: state", "IsModelSuspendedForClient": "read: state", "GetGeneration": "read: a counter",
	"RegistrationEpoch": "read: a counter", "ClientRegistrationEpoch": "read: a counter",
	"RegisterClient": "write", "RegisterClientIfEpochCurrent": "write", "ReplaceClientModels": "write", "UnregisterClient": "write",
	"UnregisterClientIfEpochCurrent": "write", "ApplyClientModelCapabilities": "write", "ApplyClientModelProjections": "write: state",
	"SetModelQuotaExceeded": "write: state", "ClearModelQuotaExceeded": "write: state", "SuspendClientModel": "write: state",
	"ResumeClientModel": "write: state", "CleanupExpiredQuotas": "write: state", "SetHook": "setup",
}

// K3: every call of the eleven read methods outside tests and the registry's own file is classified, and so is
// every exported method of ModelRegistry.
func TestProvimuxOnlyMyNamesRegistryReaders(t *testing.T) {
	root := namesRepoRoot(t)
	eleven := map[string]bool{"GetAvailableModels": true, "GetAvailableModelInfos": true, "GetAvailableModelsByProvider": true, "GetModelCount": true,
		"GetModelProviders": true, "GetModelInfo": true, "GetFirstAvailableModel": true, "GetModelsAndEpochForClient": true, "GetModelsForClient": true,
		"ClientSupportsModel": true, "LookupClientModelRoute": true}
	found := map[string]bool{}
	methods := map[string]bool{}
	namesGoFiles(t, root, func(rel string, file *ast.File) {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if strings.HasPrefix(rel, "internal/registry/") {
				if fn.Recv != nil && len(fn.Recv.List) == 1 && fn.Name.IsExported() {
					if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
						if ident, ok := star.X.(*ast.Ident); ok && ident.Name == "ModelRegistry" {
							methods[fn.Name.Name] = true
						}
					}
				}
			}
			if rel == "internal/registry/model_registry.go" {
				continue
			}
			// Every selector of a read method, called or taken as a method value (a reader stored as a function
			// and called later is a reader too).
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && eleven[sel.Sel.Name] {
					found[rel+" "+fn.Name.Name+" "+sel.Sel.Name] = true
				}
				return true
			})
		}
	})
	var missing, gone []string
	for call := range found {
		if _, ok := namesRegistryReaders[call]; !ok {
			missing = append(missing, call)
		}
	}
	for call := range namesRegistryReaders {
		if !found[call] {
			gone = append(gone, call)
		}
	}
	sort.Strings(missing)
	sort.Strings(gone)
	if len(missing) > 0 || len(gone) > 0 {
		t.Fatalf("the registry's readers changed (only-my-names.md 16.7, K3): classify each new call as list (a gated loop), after the door, or no client name, and gate it if it serves a client's model name.\nnew:\n%s\ngone:\n%s",
			strings.Join(missing, "\n"), strings.Join(gone, "\n"))
	}
	var unknown []string
	for method := range methods {
		if _, ok := namesRegistryMethods[method]; !ok {
			unknown = append(unknown, method)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		t.Fatalf("ModelRegistry has methods this test does not know (only-my-names.md 16.7, K3): %v", unknown)
	}
}

// namesFunc returns the declaration of function name in rel.
func namesFunc(t *testing.T, root, rel, name string) *ast.FuncDecl {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, rel), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name && fn.Body != nil {
			return fn
		}
	}
	t.Fatalf("%s: no function %s (only-my-names.md 16.4)", rel, name)
	return nil
}

func namesCalls(node ast.Node, name string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				found = found || fun.Sel.Name == name
			case *ast.Ident:
				found = found || fun.Name == name
			}
		}
		return !found
	})
	return found
}

// K4: the gate lines are where 16.4 puts them.
func TestProvimuxOnlyMyNamesGateLines(t *testing.T) {
	root := namesRepoRoot(t)
	for _, place := range []struct{ file, function, call string }{
		{"internal/registry/model_registry.go", "buildAvailableModelsLocked", "provimuxHeld"},
		{"internal/registry/model_registry.go", "GetAvailableModelInfos", "provimuxHeld"},
		{"sdk/api/handlers/handlers_routing.go", "applyModelRouter", "provimuxNoRouter"},
		{"internal/api/server_routes.go", "codexAlphaSearch", "provimuxRefused"},
	} {
		if !namesCalls(namesFunc(t, root, place.file, place.function), place.call) {
			t.Errorf("%s %s does not call %s (only-my-names.md 16.4)", place.file, place.function, place.call)
		}
	}
	door := namesFunc(t, root, "sdk/api/handlers/handlers_routing.go", "providersForExecution")
	if len(door.Body.List) == 0 || !namesCalls(door.Body.List[0], "provimuxDoor") {
		t.Error("provimuxDoor is not the first statement of providersForExecution (only-my-names.md 16.4)")
	}
	// applyModelRouter: the check comes before the router host is read.
	router := namesFunc(t, root, "sdk/api/handlers/handlers_routing.go", "applyModelRouter")
	if len(router.Body.List) < 2 || !namesCalls(router.Body.List[1], "provimuxNoRouter") {
		t.Error("provimuxNoRouter is not the statement after `var decision` in applyModelRouter (only-my-names.md 16.4)")
	}
}

// K5 in the source: the ways into the registry never consult the rule.
func TestProvimuxOnlyMyNamesWritersDoNotReadTheRule(t *testing.T) {
	root := namesRepoRoot(t)
	words := []string{"OnlyMyNames", "only-my-names", "ProvimuxNames", "ProvimuxServes", "ProvimuxRefuse", "provimuxSetNamesRule", "provimuxHeld"}
	for _, rel := range []string{"sdk/cliproxy/service_executors.go", "sdk/cliproxy/antigravity_models.go", "sdk/cliproxy/model_registry.go",
		"internal/pluginhost/adapters_executors.go", "internal/pluginhost/model_router.go", "internal/watcher/config_reload.go"} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		for _, word := range words {
			if strings.Contains(string(data), word) {
				t.Errorf("%s mentions %s: registration must not consult the rule (only-my-names.md 16.2)", rel, word)
			}
		}
	}
	// Registration in the registry itself: registerClientLocked and its callers read no rule.
	for _, name := range []string{"registerClientLocked", "RegisterClient", "RegisterClientIfEpochCurrent", "ReplaceClientModels"} {
		fn := namesFunc(t, root, "internal/registry/model_registry.go", name)
		for _, word := range []string{"provimuxHeld", "provimuxRule", "provimuxFact"} {
			if namesCalls(fn, word) {
				t.Errorf("model_registry.go %s calls %s", name, word)
			}
		}
	}
}

// Section 18, by structure: the door's judgement is a function of one rule value and the name. Every function that
// judges a name (the door, the router check, alpha search, model-routes' filter, the list gate) calls nothing but
// the rule package, a method of the rule value it was given, and the standard library; it names no registry and
// reads no field of the registry it may receive: a judge that is a method (the list gate's receiver is the registry)
// does not use its receiver at all, neither a field nor a method. With the rule package importing no registry
// (internal/provimux/onlymynames, TestRuleImportsNoRegistry), a judgement cannot read a registration: a change that
// makes it do so fails here.
func TestProvimuxOnlyMyNamesJudgementReadsNoRegistry(t *testing.T) {
	root := namesRepoRoot(t)
	allowedPackages := map[string]bool{"onlymynames": true, "strings": true, "errors": true, "ojson": true, "fmt": true}
	for _, judge := range []struct {
		file, function string
		values         []string // identifiers whose methods may be called: the rule value, the response writer
	}{
		{"sdk/api/handlers/provimux_only_my_names.go", "provimuxDoorJudge", []string{"rule"}},
		{"sdk/api/handlers/provimux_only_my_names.go", "provimuxNoRouter", []string{"rule"}},
		{"internal/api/server_provimux.go", "provimuxRefused", []string{"rule", "c"}},
		{"internal/api/handlers/management/provimux_model_routes.go", "provimuxGated", []string{"rule"}},
		{"internal/registry/provimux_only_my_names.go", "provimuxHeld", []string{"rule"}},
	} {
		fn := namesFunc(t, root, judge.file, judge.function)
		values := map[string]bool{}
		for _, value := range judge.values {
			values[value] = true
		}
		receivers := map[string]bool{}
		if fn.Recv != nil {
			for _, field := range fn.Recv.List {
				for _, name := range field.Names {
					if name.Name != "_" {
						receivers[name.Name] = true
					}
				}
			}
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.Ident:
				if node.Name == "registry" || node.Name == "GetGlobalRegistry" || node.Name == "reg" {
					t.Errorf("%s %s names %s: a judgement reads no registry (only-my-names.md 18)", judge.file, judge.function, node.Name)
				}
				if receivers[node.Name] {
					t.Errorf("%s %s uses its receiver %s: a judgement reads no field or method of what it is called on (only-my-names.md 18)", judge.file, judge.function, node.Name)
				}
			case *ast.CallExpr:
				switch fun := node.Fun.(type) {
				case *ast.SelectorExpr:
					x, ok := fun.X.(*ast.Ident)
					if !ok || (!allowedPackages[x.Name] && !values[x.Name]) {
						t.Errorf("%s %s calls %s: only the rule package, the rule value and the standard library (only-my-names.md 18)", judge.file, judge.function, types(fun))
					}
				case *ast.Ident:
					switch fun.Name {
					case "len", "string", "append":
					default:
						t.Errorf("%s %s calls %s", judge.file, judge.function, fun.Name)
					}
				}
			}
			return true
		})
	}
	// The door loads the rule once (onlymynames.Current) and hands that value to every judgement.
	door := namesFunc(t, root, "sdk/api/handlers/provimux_only_my_names.go", "provimuxDoor")
	loads := 0
	ast.Inspect(door.Body, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Current" {
			loads++
		}
		return true
	})
	if loads != 1 {
		t.Errorf("provimuxDoor loads the rule %d times, want once (only-my-names.md 18)", loads)
	}
	// One value from the router check to the door (only-my-names.md 18.2): the door takes the decision's value
	// first and loads only when it is empty; the router check stores the value it judges under; and
	// applyModelRouter never replaces the decision it declared, so every return carries that value.
	if first, ok := door.Body.List[0].(*ast.AssignStmt); !ok || len(first.Rhs) != 1 || namesExpr(first.Rhs[0]) != "decision.provimuxRule" {
		t.Error("provimuxDoor does not start from the decision's rule value")
	}
	noRouter := namesFunc(t, root, "sdk/api/handlers/provimux_only_my_names.go", "provimuxNoRouter")
	stores := false
	ast.Inspect(noRouter.Body, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && namesExpr(as.Lhs[0]) == "decision.provimuxRule" && namesExpr(as.Rhs[0]) == "rule" {
			stores = true
		}
		return true
	})
	if !stores {
		t.Error("provimuxNoRouter does not store its rule value on the decision")
	}
	router := namesFunc(t, root, "sdk/api/handlers/handlers_routing.go", "applyModelRouter")
	ast.Inspect(router.Body, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok {
			for _, lhs := range as.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name == "decision" {
					t.Error("applyModelRouter replaces its decision: the rule value would not reach the door")
				}
			}
		}
		if ret, ok := n.(*ast.ReturnStmt); ok && (len(ret.Results) != 1 || namesExpr(ret.Results[0]) != "decision") {
			t.Error("applyModelRouter returns something other than its decision")
		}
		return true
	})
}

// namesExpr spells a selector chain ("decision.provimuxRule") or an identifier.
func namesExpr(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return namesExpr(v.X) + "." + v.Sel.Name
	case *ast.StarExpr:
		return namesExpr(v.X)
	}
	return ""
}

func types(sel *ast.SelectorExpr) string {
	if x, ok := sel.X.(*ast.Ident); ok {
		return x.Name + "." + sel.Sel.Name
	}
	return "?." + sel.Sel.Name
}

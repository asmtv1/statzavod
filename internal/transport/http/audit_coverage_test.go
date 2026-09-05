package httpserver

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/statzavod/statzavod/internal/config"
)

func TestAuthenticatedEndpointAuditCoverageTable(t *testing.T) {
	anonymous := map[string]struct{}{
		"GET /healthz": {}, "GET /readyz": {}, "GET /metrics": {},
		"GET /api/v1/oauth/{platform}/callback":            {},
		"POST /api/v1/oauth/instagram/deauthorize":         {},
		"POST /api/v1/oauth/instagram/data-deletion":       {},
		"GET /api/v1/oauth/instagram/data-deletion/status": {},
		"GET /api/v1/deletions/{id}":                       {},
		"GET /api/v1/media/delivery/{token}":               {},
		"POST /api/v1/auth/login":                          {}, "POST /api/v1/auth/logout": {},
		"POST /api/v1/auth/accept-invitation": {},
	}
	seen := make(map[string]struct{})
	routes, ok := New(nil, config.Config{}).Router().(chi.Routes)
	if !ok {
		t.Fatal("server router does not expose chi routes")
	}
	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == http.MethodOptions || method == http.MethodHead {
			return nil
		}
		key := method + " " + route
		seen[key] = struct{}{}
		if _, excluded := anonymous[key]; excluded {
			if _, audited := auditEndpointCoverage[key]; audited {
				t.Errorf("anonymous/excluded endpoint %s must not be centrally audited", key)
			}
			return nil
		}
		action, ok := auditEndpointCoverage[key]
		if !ok {
			t.Errorf("authenticated business endpoint %s is missing audit coverage", key)
			return nil
		}
		if !strings.HasPrefix(action, "HTTP_") {
			t.Errorf("endpoint %s has invalid audit action %q", key, action)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for endpoint := range auditEndpointCoverage {
		if _, exists := seen[endpoint]; !exists {
			t.Errorf("audit coverage contains stale endpoint %s", endpoint)
		}
	}
	for _, excluded := range []string{"POST /api/v1/auth/login", "POST /api/v1/auth/logout", "GET /healthz", "GET /readyz", "GET /metrics"} {
		if _, exists := auditEndpointCoverage[excluded]; exists {
			t.Errorf("excluded endpoint %s unexpectedly audited", excluded)
		}
	}
}

func TestAuthenticatedMutationRoutesRequireAtomicAuditRegistration(t *testing.T) {
	anonymous := map[string]struct{}{
		"GET /healthz": {}, "GET /readyz": {}, "GET /metrics": {},
		"GET /api/v1/oauth/{platform}/callback":            {},
		"POST /api/v1/oauth/instagram/deauthorize":         {},
		"POST /api/v1/oauth/instagram/data-deletion":       {},
		"GET /api/v1/oauth/instagram/data-deletion/status": {},
		"GET /api/v1/deletions/{id}":                       {},
		"GET /api/v1/media/delivery/{token}":               {},
		"POST /api/v1/auth/login":                          {},
		"POST /api/v1/auth/logout":                         {},
		"POST /api/v1/auth/accept-invitation":              {},
	}
	// POST is also used for sensitive reads. Those endpoints are buffered by
	// auditAuthenticatedBusiness and fail closed, but have no business mutation
	// to share a transaction with.
	nonMutatingPOST := map[string]struct{}{
		"POST /api/v1/company-vk-accounts/{id}/password/reveal":         {},
		"POST /api/v1/creators/{id}/credentials/{credentialID}/reveal":  {},
		"POST /api/v1/creators/{id}/history/changes/{changeID}/reveal":  {},
		"POST /api/v1/creator-portal/credentials/{credentialID}/reveal": {},
	}
	seen := make(map[string]struct{})
	routes, ok := New(nil, config.Config{}).Router().(chi.Routes)
	if !ok {
		t.Fatal("server router does not expose chi routes")
	}
	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == http.MethodOptions || method == http.MethodHead {
			return nil
		}
		key := method + " " + route
		seen[key] = struct{}{}
		if _, excluded := anonymous[key]; excluded {
			return nil
		}
		mutation := method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete || method == http.MethodPost
		if _, readOnly := nonMutatingPOST[key]; readOnly {
			mutation = false
		}
		_, registered := auditAtomicMutationCoverage[key]
		if mutation != registered {
			t.Errorf("route %s mutation=%t atomic-audit-registered=%t", key, mutation, registered)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for endpoint := range auditAtomicMutationCoverage {
		if _, exists := seen[endpoint]; !exists {
			t.Errorf("atomic audit coverage contains stale endpoint %s", endpoint)
		}
		if _, audited := auditEndpointCoverage[endpoint]; !audited {
			t.Errorf("atomic audit endpoint %s lacks general audit coverage", endpoint)
		}
	}
}

func TestSystemMutationAuditCoverage(t *testing.T) {
	want := []string{
		auditSystemOAuthRefresh, auditSystemOAuthReauthRequired,
		auditSystemSyncStarted, auditSystemSyncSuccess, auditSystemSyncFailed,
		auditSystemPurgeCompany, auditSystemPurgeWorkspace,
		auditSystemContentPublishAttempt, auditSystemContentPublishPending,
		auditSystemContentPublishSucceeded, auditSystemContentPublishFailed,
		auditSystemContentPublishCancelled, auditSystemContentPublishReauthRequired,
		auditSystemMediaReady, auditSystemMediaRejected,
		auditSystemMediaUploadExpired, auditSystemMediaDeletePending, auditSystemMediaDeleted,
	}
	if len(auditSystemMutationCoverage) != len(want) {
		t.Fatalf("system audit coverage size=%d want=%d", len(auditSystemMutationCoverage), len(want))
	}
	for _, action := range want {
		if _, ok := auditSystemMutationCoverage[action]; !ok {
			t.Errorf("system mutation action %s is not registered", action)
		}
	}
}

// TestSystemMutationCoverageUsesCanonicalWriter is deliberately source based:
// the registration table alone is not evidence that a background mutation
// calls the canonical writer. It follows both direct Action values and local
// action variables used for outcome branches inside a writeAudit call.
func TestSystemMutationCoverageUsesCanonicalWriter(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not resolve test source directory")
	}
	dir := filepath.Dir(currentFile)
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}

	constants := make(map[string]string)
	parsed := make([]*ast.File, 0, len(files))
	for _, filename := range files {
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", filepath.Base(filename), parseErr)
		}
		parsed = append(parsed, file)
		ast.Inspect(file, func(node ast.Node) bool {
			spec, isValueSpec := node.(*ast.ValueSpec)
			if !isValueSpec {
				return true
			}
			for i, name := range spec.Names {
				if i < len(spec.Values) {
					if value, resolved := auditStaticString(spec.Values[i], constants); resolved {
						constants[name.Name] = value
					}
				}
			}
			return true
		})
	}

	written := make(map[string]struct{})
	// A small transactional wrapper may accept the action as a parameter and
	// call writeAudit itself. Record that parameter position so callers remain
	// part of the same static evidence chain.
	actionSinkParameter := make(map[string]int)
	for _, file := range parsed {
		for _, declaration := range file.Decls {
			function, isFunction := declaration.(*ast.FuncDecl)
			if !isFunction || function.Body == nil {
				continue
			}
			parameters := functionParameterNames(function)
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, isCall := node.(*ast.CallExpr)
				if !isCall {
					return true
				}
				selector, isSelector := call.Fun.(*ast.SelectorExpr)
				if !isSelector || selector.Sel.Name != "writeAudit" {
					return true
				}
				for _, argument := range call.Args {
					literal, isLiteral := argument.(*ast.CompositeLit)
					if !isLiteral {
						continue
					}
					for _, element := range literal.Elts {
						field, isField := element.(*ast.KeyValueExpr)
						if !isField {
							continue
						}
						key, isKey := fieldKey(field)
						name, isName := field.Value.(*ast.Ident)
						if !isKey || key != "Action" || !isName {
							continue
						}
						for index, parameter := range parameters {
							if parameter == name.Name {
								actionSinkParameter[function.Name.Name] = index
							}
						}
					}
				}
				return true
			})
		}
	}
	for _, file := range parsed {
		for _, declaration := range file.Decls {
			function, isFunction := declaration.(*ast.FuncDecl)
			if !isFunction || function.Body == nil {
				continue
			}
			locals := make(map[string]map[string]struct{})
			ast.Inspect(function.Body, func(node ast.Node) bool {
				switch typed := node.(type) {
				case *ast.AssignStmt:
					for i, lhs := range typed.Lhs {
						name, isName := lhs.(*ast.Ident)
						if !isName || i >= len(typed.Rhs) {
							continue
						}
						if value, resolved := auditStaticString(typed.Rhs[i], constants); resolved {
							if locals[name.Name] == nil {
								locals[name.Name] = make(map[string]struct{})
							}
							locals[name.Name][value] = struct{}{}
						}
					}
				case *ast.CallExpr:
					selector, isSelector := typed.Fun.(*ast.SelectorExpr)
					if !isSelector {
						return true
					}
					if parameter, isSink := actionSinkParameter[selector.Sel.Name]; isSink && parameter < len(typed.Args) {
						if value, resolved := auditStaticString(typed.Args[parameter], constants); resolved {
							written[value] = struct{}{}
						} else if name, isName := typed.Args[parameter].(*ast.Ident); isName {
							for value := range locals[name.Name] {
								written[value] = struct{}{}
							}
						}
					}
					if selector.Sel.Name != "writeAudit" {
						return true
					}
					for _, argument := range typed.Args {
						literal, isLiteral := argument.(*ast.CompositeLit)
						if !isLiteral {
							continue
						}
						recordType, isRecord := literal.Type.(*ast.Ident)
						if !isRecord || recordType.Name != "auditRecord" {
							continue
						}
						for _, element := range literal.Elts {
							field, isField := element.(*ast.KeyValueExpr)
							key, isKey := fieldKey(field)
							if !isField || !isKey || key != "Action" {
								continue
							}
							if value, resolved := auditStaticString(field.Value, constants); resolved {
								written[value] = struct{}{}
							} else if name, isName := field.Value.(*ast.Ident); isName {
								for value := range locals[name.Name] {
									written[value] = struct{}{}
								}
							}
						}
					}
				}
				return true
			})
		}
	}
	for action := range auditSystemMutationCoverage {
		if _, ok = written[action]; !ok {
			t.Errorf("registered system mutation %s has no canonical writeAudit call", action)
		}
	}
	for action := range written {
		if strings.HasPrefix(action, "SYSTEM_") {
			if _, ok = auditSystemMutationCoverage[action]; !ok {
				t.Errorf("canonical writeAudit action %s is missing system mutation registration", action)
			}
		}
	}
}

func functionParameterNames(function *ast.FuncDecl) []string {
	result := make([]string, 0)
	if function.Type.Params == nil {
		return result
	}
	for _, field := range function.Type.Params.List {
		for _, name := range field.Names {
			result = append(result, name.Name)
		}
	}
	return result
}

func auditStaticString(expression ast.Expr, constants map[string]string) (string, bool) {
	switch typed := expression.(type) {
	case *ast.BasicLit:
		if typed.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(typed.Value)
		return value, err == nil
	case *ast.Ident:
		value, ok := constants[typed.Name]
		return value, ok
	default:
		return "", false
	}
}

func fieldKey(field *ast.KeyValueExpr) (string, bool) {
	if field == nil {
		return "", false
	}
	name, ok := field.Key.(*ast.Ident)
	if !ok {
		return "", false
	}
	return name.Name, true
}

func TestCanonicalAuditWriterIsTheOnlyProductionInsert(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not resolve test source directory")
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(currentFile), "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	rawInsert := regexp.MustCompile(`(?is)insert\s+into\s+audit_logs`)
	for _, filename := range files {
		base := filepath.Base(filename)
		if base == "audit.go" || strings.HasSuffix(base, "_test.go") {
			continue
		}
		contents, readErr := os.ReadFile(filename)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if rawInsert.Match(contents) {
			t.Errorf("%s bypasses the canonical audit writer", base)
		}
	}
}

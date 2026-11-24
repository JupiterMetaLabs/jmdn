package Block

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProposalIngress_UsesSharedNonceStamp pins D-78: every proposal ingress
// stamps AccountNonces through stampProposalAccountNonces, and no other file in
// this package calls the enrichment or prediction primitives directly. The gRPC
// handler kept the pre-prediction EnrichBlockAccountNonces call when the HTTP
// handler was converted; this test fails on that code.
func TestProposalIngress_UsesSharedNonceStamp(t *testing.T) {
	fset := token.NewFileSet()
	ingress := map[string]string{ // handler func → file
		"processZKBlock": "Server.go",
		"ProcessBlock":   "grpc_server.go",
	}
	calls := map[string]int{}
	forbidden := map[string]bool{
		"EnrichBlockAccountNonces":              true,
		"EnrichBlockAccountNoncesWithPredicted": true,
		"PredictContractCreatedAccounts":        true,
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				ce, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var name string
				switch fe := ce.Fun.(type) {
				case *ast.Ident:
					name = fe.Name
				case *ast.SelectorExpr:
					name = fe.Sel.Name
				}
				if name == "stampProposalAccountNonces" {
					calls[f+":"+fn.Name.Name]++
				}
				if forbidden[name] && f != "proposal_nonces.go" {
					t.Errorf("%s: %s calls %s directly — use stampProposalAccountNonces so every ingress stamps identically", fset.Position(ce.Pos()), fn.Name.Name, name)
				}
				return true
			})
		}
	}
	for fn, f := range ingress {
		if calls[f+":"+fn] != 1 {
			t.Errorf("%s in %s must call stampProposalAccountNonces exactly once, calls=%d", fn, f, calls[f+":"+fn])
		}
	}
}

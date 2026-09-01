// Package lostrelease defines an Analyzer that reports velocity resources
// acquired but not released on every path.
//
// # Analyzer lostrelease
//
// lostrelease: check that acquired velocity handles are released
//
// An advanced borrow (ownership.Owner.Borrow, BorrowMut, and the same on
// Shared and Frozen), a lease (ownership.NewLease), or a pool checkout
// (pool.Pool.Get) must be released exactly once. A leaked borrow blocks its
// cell until it is released, and production builds have no runtime net for
// it, so the check belongs here, before the code runs.
//
// The analyzer reports a handle assigned to the blank identifier, and a
// handle for which some control-flow path from the acquisition to a return
// never uses the variable. Almost any mention counts as a use — Release,
// Close, Discard, Move, a defer, passing it to another function, sending or
// returning it — since responsibility may have moved with it; the check is
// conservative. Only calls that consume the resource without discharging it
// (Project, Update, Value, Held, State) and `_ = handle` do not count. A
// path taken because the acquisition's own error result is non-nil is not
// reported, since no handle exists on it.
package lostrelease

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/ctrlflow"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
	"golang.org/x/tools/go/cfg"
	"golang.org/x/tools/go/types/typeutil"
)

var Analyzer = &analysis.Analyzer{
	Name:     "lostrelease",
	Doc:      "check that acquired velocity handles are released on all paths",
	URL:      "https://pkg.go.dev/github.com/apsis-io/velocity/analysis/lostrelease",
	Run:      run,
	Requires: []*analysis.Analyzer{inspect.Analyzer, ctrlflow.Analyzer},
}

const (
	ownershipPath = "github.com/apsis-io/velocity/ownership"
	poolPath      = "github.com/apsis-io/velocity/pool"
)

// acquirers maps package path to the receiver type (empty for a function)
// and method or function names that hand out a handle as their first result.
var acquirers = map[string]map[string][]string{
	ownershipPath: {
		"Owner":  {"Borrow", "BorrowMut"},
		"Shared": {"Borrow", "BorrowMut"},
		"Frozen": {"Borrow"},
		"":       {"NewLease"},
	},
	poolPath: {
		"Pool": {"Get"},
	},
}

func run(pass *analysis.Pass) (any, error) {
	if !imports(pass.Pkg, ownershipPath) && !imports(pass.Pkg, poolPath) {
		return nil, nil
	}
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	insp.Preorder([]ast.Node{(*ast.FuncLit)(nil), (*ast.FuncDecl)(nil)}, func(n ast.Node) {
		runFunc(pass, n)
	})
	return nil, nil
}

func imports(pkg *types.Package, path string) bool {
	for _, imp := range pkg.Imports() {
		if imp.Path() == path {
			return true
		}
	}
	return false
}

// acquisition is one statement that binds a handle variable, and the error
// variable bound beside it if there is one.
type acquisition struct {
	stmt   ast.Node
	errVar *types.Var
	what   string
}

func runFunc(pass *analysis.Pass, node ast.Node) {
	var funcScope *types.Scope
	switch v := node.(type) {
	case *ast.FuncLit:
		funcScope = pass.TypesInfo.Scopes[v.Type]
	case *ast.FuncDecl:
		funcScope = pass.TypesInfo.Scopes[v.Type]
	}

	handles := make(map[*types.Var]acquisition)
	ast.PreorderStack(node, nil, func(n ast.Node, stack []ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok && len(stack) > 0 {
			return false // nested functions are analyzed on their own
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		what := acquirerName(pass.TypesInfo, call)
		if what == "" || len(stack) == 0 {
			return true
		}
		// The handle is the first name bound by the enclosing statement:
		//
		//	borrow, err := owner.Borrow()
		//	var borrow, err = owner.Borrow()
		var names []*ast.Ident
		stmt := stack[len(stack)-1]
		switch stmt := stmt.(type) {
		case *ast.ValueSpec:
			names = stmt.Names
		case *ast.AssignStmt:
			for _, lhs := range stmt.Lhs {
				id, _ := lhs.(*ast.Ident)
				names = append(names, id)
			}
		default:
			return true
		}
		if len(names) == 0 || names[0] == nil {
			return true
		}
		id := names[0]
		if id.Name == "_" {
			if !probesError(pass.TypesInfo, stack, names) {
				pass.ReportRangef(id, "the handle returned by %s should be released, not discarded", what)
			}
			return true
		}
		v := varOf(pass.TypesInfo, id)
		if v == nil || !funcScope.Contains(v.Pos()) {
			return true // defined outside this function: assume other uses
		}
		acq := acquisition{stmt: stmt, what: what}
		if len(names) > 1 && names[1] != nil {
			acq.errVar = varOf(pass.TypesInfo, names[1])
		}
		handles[v] = acq
		return true
	})
	if len(handles) == 0 {
		return
	}

	cfgs := pass.ResultOf[ctrlflow.Analyzer].(*ctrlflow.CFGs)
	var g *cfg.CFG
	var sig *types.Signature
	switch node := node.(type) {
	case *ast.FuncDecl:
		sig, _ = pass.TypesInfo.Defs[node.Name].Type().(*types.Signature)
		g = cfgs.FuncDecl(node)
	case *ast.FuncLit:
		sig, _ = pass.TypesInfo.Types[node.Type].Type.(*types.Signature)
		g = cfgs.FuncLit(node)
	}
	if sig == nil || g == nil {
		return
	}
	for v, acq := range handles {
		ret := lostPath(pass, g, v, acq, sig)
		if ret == nil {
			continue
		}
		line := pass.Fset.Position(acq.stmt.Pos()).Line
		pass.ReportRangef(acq.stmt, "%s returned by %s is not released on all paths", v.Name(), acq.what)
		pos, end := ret.Pos(), ret.End()
		if pass.Fset.File(pos) != pass.Fset.File(end) {
			end = pos // synthetic return at the closing brace
		}
		pass.Report(analysis.Diagnostic{
			Pos:     pos,
			End:     end,
			Message: fmt.Sprintf("this return statement may be reached without releasing %s acquired on line %d", v.Name(), line),
		})
	}
}

// probesError reports whether a blank-handle acquisition is the init of an
// if statement whose condition inspects the error result:
//
//	if _, err := owner.Borrow(); !errors.Is(err, ownership.ErrConflict) {
//
// That is a probe of whether acquisition fails, not a discarded handle, and
// is how tests assert conflicts. It is not reported.
func probesError(info *types.Info, stack []ast.Node, names []*ast.Ident) bool {
	if len(names) < 2 || names[1] == nil || len(stack) < 2 {
		return false
	}
	errVar := varOf(info, names[1])
	ifStmt, ok := stack[len(stack)-2].(*ast.IfStmt)
	if !ok || errVar == nil || ifStmt.Init != stack[len(stack)-1] {
		return false
	}
	mentioned := false
	ast.Inspect(ifStmt.Cond, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && info.Uses[id] == errVar {
			mentioned = true
		}
		return !mentioned
	})
	return mentioned
}

// nonReleasing are handle methods that use the resource without discharging
// the obligation to release it. Every other mention of the handle counts as a
// use, since responsibility may have moved with it.
var nonReleasing = map[string]bool{
	"Project": true, "Update": true, // borrows
	"Value": true, "Held": true, // leases and checkouts
	"State": true,
}

func isBlankAssign(n *ast.AssignStmt) bool {
	for _, lhs := range n.Lhs {
		if id, ok := lhs.(*ast.Ident); !ok || id.Name != "_" {
			return false
		}
	}
	return true
}

func varOf(info *types.Info, id *ast.Ident) *types.Var {
	if v, ok := info.Defs[id].(*types.Var); ok {
		return v
	}
	if v, ok := info.Uses[id].(*types.Var); ok {
		return v
	}
	return nil
}

// acquirerName reports the qualified name of the acquiring function or
// method a call resolves to, or "" if it is not one.
func acquirerName(info *types.Info, call *ast.CallExpr) string {
	fn, ok := typeutil.Callee(info, call).(*types.Func)
	if !ok || fn.Pkg() == nil {
		return ""
	}
	byRecv, ok := acquirers[fn.Pkg().Path()]
	if !ok {
		return ""
	}
	recvName := ""
	if recv := fn.Signature().Recv(); recv != nil {
		t := types.Unalias(recv.Type())
		if ptr, ok := t.(*types.Pointer); ok {
			t = types.Unalias(ptr.Elem())
		}
		named, ok := t.(*types.Named)
		if !ok {
			return ""
		}
		recvName = named.Obj().Name()
	}
	for _, name := range byRecv[recvName] {
		if name == fn.Name() {
			if recvName == "" {
				return fn.Pkg().Name() + "." + name
			}
			return fn.Pkg().Name() + "." + recvName + "." + name
		}
	}
	return ""
}

// lostPath finds a path through the CFG from the acquisition to a return
// along which v is never mentioned, ignoring the branch taken when the
// acquisition's own error is non-nil. It returns the return statement it
// reached, or nil.
func lostPath(pass *analysis.Pass, g *cfg.CFG, v *types.Var, acq acquisition, sig *types.Signature) *ast.ReturnStmt {
	namedResult := false
	for r := range sig.Results().Variables() {
		if r == v {
			namedResult = true
		}
	}
	uses := func(nodes []ast.Node) bool {
		found := false
		// Idents that mention v without discharging it: the receiver of a
		// non-releasing method, or the right side of a blank assignment.
		ignored := map[*ast.Ident]bool{}
		for _, node := range nodes {
			ast.Inspect(node, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					if sel, ok := n.Fun.(*ast.SelectorExpr); ok && nonReleasing[sel.Sel.Name] {
						if id, ok := ast.Unparen(sel.X).(*ast.Ident); ok && pass.TypesInfo.Uses[id] == v {
							ignored[id] = true
						}
					}
				case *ast.AssignStmt:
					if isBlankAssign(n) {
						for _, rhs := range n.Rhs {
							if id, ok := ast.Unparen(rhs).(*ast.Ident); ok && pass.TypesInfo.Uses[id] == v {
								ignored[id] = true
							}
						}
					}
				case *ast.Ident:
					if pass.TypesInfo.Uses[n] == v && !ignored[n] {
						found = true
					}
				case *ast.ReturnStmt:
					if n.Results == nil && namedResult {
						found = true
					}
				}
				return !found
			})
		}
		return found
	}

	var defblock *cfg.Block
	var rest []ast.Node
outer:
	for _, b := range g.Blocks {
		for i, n := range b.Nodes {
			if n == acq.stmt {
				defblock = b
				rest = b.Nodes[i+1:]
				break outer
			}
		}
	}
	if defblock == nil {
		return nil
	}
	if uses(rest) {
		return nil
	}
	if ret := defblock.Return(); ret != nil {
		return ret
	}

	seen := map[*cfg.Block]bool{defblock: true}
	memo := map[*cfg.Block]bool{}
	var search func(b *cfg.Block) *ast.ReturnStmt
	search = func(b *cfg.Block) *ast.ReturnStmt {
		for _, succ := range successors(pass, b, acq.errVar) {
			if succ == nil || seen[succ] {
				continue
			}
			seen[succ] = true
			used, ok := memo[succ]
			if !ok {
				used = uses(succ.Nodes)
				memo[succ] = used
			}
			if used {
				continue
			}
			if ret := succ.Return(); ret != nil {
				return ret
			}
			if ret := search(succ); ret != nil {
				return ret
			}
		}
		return nil
	}
	return search(defblock)
}

// successors returns b's successors with the failure branch of a test on
// errVar removed: on that branch the acquisition returned no handle, so
// there is nothing to release.
func successors(pass *analysis.Pass, b *cfg.Block, errVar *types.Var) []*cfg.Block {
	if errVar == nil || len(b.Succs) != 2 || len(b.Nodes) == 0 {
		return b.Succs
	}
	cond, ok := b.Nodes[len(b.Nodes)-1].(ast.Expr)
	if !ok {
		return b.Succs
	}
	switch failsWhen(pass.TypesInfo, cond, errVar) {
	case token.NEQ: // err != nil: true branch is the failure
		return []*cfg.Block{nil, b.Succs[1]}
	case token.EQL: // err == nil: false branch is the failure
		return []*cfg.Block{b.Succs[0], nil}
	}
	return b.Succs
}

// failsWhen reports the comparison operator if cond is `errVar != nil` or
// `errVar == nil`, and token.ILLEGAL otherwise.
func failsWhen(info *types.Info, cond ast.Expr, errVar *types.Var) token.Token {
	bin, ok := ast.Unparen(cond).(*ast.BinaryExpr)
	if !ok || (bin.Op != token.NEQ && bin.Op != token.EQL) {
		return token.ILLEGAL
	}
	isErr := func(e ast.Expr) bool {
		id, ok := ast.Unparen(e).(*ast.Ident)
		return ok && info.Uses[id] == errVar
	}
	isNil := func(e ast.Expr) bool {
		id, ok := ast.Unparen(e).(*ast.Ident)
		if !ok {
			return false
		}
		_, ok = info.Uses[id].(*types.Nil)
		return ok
	}
	if (isErr(bin.X) && isNil(bin.Y)) || (isNil(bin.X) && isErr(bin.Y)) {
		return bin.Op
	}
	return token.ILLEGAL
}

// Package lostrelease defines an Analyzer that reports velocity resources
// acquired but not released on every path.
//
// # Analyzer lostrelease
//
// lostrelease: check that acquired velocity handles are released
//
// A function is an acquirer when its declaration carries the
// //velocity:acquires directive; velocity marks its own, and any library
// can mark its own handle-returning functions to get the same checking. The
// classification travels to consumers as a package fact, so it is written
// once at the declaration rather than maintained as a list here.
//
// A built-in table covers velocity releases that predate the directive —
// an advanced borrow (ownership.Owner.Borrow, BorrowMut, and the same on
// Shared and Frozen), a lease (ownership.NewLease), a pool checkout
// (pool.Pool.Get), or a permit (async.Semaphore.Acquire/TryAcquire,
// async.Mutex.Lock/TryLock). Either way the handle must be released
// exactly once. A leaked borrow blocks its
// cell until it is released, and production builds have no runtime net for
// it, so the check belongs here, before the code runs.
//
// The second result decides which branch is the failure: an error tested
// against nil, or a bool tested directly, as the Try forms return.
//
// The analyzer reports a handle assigned to the blank identifier, and a
// handle for which some control-flow path from the acquisition to a return
// never uses the variable. Almost any mention counts as a use — Release,
// Close, Discard, Move, a defer, passing it to another function, sending or
// returning it — since responsibility may have moved with it; the check is
// conservative. Only calls that consume the resource without discharging it
// (Project, Update, Value, Held, State) and `_ = handle` do not count. A
// path taken because the acquisition's own error result is non-nil is not
// reported, since no handle exists on it, and neither is the zero-iteration
// exit of a loop that provably runs at least once (`for {}`, `for range 3`,
// `for i := 0; i < n; ...` with constant bounds, a range over a non-empty
// literal or array); a release inside such a loop counts. A loop whose
// iteration count depends on a value at runtime is still assumed able to
// run zero times.
package lostrelease

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/ctrlflow"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
	"golang.org/x/tools/go/cfg"
	"golang.org/x/tools/go/types/typeutil"
)

var Analyzer = &analysis.Analyzer{
	Name:      "lostrelease",
	Doc:       "check that acquired velocity handles are released on all paths",
	URL:       "https://pkg.go.dev/github.com/apsis-io/velocity/analysis/lostrelease",
	Run:       run,
	Requires:  []*analysis.Analyzer{inspect.Analyzer, ctrlflow.Analyzer},
	FactTypes: []analysis.Fact{(*acquires)(nil)},
}

// Marker is the directive that declares a function to hand back something
// its caller must release. Written in the doc comment of the function
// itself, it puts the classification where the reader of that function
// already is, rather than in a table in another module:
//
//	// Borrow acquires an advanced shared read borrow.
//	//
//	//velocity:acquires
//	func (o *Owner[T]) Borrow() (*ReadBorrow[T], error) {
//
// It is a directive by Go's rules — a lowercase name, a colon, no space —
// so godoc hides it from rendered documentation.
//
// Any package may use it, not only velocity's: a library with its own
// handle type gets the same checking by marking the function that hands
// one out.
const Marker = "//velocity:acquires"

// acquires is exported as a fact about a marked package, so that a package
// analysed later — a consumer, which sees velocity only through export data
// and never its comments — learns what to track without the analyzer
// carrying a list of names.
//
// It is a package fact holding qualified names rather than an object fact
// per function, because most of velocity's acquirers are methods on generic
// types: a call site yields the method of an instantiation, which is a
// different object from the declaration a fact would attach to, and no
// amount of Origin-chasing reliably bridges instantiation back to
// declaration. A package fact is keyed on the package, which has no
// instantiations.
type acquires struct {
	// Names holds "Recv.Method" for methods and "Func" for functions.
	// Exported for gob.
	Names map[string]bool
}

func (*acquires) AFact() {}

func (a *acquires) String() string {
	return fmt.Sprintf("acquires %d", len(a.Names))
}

// factKey is how a declaration is named inside an acquires fact.
func factKey(recv, name string) string {
	if recv == "" {
		return name
	}
	return recv + "." + name
}

const (
	ownershipPath = "github.com/apsis-io/velocity/ownership"
	poolPath      = "github.com/apsis-io/velocity/pool"
	asyncPath     = "github.com/apsis-io/velocity/async"
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
	asyncPath: {
		"Semaphore": {"Acquire", "TryAcquire"},
		"Mutex":     {"Lock", "TryLock"},
	},
}

func run(pass *analysis.Pass) (any, error) {
	// Always publish facts for this package's own marked functions, even
	// when it has nothing to check itself: that is how a library declares
	// its acquirers to everything downstream.
	marked := exportMarkers(pass)

	if !marked && !imports(pass.Pkg, ownershipPath) && !imports(pass.Pkg, poolPath) && !imports(pass.Pkg, asyncPath) {
		return nil, nil
	}
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	insp.Preorder([]ast.Node{(*ast.FuncLit)(nil), (*ast.FuncDecl)(nil)}, func(n ast.Node) {
		runFunc(pass, n)
	})
	return nil, nil
}

// exportMarkers publishes one package fact naming every function in this
// package whose doc comment carries Marker, and reports whether any did.
func exportMarkers(pass *analysis.Pass) bool {
	names := map[string]bool{}
	for _, file := range pass.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Doc == nil || !hasMarker(fn.Doc) {
				continue
			}
			obj, ok := pass.TypesInfo.Defs[fn.Name].(*types.Func)
			if !ok {
				continue
			}
			names[factKey(receiverName(obj), obj.Name())] = true
		}
	}
	if len(names) == 0 {
		return false
	}
	pass.ExportPackageFact(&acquires{Names: names})
	return true
}

// receiverName is the bare type name a method hangs off, generics stripped,
// or "" for a plain function.
func receiverName(fn *types.Func) string {
	recv := fn.Signature().Recv()
	if recv == nil {
		return ""
	}
	t := types.Unalias(recv.Type())
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	if named, ok := t.(*types.Named); ok {
		return named.Obj().Name()
	}
	return ""
}

func hasMarker(doc *ast.CommentGroup) bool {
	for _, comment := range doc.List {
		if strings.TrimSpace(comment.Text) == Marker {
			return true
		}
	}
	return false
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
		what := acquirerName(pass, call)
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
	if isBool(errVar) {
		// `if _, ok := mu.TryLock(); ok` holds the lock on its true branch
		// with no way to release it: a leak, not a probe.
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
func acquirerName(pass *analysis.Pass, call *ast.CallExpr) string {
	fn, ok := typeutil.Callee(pass.TypesInfo, call).(*types.Func)
	if !ok || fn.Pkg() == nil {
		return ""
	}
	// A fact from the declaring package is authoritative: it was written at
	// the declaration by whoever owns it.
	var fact acquires
	if pass.ImportPackageFact(fn.Pkg(), &fact) && fact.Names[factKey(receiverName(fn), fn.Name())] {
		return qualifiedName(fn)
	}
	// Otherwise the built-in table, which covers velocity releases that
	// predate the marker.
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
			return qualifiedName(fn)
		}
	}
	return ""
}

// qualifiedName renders pkg.Func or pkg.Type.Method for a diagnostic.
func qualifiedName(fn *types.Func) string {
	name := fn.Pkg().Name() + "."
	if recv := receiverName(fn); recv != "" {
		name += recv + "."
	}
	return name + fn.Name()
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

// successors returns b's successors minus the edges a path cannot take:
// the failure branch of a test on errVar, where the acquisition returned no
// handle and there is nothing to release; and the zero-iteration exit of a
// loop that provably runs at least once, so a release inside such a loop
// counts on every path through it.
func successors(pass *analysis.Pass, b *cfg.Block, errVar *types.Var) []*cfg.Block {
	if len(b.Succs) != 2 {
		return b.Succs
	}
	switch b.Kind {
	case cfg.KindForLoop, cfg.KindRangeLoop:
		// Succs[0] enters the body, Succs[1] leaves the loop.
		if runsAtLeastOnce(pass.TypesInfo, b.Stmt) {
			return []*cfg.Block{b.Succs[0], nil}
		}
		return b.Succs
	}
	if errVar == nil || len(b.Nodes) == 0 {
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

// runsAtLeastOnce reports whether a loop statement's body is certain to
// execute: `for {}`, `for i := c1; i < c2; ...` with constants c1 < c2, a
// range over a positive integer constant, a non-empty composite literal, a
// string constant, or an array type of positive length.
func runsAtLeastOnce(info *types.Info, stmt ast.Stmt) bool {
	switch loop := stmt.(type) {
	case *ast.ForStmt:
		if loop.Cond == nil {
			return true
		}
		return firstIterationHolds(info, loop.Init, loop.Cond)
	case *ast.RangeStmt:
		x := ast.Unparen(loop.X)
		if tv, ok := info.Types[x]; ok && tv.Value != nil {
			switch tv.Value.Kind() {
			case constant.Int:
				n, exact := constant.Int64Val(tv.Value)
				return exact && n > 0
			case constant.String:
				return len(constant.StringVal(tv.Value)) > 0
			}
		}
		if lit, ok := x.(*ast.CompositeLit); ok {
			return len(lit.Elts) > 0
		}
		if tv, ok := info.Types[x]; ok {
			if arr, ok := types.Unalias(tv.Type).Underlying().(*types.Array); ok {
				return arr.Len() > 0
			}
		}
	}
	return false
}

// firstIterationHolds evaluates `i op c` for the pattern `i := c0; i op c`
// where c0 and c are integer constants and i is the loop's own variable.
func firstIterationHolds(info *types.Info, init ast.Stmt, cond ast.Expr) bool {
	assign, ok := init.(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return false
	}
	loopVar, ok := assign.Lhs[0].(*ast.Ident)
	if !ok {
		return false
	}
	start := constInt(info, assign.Rhs[0])
	bin, ok := ast.Unparen(cond).(*ast.BinaryExpr)
	if !ok {
		return false
	}
	id, ok := ast.Unparen(bin.X).(*ast.Ident)
	if !ok || info.Uses[id] != info.Defs[loopVar] || start == nil {
		return false
	}
	bound := constInt(info, bin.Y)
	if bound == nil {
		return false
	}
	return constant.Compare(start, bin.Op, bound)
}

func isBool(v *types.Var) bool {
	basic, ok := types.Unalias(v.Type()).Underlying().(*types.Basic)
	return ok && basic.Kind() == types.Bool
}

func constInt(info *types.Info, e ast.Expr) constant.Value {
	tv, ok := info.Types[ast.Unparen(e)]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.Int {
		return nil
	}
	return tv.Value
}

// failsWhen reports token.NEQ if the true branch of cond is the failure
// (`err != nil`, `!ok`), token.EQL if the false branch is (`err == nil`,
// `ok`), and token.ILLEGAL if cond does not test the second result.
func failsWhen(info *types.Info, cond ast.Expr, errVar *types.Var) token.Token {
	cond = ast.Unparen(cond)
	if isBool(errVar) {
		if id, ok := cond.(*ast.Ident); ok && info.Uses[id] == errVar {
			return token.EQL // `if ok`: false branch fails
		}
		if not, ok := cond.(*ast.UnaryExpr); ok && not.Op == token.NOT {
			if id, ok := ast.Unparen(not.X).(*ast.Ident); ok && info.Uses[id] == errVar {
				return token.NEQ // `if !ok`: true branch fails
			}
		}
		return token.ILLEGAL
	}
	bin, ok := cond.(*ast.BinaryExpr)
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

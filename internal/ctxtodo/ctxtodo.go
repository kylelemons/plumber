// Copyright 2021 Kyle Lemons
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package ctxtodo implements a Go Analyzer for detecting context.TODO() and plumbing contexts to it.
package ctxtodo

import (
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"log"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ast/inspector"
)

// Analyzer provides the ctxtodo analyzer.
var Analyzer = &analysis.Analyzer{
	Name:  "ctxtodo",
	Doc:   "Find calls of the context.TODO function in need of plumbing.",
	Run:   run,
	Flags: flags(),

	FactTypes: []analysis.Fact{
		new(NeedsContext), // propagate the necessity of adding ctx parameters
	},

	// We want to be able to add context parameters where they were missing,
	// so we will need to run even if there were some type-checking errors.
	RunDespiteErrors: true,
}

var (
	// ModuleCache is a prefix that will cause suggested fixes to be ignored.
	ModuleCache string
)

func init() {
	modcache, _ := exec.Command("go", "env", "GOMODCACHE").CombinedOutput()
	ModuleCache = strings.TrimSpace(string(modcache))
}

func flags() flag.FlagSet {
	flag := flag.NewFlagSet("ctxtodo", flag.ContinueOnError)
	flag.StringVar(&ModuleCache, "modcache", ModuleCache, "Module cache directory (ignored for fixes)")
	return *flag
}

// NeedsContext indicates that an exported function is having a context added
// by the ctxtodo analyzer, so that other packages can understand the need to
// add a context parameter.
type NeedsContext struct{}

func (NeedsContext) AFact()         {}
func (NeedsContext) String() string { return "NeedsContext" }

// TODO(kevlar): Potential future improvements:
//  - Add a --maxdepth flag to limit how many levels it will edit
//  - Add a --stop repeated regex flag to prevent plumbing through matched functions
//  - Detect calls like (foo) to functions taking (context, foo)

func run(pass *analysis.Pass) (interface{}, error) {
	if ModuleCache == "" {
		return nil, fmt.Errorf("failed to determine GOMODCACHE, specify --modcache flag")
	}
	filterReports(pass)

	r := &runner{
		Pass:            pass,
		byObj:           map[types.Object]*ast.FuncDecl{},
		callers:         map[types.Object][]localCall{},
		paramAdded:      map[*ast.FuncDecl]bool{},
		contextImported: map[*ast.File]bool{},
	}
	r.buildScopeMap()
	r.buildCallGraph()
	r.buildDiagnostics()
	return nil, nil
}

type runner struct {
	*analysis.Pass

	// Analysis State
	byObj       map[types.Object]*ast.FuncDecl
	byScope     map[*types.Scope]ast.Node    // *ast.FuncDecl or *ast.FuncLit
	callers     map[types.Object][]localCall // callers[target] = [funcs calling target]
	todos       []localCall
	transitives []localCall

	// Diagnostic state
	paramAdded      map[*ast.FuncDecl]bool
	contextImported map[*ast.File]bool
}

func filterReports(p *analysis.Pass) {
	actualReport := p.Report
	p.Report = func(diag analysis.Diagnostic) {
		for _, sf := range diag.SuggestedFixes {
			for _, te := range sf.TextEdits {
				for _, pos := range []token.Pos{te.Pos, te.End} {
					if !pos.IsValid() {
						continue
					}
					if filename := p.Fset.Position(diag.Pos).Filename; strings.HasPrefix(filename, ModuleCache) {
						return // don't try to edit files in the go module cache
					}
				}
			}
		}
		actualReport(diag)
	}
}

func (r *runner) isContextTODO(obj types.Object) bool {
	fun, ok := obj.(*types.Func)
	if !ok || fun.Pkg() == nil {
		return false
	}
	return fun.Pkg().Path() == "context" && fun.Name() == "TODO"
}

func (r *runner) isContextContext(otyp types.Type) bool {
	typ, ok := otyp.(*types.Named)
	if !ok || typ.Obj() == nil || typ.Obj().Pkg() == nil {
		return false
	}
	return typ.Obj().Pkg().Path() == "context" && typ.Obj().Name() == "Context"
}

// buildScopeMap inverts the type-checking scope map so it can be indexed by scope.
func (r *runner) buildScopeMap() {
	r.byScope = make(map[*types.Scope]ast.Node, len(r.TypesInfo.Scopes))
	for node, scope := range r.TypesInfo.Scopes {
		r.byScope[scope] = node
	}
}

// buildCallGraph walks the function declarations in the package looking for calls,
// creating a call graph and taking note of the locations of the todo calls we want to target.
func (r *runner) buildCallGraph() {
	walker := inspector.New(r.Files)
	const (
		Push = true
		Pop  = false
	)

	walker.WithStack(nil, func(node ast.Node, op bool, stack []ast.Node) (proceed bool) {
		switch op {
		case Push:
			switch n := node.(type) {
			case *ast.FuncDecl:
				return r.walkFuncDecl(n)
			case *ast.AssignStmt:
				return r.walkAssignStmt(stack, n)
			case *ast.CallExpr:
				return r.walkCallExpr(stack, n)
			}
		case Pop:
			// pass
		}
		return true
	})
}

func (r *runner) buildDiagnostics() {
	for _, call := range r.todos {
		r.rewriteTODO(call)
	}
	for _, transitive := range r.transitives {
		r.rewriteTransitives(transitive)
	}
}

type localCall struct {
	path   astPath         // declaration of the enclosing function
	call   *ast.CallExpr   // call expression of the called function
	assign *ast.AssignStmt // if present, the "ctx :=" assignment for the call
}

func (r *runner) walkFuncDecl(decl *ast.FuncDecl) bool {
	obj := r.TypesInfo.ObjectOf(decl.Name).(*types.Func)
	if obj == nil {
		log.Printf("insufficient types to analyze %q", decl.Name)
		return true
	}
	r.byObj[obj] = decl
	return true
}

func (r *runner) walkAssignStmt(stack []ast.Node, assign *ast.AssignStmt) bool {
	// Looking for: ctx := context.TODO()
	if len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return true
	}

	// Looking for: "ctx :=" or "_ ="
	ident, ok := assign.Lhs[0].(*ast.Ident)
	if !ok {
		return true
	}
	switch ident.Name {
	case "ctx", "_":
		// these are fine to replace
	default:
		// otherwise this isn't an assignment we want to touch
		return true
	}

	// Looking for: ":= context.TODO()"
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok {
		return true
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return true
	}
	if !r.isContextTODO(r.TypesInfo.ObjectOf(sel.Sel)) {
		return true
	}

	r.todos = append(r.todos, localCall{
		path:   forStack(stack),
		call:   call,
		assign: assign,
	})
	return false // don't double-walk the call expression if we found our assignment
}

func (r *runner) walkCallExpr(stack []ast.Node, call *ast.CallExpr) bool {
	var ident *ast.Ident
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		ident = fun
	case *ast.SelectorExpr:
		ident = fun.Sel
	default:
		return true // who knows what this is, keep walking
	}

	called := r.TypesInfo.ObjectOf(ident)
	if called == nil {
		return true // no type info for called function
	}

	// Check if this is a call to context.TODO
	if r.isContextTODO(called) {
		r.todos = append(r.todos, localCall{
			path: forStack(stack),
			call: call,
		})
		return false // we're done here
	}

	// Check if this is a call to something in this package
	if r.isLocal(called.Pkg()) {
		r.callers[called] = append(r.callers[called], localCall{
			path: forStack(stack),
			call: call,
		})
	}

	// Check if this is a func for which we added a context to a call from another package
	if r.ImportObjectFact(called, new(NeedsContext)) {
		r.transitives = append(r.transitives, localCall{
			path: forStack(stack),
			call: call,
		})
	}

	return true // keep walking in case there's something deeper in the AST (e.g. arguments to this call)
}

func (r *runner) rewriteTODO(todo localCall) {
	seen := map[types.Object]bool{}

	var edits []analysis.TextEdit
	if todo.assign != nil {
		// If this is an assignment of the ctx parameter, we can just remove it
		edits = append(edits, r.propagateContextThrough(todo.path.decl(), seen)...)
		edits = append(edits, analysis.TextEdit{
			Pos: todo.assign.Pos(),
			End: todo.assign.Rhs[0].(*ast.CallExpr).Rparen + 1,
		})
	} else if expr, ok := r.hasContextProviderInPath(todo.path, todo.call.Pos()); ok {
		// If we have a way to get the parameter, we can use that
		edits = append(edits, analysis.TextEdit{
			Pos:     todo.call.Pos(),
			End:     todo.call.End(),
			NewText: []byte(expr),
		})
	} else {
		// Otherwise, since we're adding the ctx parameter to this function,
		// we also need to update the call that we're rewriting to "ctx".
		edits = append(edits, r.propagateContextThrough(todo.path.decl(), seen)...)
		edits = append(edits, analysis.TextEdit{
			Pos:     todo.call.Pos(),
			End:     todo.call.End(),
			NewText: []byte("ctx"),
		})
	}

	r.Report(analysis.Diagnostic{
		Pos:      todo.call.Pos(),
		End:      todo.call.End(),
		Category: "context",
		Message:  fmt.Sprintf("Plumb context"),
		SuggestedFixes: []analysis.SuggestedFix{
			{
				Message:   "Plumb context.Context",
				TextEdits: edits,
			},
		},
	})
}

func (r *runner) rewriteTransitives(todo localCall) {
	seen := map[types.Object]bool{}
	edits := r.propagateContextForCall(todo, seen)
	r.Report(analysis.Diagnostic{
		Pos:      todo.call.Pos(),
		End:      todo.call.End(),
		Category: "context",
		Message:  "Continue plumbing context",
		SuggestedFixes: []analysis.SuggestedFix{
			{
				Message:   "Plumb context.Context",
				TextEdits: edits,
			},
		},
	})
}

func (r *runner) propagateContextThrough(funcDecl *ast.FuncDecl, seen map[types.Object]bool) (edits []analysis.TextEdit) {
	if funcDecl == nil {
		return nil
	}

	fun := r.TypesInfo.ObjectOf(funcDecl.Name).(*types.Func)
	if seen[fun] {
		return
	}
	seen[fun] = true

	// Make sure a different diagnostic didn't add a context parameter already
	if r.paramAdded[funcDecl] {
		return
	}
	r.paramAdded[funcDecl] = true

	// Check if the function itself has a ctx parameter.
	//
	// If it does, we don't need to propagate the context because it already has one.
	params := fun.Type().(*types.Signature).Params()
	for i, n := 0, params.Len(); i < n; i++ {
		param := params.At(i)
		if param.Name() == "ctx" {
			// Call already has a "ctx" parameter.
			if !r.isContextContext(param.Type()) {
				r.ReportRangef(funcDecl, "Non-context ctx parameter")
			}
			return
		}
	}

	// Check if the function is main or a top-level test function.
	//
	// If it is, then we can't add ctx, so we'll just stop.
	if r.isMainOrInit(fun) || r.isTopLevelTestFunc(funcDecl) {
		edits = append(edits, r.editToAddContextVarDecl(funcDecl, "context.Background()"))
		edits = append(edits, r.editToImportContext(funcDecl.Name.Pos())...)
		return
	}

	// Check if the function has any parameters that can provide a context (e.g. http.Request)
	if expr, ok := r.hasContextProviderParam(fun); ok {
		edits = append(edits, r.editToAddContextVarDecl(funcDecl, expr))
		edits = append(edits, r.editToImportContext(funcDecl.Name.Pos())...)
		return
	}

	log.Printf("Adding context to %s", fun.FullName())

	// If it is an exported function, allow other packages to understand the context is being added
	if fun.Exported() {
		r.ExportObjectFact(fun, &NeedsContext{})
	}

	// Add the parameter
	edits = append(edits, r.editToPrependCtxParam(funcDecl))
	edits = append(edits, r.editToImportContext(funcDecl.Name.Pos())...)

	for _, caller := range r.callers[r.TypesInfo.ObjectOf(funcDecl.Name)] {
		edits = append(edits, r.propagateContextForCall(caller, seen)...)
	}

	return
}

func (r *runner) propagateContextForCall(caller localCall, seen map[types.Object]bool) (edits []analysis.TextEdit) {
	if expr, ok := r.hasContextProviderInPath(caller.path, caller.call.Pos()); ok {
		// There is already a way to get "ctx" in the current scope, call it and move on
		edits = append(edits, r.editToPrependExpr(caller.call, expr))
		return
	}

	// Ensure that the calling function itself has a ctx parameter to pass
	edits = append(edits, r.propagateContextThrough(caller.path.decl(), seen)...)

	// Add the new "ctx" parameter to call-sites
	edits = append(edits, r.editToPrependExpr(caller.call, "ctx"))
	return
}

func (r *runner) isMainOrInit(fun *types.Func) bool {
	if fun.Pkg().Name() == "main" && fun.Name() == "main" {
		return true
	}
	if fun.Name() == "init" && fun.Type().(*types.Signature).Recv() == (*types.Var)(nil) {
		return true
	}
	return false
}

func (r *runner) isTopLevelTestFunc(funcDecl *ast.FuncDecl) bool {
	return strings.HasSuffix(r.Fset.Position(funcDecl.Pos()).Filename, "_test.go") && topLevelTestFunc.MatchString(funcDecl.Name.Name)
}

func (r *runner) hasContextProviderInPath(caller astPath, at token.Pos) (string, bool) {
	if len(caller) == 0 {
		return "", false
	}
	prev, last := caller.pop()
	switch last := last.(type) {
	case *ast.AssignStmt:
		// When we walk out of an assignment, update the "at" position because anything within
		// the assignment can't consider anything declared inside it.
		at = last.Pos()
	case *ast.FuncDecl:
		// Check formal parameters first
		if expr, ok := r.hasContextProviderParam(r.TypesInfo.ObjectOf(last.Name).(*types.Func)); ok {
			return expr, true
		}
		// Check variables that are in scope
		if expr, ok := r.hasContextProviderInScope(r.TypesInfo.Scopes[last.Type], at); ok {
			return expr, true
		}
	case *ast.FuncLit: // TODO block
		// Check formal parameters first
		if expr, ok := r.hasContextProviderField(last.Type.Params); ok {
			return expr, true
		}
		// Check variables that are in scope
		if expr, ok := r.hasContextProviderInScope(r.TypesInfo.Scopes[last.Type], at); ok {
			return expr, true
		}
	}
	return r.hasContextProviderInPath(prev, at)
}

func (r *runner) hasContextProviderParam(fun *types.Func) (expr string, ok bool) {
	params := fun.Type().(*types.Signature).Params()
	for i, n := 0, params.Len(); i < n; i++ {
		param := params.At(i)
		paramName := param.Name()
		if paramName == "" {
			paramName = fmt.Sprintf("unnamedParam%d", i)
			if r.isContextContext(param.Type()) || r.typeHasContextMethod(param.Type()) {
				r.Reportf(param.Pos(), "Name this param if you want plumber to use it")
			}
		}
		if r.isContextContext(param.Type()) {
			return paramName, true
		}
		if r.typeHasContextMethod(param.Type()) {
			return paramName + ".Context()", true
		}
	}
	return "", false
}

func (r *runner) hasContextProviderField(fields *ast.FieldList) (expr string, ok bool) {
	for i, field := range fields.List {
		tav, ok := r.TypesInfo.Types[field.Type]
		if !ok {
			continue
		}
		var fieldName string
		if len(field.Names) > 0 {
			fieldName = field.Names[0].Name
		} else {
			fieldName = fmt.Sprintf("unnamedParam%d", i)
			if r.isContextContext(tav.Type) || r.typeHasContextMethod(tav.Type) {
				r.ReportRangef(field, "Name this param if you want plumber to use it")
			}
		}
		if r.isContextContext(tav.Type) {
			return fieldName, true
		}
		if r.typeHasContextMethod(tav.Type) {
			return fieldName + ".Context()", true
		}
	}
	return "", false
}

func (r *runner) hasContextProviderInScope(scope *types.Scope, at token.Pos) (expr string, ok bool) {
	for _, varname := range scope.Names() {
		param := scope.Lookup(varname)
		if param.Pos() >= at {
			continue
		}
		if r.isContextContext(param.Type()) {
			return param.Name(), true
		}
		if r.typeHasContextMethod(param.Type()) {
			return param.Name() + ".Context()", true
		}
	}
	return "", false
}

func (r *runner) typeHasContextMethod(typ types.Type) bool {
	if ptr, ok := typ.(*types.Pointer); ok {
		return r.typeHasContextMethod(ptr.Elem())
	}

	named, ok := typ.(*types.Named)
	if !ok {
		return false
	}
	for i, n := 0, named.NumMethods(); i < n; i++ {
		meth := named.Method(i)
		if meth.Name() != "Context" {
			continue
		}
		sig := meth.Type().(*types.Signature)
		if sig.Results().Len() != 1 {
			continue
		}
		if r.isContextContext(sig.Results().At(0).Type()) {
			return true
		}
	}
	return false
}

func (r *runner) editToAddContextVarDecl(funcDecl *ast.FuncDecl, call string) analysis.TextEdit {
	return analysis.TextEdit{
		Pos:     funcDecl.Body.Lbrace + 1,
		End:     funcDecl.Body.Lbrace + 1,
		NewText: []byte("ctx := " + call + ";"),
	}
}

func (r *runner) editToPrependCtxParam(funcDecl *ast.FuncDecl) analysis.TextEdit {
	return analysis.TextEdit{
		Pos:     funcDecl.Type.Params.Opening + 1,
		End:     funcDecl.Type.Params.Opening + 1,
		NewText: []byte("ctx context.Context, "),
	}
}

func (r *runner) editToPrependExpr(callExpr *ast.CallExpr, varname string) analysis.TextEdit {
	return analysis.TextEdit{
		Pos:     callExpr.Lparen + 1,
		End:     callExpr.Lparen + 1,
		NewText: []byte(varname + ", "),
	}
}

func (r *runner) editToImportContext(pos token.Pos) []analysis.TextEdit {
	filename := r.Fset.Position(pos).Filename
	var file *ast.File
	for _, f := range r.Files {
		if r.Fset.Position(f.Pos()).Filename == filename {
			file = f
			break
		}
	}
	if file == nil {
		log.Printf("Warning: failed to find file %q to add context import", filename)
		return nil
	}

	if r.contextImported[file] {
		return nil
	}
	r.contextImported[file] = true

	for _, imp := range file.Imports {
		if imp.Path.Value == `"context"` {
			return nil
		}
	}

	var importBlock *ast.GenDecl
	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *ast.GenDecl:
			if decl.Tok.String() == "import" && decl.Lparen.IsValid() {
				importBlock = decl
				break
			}
		}
	}

	// If we found an import block, add it at the beginning
	if importBlock != nil {
		log.Printf("Adding import to %q", filepath.Base(filename))
		return []analysis.TextEdit{{
			Pos:     importBlock.Lparen + 1,
			End:     importBlock.Lparen + 1,
			NewText: []byte(`"context";`),
		}}
	}
	// Otherwise add a new declaration before the first one
	if len(file.Decls) > 0 { // should always be true
		log.Printf("Adding import to %q (no import block found)", filepath.Base(filename))
		first := file.Decls[0]
		return []analysis.TextEdit{{
			Pos:     first.Pos(),
			End:     first.Pos(),
			NewText: []byte(`import "context";`),
		}}
	}

	log.Printf("Warning: unable to add import to %q", filepath.Base(filename))
	return nil
}

func (r *runner) isLocal(pkg *types.Package) bool {
	if pkg == nil {
		return false
	}
	return r.Pkg.Path() == pkg.Path()
}

var topLevelTestFunc = regexp.MustCompile(`^(Test|Benchmark|Fuzz)[A-Z]`)

type astPath []ast.Node

func forStack(stack []ast.Node) astPath {
	return append([]ast.Node(nil), stack...)
}

func (p astPath) decl() (last *ast.FuncDecl) {
	for _, n := range p {
		if decl, ok := n.(*ast.FuncDecl); ok {
			last = decl
		}
	}
	return
}

func (p astPath) pop() (astPath, ast.Node) {
	n := len(p) - 1
	return p[:n], p[n]
}

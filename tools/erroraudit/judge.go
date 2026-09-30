package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"regexp"
	"strings"
)

// judge runs every rule over one file of the delivered code. The tests are left
// out on purpose: the quality of the tests is the subject of P23-T07, and a rule
// that judged a fake, a fixture or a double would be refusing the scaffolding
// that makes the failure paths reachable.
func judge(result *measured, positions *token.FileSet, index *moduleIndex, path string, file *ast.File) {
	judgeContexts(result, positions, path, file)
	judgeContextTodos(result, positions, path, file)
	judgeResources(result, positions, index, path, file)
	judgeClients(result, positions, path, file)
	judgeWrapping(result, positions, path, file)
	judgeDiscarded(result, positions, index, path, file)
	judgeMessages(result, positions, path, file)
	judgeGoroutines(result, positions, index, path, file)
}

// functions walks the declarations with a body, which is where every rule of
// this gate applies.
func functions(file *ast.File, visit func(*ast.FuncDecl)) {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Body != nil {
			visit(function)
		}
	}
}

// judgeContexts refuses the context a function takes and never uses: the
// cancellation, the deadline and the request-scoped values of the caller are all
// in that argument, and a body that never mentions it has silently dropped them.
func judgeContexts(result *measured, positions *token.FileSet, path string, file *ast.File) {
	functions(file, func(function *ast.FuncDecl) {
		for _, field := range function.Type.Params.List {
			if !isContextType(field.Type) {
				continue
			}
			for _, name := range field.Names {
				if name.Name == "_" {
					continue
				}
				result.ContextParams++
				if mentions(function.Body, name.Name) {
					continue
				}
				result.Findings = append(result.Findings, finding{
					Rule: RuleContextDropped, Path: path, Line: positions.Position(name.Pos()).Line,
					Detail: fmt.Sprintf("%s recebe %q e nunca o usa: o cancelamento e o prazo do chamador param aqui", funcName(function), name.Name),
				})
			}
		}
	})
}

// isContextType recognizes the parameter type this gate is about: the context of
// the standard library, written as `context.Context`.
func isContextType(expression ast.Expr) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return ok && identifier.Name == "context" && selector.Sel.Name == "Context"
}

// judgeContextTodos refuses the context nobody chose. `context.TODO` is the
// placeholder of the standard library: it says the author knew a context was
// missing, which is exactly the sentence this gate does not accept in delivered
// code.
func judgeContextTodos(result *measured, positions *token.FileSet, path string, file *ast.File) {
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || selectorText(call.Fun) != "context.TODO" {
			return true
		}
		result.Findings = append(result.Findings, finding{
			Rule: RuleContextTodo, Path: path, Line: positions.Position(call.Pos()).Line,
			Detail: "context.TODO(): o contexto que ninguém escolheu — passe o do chamador ou nomeie o dono do novo",
		})
		return true
	})
}

// acquisition is a resource this gate watched being acquired, and the kind of
// cleanup it owes.
type acquisition struct {
	name string
	kind string
	line int
}

// judgesAcquisition answers the acquisition shape and the kind of cleanup it
// owes, or "" when the call acquires nothing this gate tracks.
func judgesAcquisition(call *ast.CallExpr) string {
	chain := selectorText(call.Fun)
	switch {
	case strings.HasPrefix(chain, "os.Open") || strings.HasPrefix(chain, "os.Create"):
		return "arquivo"
	case strings.HasSuffix(chain, ".Begin") || strings.HasSuffix(chain, ".BeginTx"):
		return "transação"
	case strings.HasSuffix(chain, ".Do"):
		return "resposta HTTP"
	case strings.HasSuffix(chain, ".Dial") || strings.HasSuffix(chain, ".DialContext"):
		return "conexão"
	case strings.HasSuffix(chain, ".Query") && len(call.Args) > 0 && isContextish(call.Args[0]):
		return "consulta"
	}
	return ""
}

// isContextish recognizes the first argument of a query that is a context: the
// URL query of an incoming request is a different `Query`, and the gate tells
// them apart by what the call is given.
func isContextish(expression ast.Expr) bool {
	switch typed := expression.(type) {
	case *ast.Ident:
		return typed.Name == "ctx" || strings.Contains(strings.ToLower(typed.Name), "context")
	case *ast.CallExpr:
		return strings.HasSuffix(selectorText(typed.Fun), ".Context")
	}
	return false
}

// cleanupChain answers which cleanups a name owes: a resource is closed, and a
// transaction is rolled back — committing is the success path and not the
// cleanup the error path needs.
func cleanupChain(kind string) string {
	if kind == "transação" {
		return ".Rollback"
	}
	return ".Close"
}

// judgeResources refuses the resource nobody releases. The rule reads the
// function the acquisition happens in: a value that is closed, rolled back or
// handed to another call is accounted for, and a value that is none of those
// leaks one connection, one row set or one file per call.
func judgeResources(result *measured, positions *token.FileSet, index *moduleIndex, path string, file *ast.File) {
	functions(file, func(function *ast.FuncDecl) {
		acquired := []acquisition{}
		chains := []string{}
		handed := map[string]bool{}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.AssignStmt:
				acquired = append(acquired, acquisitionsOf(positions, typed)...)
			case *ast.CallExpr:
				chains = append(chains, selectorText(typed.Fun))
				markHanded(handed, typed.Args)
			case *ast.ReturnStmt:
				markHanded(handed, typed.Results)
			}
			return true
		})
		for _, entry := range acquired {
			if entry.kind == "transação" {
				result.Transactions++
			} else {
				result.Acquired++
			}
			if cleanup(entry.name, cleanupChain(entry.kind), chains) || handed[entry.name] {
				continue
			}
			result.Findings = append(result.Findings, refusal(function, path, entry))
		}
	})
}

// refusal names the rule an unreleased acquisition breaks. The transaction has
// its own rule and its own sentence: what it owes is the rollback of the error
// path, which is a different mistake from a body left open.
func refusal(function *ast.FuncDecl, path string, entry acquisition) finding {
	if entry.kind == "transação" {
		return finding{
			Rule: RuleTransaction, Path: path, Line: entry.line,
			Detail: fmt.Sprintf("%s abre a transação e nunca a desfaz: sem `defer %s.Rollback(ctx)` o erro entre a abertura e o commit deixa a trava aberta",
				funcName(function), entry.name),
		}
	}
	return finding{
		Rule: RuleUnclosedResource, Path: path, Line: entry.line,
		Detail: fmt.Sprintf("%s adquire %s e nunca o fecha: sem `defer %s%s()` o recurso vaza a cada chamada",
			funcName(function), entry.kind, entry.name, cleanupChain(entry.kind)),
	}
}

// acquisitionsOf reads the resources an assignment acquires, one per name bound
// to a tracked call.
func acquisitionsOf(positions *token.FileSet, assignment *ast.AssignStmt) []acquisition {
	acquired := []acquisition{}
	for index, right := range assignment.Rhs {
		call, ok := right.(*ast.CallExpr)
		if !ok {
			continue
		}
		kind := judgesAcquisition(call)
		if kind == "" || index >= len(assignment.Lhs) {
			continue
		}
		name, ok := assignment.Lhs[index].(*ast.Ident)
		if !ok || name.Name == "_" {
			continue
		}
		acquired = append(acquired, acquisition{name: name.Name, kind: kind, line: positions.Position(name.Pos()).Line})
	}
	return acquired
}

// markHanded records the names a call or a return hands to somebody else: the
// ownership of the resource moved, and the function that received it owes the
// cleanup.
func markHanded(handed map[string]bool, expressions []ast.Expr) {
	for _, expression := range expressions {
		if identifier, ok := expression.(*ast.Ident); ok {
			handed[identifier.Name] = true
		}
	}
}

// cleanup reports whether a chain in the function releases the named value,
// either on the value itself or on something the value owns — the body of a
// response, which is the close that matters.
func cleanup(name, suffix string, chains []string) bool {
	for _, chain := range chains {
		if strings.HasPrefix(chain, name+".") && strings.HasSuffix(chain, suffix) {
			return true
		}
	}
	return false
}

// judgeClients refuses the HTTP client without a deadline. A client with no
// timeout waits for the other side forever, which turns their slowness into this
// side's outage, and this repository builds its clients in the open.
func judgeClients(result *measured, positions *token.FileSet, path string, file *ast.File) {
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok || selectorText(literal.Type) != "http.Client" {
			return true
		}
		result.Clients++
		for _, element := range literal.Elts {
			entry, ok := element.(*ast.KeyValueExpr)
			if ok && selectorText(entry.Key) == "Timeout" {
				return true
			}
		}
		result.Findings = append(result.Findings, finding{
			Rule: RuleClientTimeout, Path: path, Line: positions.Position(literal.Pos()).Line,
			Detail: "http.Client sem Timeout: o cliente espera para sempre e a lentidão do outro lado vira indisponibilidade deste",
		})
		return true
	})
}

// errorShaped is the vocabulary of an error the gate can recognize by name: the
// declaration of the wrapping rule, and the boundary of what it can judge — an
// error inside a value with another name is not guessed.
var errorShaped = regexp.MustCompile(`^(err|errs|err[0-9]+|err[A-Z][A-Za-z0-9]*|[a-z][A-Za-z0-9]*Err|[a-z][A-Za-z0-9]*Error)$`)

// credentialShaped is the vocabulary of a secret the gate can recognize by name,
// the other half of the public message rule.
var credentialShaped = regexp.MustCompile(`(?i)(secret|token|password|passwd|apikey|api_key|credential|privatekey|private_key|dsn)$`)

// judgeWrapping refuses the error built from another error without wrapping it.
// `fmt.Errorf` with a verb and an error is how a chain is kept, and `%w` is the
// only verb that keeps it: without it the caller's `errors.Is` and `errors.As`
// stop matching, and the code that handled the failure stops working.
func judgeWrapping(result *measured, positions *token.FileSet, path string, file *ast.File) {
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || selectorText(call.Fun) != "fmt.Errorf" || len(call.Args) == 0 {
			return true
		}
		result.FormatCalls++
		format := literalText(call.Args[0])
		if format == "" || strings.Contains(format, "%w") {
			return true
		}
		for _, argument := range call.Args[1:] {
			if !isErrorValue(argument) {
				continue
			}
			result.Findings = append(result.Findings, finding{
				Rule: RuleUnwrappedError, Path: path, Line: positions.Position(call.Pos()).Line,
				Detail: fmt.Sprintf("fmt.Errorf(%q, %s) sem `%%w`: a cadeia do erro se perde e `errors.Is` do chamador deixa de casar", format, expressionText(argument)),
			})
			return true
		}
		return true
	})
}

// isErrorValue reports whether an expression is an error the gate can name: an
// identifier of the error vocabulary, or a call to `Error()`.
func isErrorValue(expression ast.Expr) bool {
	switch typed := expression.(type) {
	case *ast.Ident:
		return errorShaped.MatchString(typed.Name)
	case *ast.CallExpr:
		return strings.HasSuffix(selectorText(typed.Fun), ".Error")
	}
	return false
}

// expressionText renders an expression for the finding, so that the refusal shows
// what it read.
func expressionText(expression ast.Expr) string {
	if identifier, ok := expression.(*ast.Ident); ok {
		return identifier.Name
	}
	if chain := selectorText(expression); chain != "" {
		return chain + "()"
	}
	return "(expressão)"
}

// judgeDiscarded refuses the call whose error is thrown away. The rule judges the
// calls it can trace — the functions this module declares, which is where the
// failure of a use case or a query lands — and the report prints how many calls
// it had to leave to the gap: a method on an interface, a standard library
// function and a function held in a variable are all beyond a syntax tree.
func judgeDiscarded(result *measured, positions *token.FileSet, index *moduleIndex, path string, file *ast.File) {
	functions(file, func(function *ast.FuncDecl) {
		ast.Inspect(function.Body, func(node ast.Node) bool {
			calls := discardedCalls(node)
			for _, call := range calls {
				result.Discarded++
				answers, known := index.callAnswersWithError(path, call)
				if !known {
					continue
				}
				result.Resolved++
				if !answers {
					continue
				}
				result.Findings = append(result.Findings, finding{
					Rule: RuleDiscardedError, Path: path, Line: positions.Position(call.Pos()).Line,
					Detail: fmt.Sprintf("%s responde com erro e o resultado é descartado: trate a falha ou comente por que ela não muda nada", selectorText(call.Fun)),
				})
			}
			return true
		})
	})
}

// discardedCalls reads the two shapes of a thrown-away result: a bare call
// statement, and an assignment whose only destination is the blank identifier.
func discardedCalls(node ast.Node) []*ast.CallExpr {
	switch typed := node.(type) {
	case *ast.ExprStmt:
		if call, ok := typed.X.(*ast.CallExpr); ok {
			return []*ast.CallExpr{call}
		}
	case *ast.AssignStmt:
		if len(typed.Lhs) != 1 || len(typed.Rhs) != 1 {
			return nil
		}
		blank, ok := typed.Lhs[0].(*ast.Ident)
		if !ok || blank.Name != "_" {
			return nil
		}
		if call, ok := typed.Rhs[0].(*ast.CallExpr); ok {
			return []*ast.CallExpr{call}
		}
	}
	return nil
}

// publicMessages are the constructors whose text reaches the caller. Every one of
// them is a deliberate public message: `apperr.New` and `apperr.Newf` build the
// RFC 9457 detail the response carries, and `httperror.WriteProblem` collapses a
// foreign error into a generic problem before writing it, which is why the error
// itself is allowed there and the text is not.
var publicMessages = map[string]bool{"apperr.New": true, "apperr.Newf": true}

// judgeMessages refuses the public message that carries the internal error or a
// credential. The caller reads that text in the response, so an error inside it is
// an internal detail published, and a secret inside it is the secret published.
func judgeMessages(result *measured, positions *token.FileSet, path string, file *ast.File) {
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		constructor := selectorText(call.Fun)
		if !publicMessages[constructor] {
			return true
		}
		detail := publicDetail(call)
		if detail == nil {
			return true
		}
		result.Messages++
		if leak := leaksInto(detail); leak != "" {
			result.Findings = append(result.Findings, finding{
				Rule: RulePublicLeak, Path: path, Line: positions.Position(call.Pos()).Line,
				Detail: fmt.Sprintf("%s publica %s: a mensagem vai na resposta e o detalhe interno (ou o segredo) vaza com ela", constructor, leak),
			})
		}
		return true
	})
}

// publicDetail is the text a constructor publishes: the third argument of both
// constructors, and the arguments after it when the text is a format.
func publicDetail(call *ast.CallExpr) ast.Node {
	if len(call.Args) < 3 {
		return nil
	}
	detail := call.Args[2]
	if selectorText(call.Fun) != "apperr.Newf" {
		return detail
	}
	group := &ast.CompositeLit{Elts: []ast.Expr{detail}}
	group.Elts = append(group.Elts, call.Args[3:]...)
	return group
}

// curatedFields are the parts of an error that are public by construction. The
// domain errors of this repository carry a message and a code the client is meant
// to read — the adapter surfaces the code as the problem code and the message as
// the detail —, so publishing those two is the contract and not a leak. What the
// gate refuses is everything else an error carries: the error itself, its
// `Error()` text, its cause, its fields without a public meaning.
var curatedFields = map[string]bool{
	"Message": true, "Code": true, "Kind": true, "Status": true, "Title": true,
}

// leaksInto answers what a published text carries that must not be published, or
// "" when it carries nothing the gate can name. A selector over a curated field is
// public text: the gate stops there instead of reading the error behind it.
func leaksInto(detail ast.Node) string {
	leak := ""
	ast.Inspect(detail, func(node ast.Node) bool {
		if leak != "" {
			return false
		}
		switch typed := node.(type) {
		case *ast.SelectorExpr:
			if curatedFields[typed.Sel.Name] {
				return false
			}
		case *ast.CallExpr:
			chain := selectorText(typed.Fun)
			switch {
			case strings.HasSuffix(chain, ".Error"):
				leak = "a mensagem do erro (" + chain + "())"
				return false
			case strings.HasSuffix(chain, ".Unredacted"):
				leak = "o valor não redigido de um segredo"
				return false
			}
		case *ast.Ident:
			switch {
			case errorShaped.MatchString(typed.Name):
				leak = "o erro " + typed.Name
			case credentialShaped.MatchString(typed.Name):
				leak = "a credencial " + typed.Name
			}
		}
		return true
	})
	return leak
}

// judgeGoroutines refuses the goroutine that names no owner. A goroutine can only
// be waited for, stopped or cancelled through something the statement points at:
// the context it took, the channel it reports on, the wait group that counts it,
// the value it serves, or the receiver of a method on a type that declares how to
// stop it. A `go` statement that names none of those is a worker nobody can shut
// down, and no test can prove it ever finished.
func judgeGoroutines(result *measured, positions *token.FileSet, index *moduleIndex, path string, file *ast.File) {
	functions(file, func(function *ast.FuncDecl) {
		owners := localOwners(function)
		ast.Inspect(function.Body, func(node ast.Node) bool {
			statement, ok := node.(*ast.GoStmt)
			if !ok {
				return true
			}
			result.Goroutines++
			if namesOwner(statement.Call, index, path, owners) {
				return true
			}
			result.Findings = append(result.Findings, finding{
				Rule: RuleGoroutine, Path: path, Line: positions.Position(statement.Pos()).Line,
				Detail: fmt.Sprintf("go %s não nomeia dono: receba um contexto, reporte num canal, conte num WaitGroup, feche o que serve ou chame um método de um tipo que saiba parar",
					selectorText(statement.Call.Fun)),
			})
			return true
		})
	})
}

// localOwners maps the values a function declares with a composite literal to the
// type they carry, so that a goroutine naming one of them names an owner whenever
// that type declares how to stop.
func localOwners(function *ast.FuncDecl) map[string]string {
	owners := map[string]string{}
	ast.Inspect(function.Body, func(node ast.Node) bool {
		assignment, ok := node.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for index, right := range assignment.Rhs {
			if index >= len(assignment.Lhs) {
				continue
			}
			name, ok := assignment.Lhs[index].(*ast.Ident)
			if !ok {
				continue
			}
			if typeName := compositeTypeName(right); typeName != "" {
				owners[name.Name] = typeName
			}
		}
		return true
	})
	return owners
}

// compositeTypeName reads the type a value is built with: `&T{...}`, `T{...}` and
// `new(T)` are the three declarations that carry a type in plain sight.
func compositeTypeName(expression ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.CompositeLit:
		return lastSegment(selectorText(typed.Type))
	case *ast.UnaryExpr:
		if typed.Op == token.AND {
			return compositeTypeName(typed.X)
		}
	case *ast.CallExpr:
		if selectorText(typed.Fun) == "new" && len(typed.Args) == 1 {
			return lastSegment(selectorText(typed.Args[0]))
		}
	}
	return ""
}

// namesOwner reports whether the started call names the thing that owns it.
func namesOwner(call *ast.CallExpr, index *moduleIndex, path string, owners map[string]string) bool {
	chain := selectorText(call.Fun)
	lowered := strings.ToLower(chain)
	if strings.Contains(lowered, "ctx") || strings.Contains(lowered, "context") {
		return true
	}
	name := lastSegment(chain)
	if index.methodBelongsToOwner(path, name) || index.closesWhatItWasGiven(path, name) {
		return true
	}
	return signalsOwnership(call, index, path, owners)
}

// signalsOwnership walks the started call for the signals a syntax tree can read:
// a context named anywhere in it, a `select`, an operation on a channel — a send,
// a receive, or the builtin `close`, which is only legal on one —, a call to one of
// the methods that coordinate goroutines, and a value whose declared type knows how
// to stop.
func signalsOwnership(node ast.Node, index *moduleIndex, path string, owners map[string]string) bool {
	found := false
	ast.Inspect(node, func(candidate ast.Node) bool {
		if found {
			return false
		}
		switch typed := candidate.(type) {
		case *ast.SelectStmt:
			found = true
		case *ast.SendStmt:
			found = true
		case *ast.UnaryExpr:
			found = typed.Op == token.ARROW
		case *ast.CallExpr:
			found = namesOwnerInCall(typed, index, path, owners)
		case *ast.Ident:
			found = namesOwnerInIdent(typed, index, path, owners)
		}
		return true
	})
	return found
}

// namesOwnerInCall reads the two signals a call carries: the coordination methods
// and a context named in the call itself.
func namesOwnerInCall(call *ast.CallExpr, index *moduleIndex, path string, owners map[string]string) bool {
	chain := selectorText(call.Fun)
	if chain == "close" || isCoordinationCall(chain) {
		return true
	}
	lowered := strings.ToLower(chain)
	if strings.Contains(lowered, "ctx") || strings.Contains(lowered, "context") {
		return true
	}
	for _, argument := range call.Args {
		identifier, ok := argument.(*ast.Ident)
		if ok && namesOwnerInIdent(identifier, index, path, owners) {
			return true
		}
	}
	return false
}

// namesOwnerInIdent reads the two signals an identifier carries: it is a context,
// or it holds a value whose type knows how to stop.
func namesOwnerInIdent(identifier *ast.Ident, index *moduleIndex, path string, owners map[string]string) bool {
	lowered := strings.ToLower(identifier.Name)
	if lowered == "ctx" || strings.HasPrefix(lowered, "ctx") || strings.Contains(lowered, "context") {
		return true
	}
	typeName, ok := owners[identifier.Name]
	return ok && index.typeOwns(path, typeName)
}

// coordinationMethods are the calls that make a goroutine accountable to somebody.
var coordinationMethods = map[string]bool{
	"Done": true, "Add": true, "Wait": true, "Stop": true, "Close": true, "Shutdown": true, "Join": true,
}

func isCoordinationCall(chain string) bool {
	return coordinationMethods[lastSegment(chain)]
}

func lastSegment(chain string) string {
	if index := strings.LastIndex(chain, "."); index >= 0 {
		return chain[index+1:]
	}
	return chain
}

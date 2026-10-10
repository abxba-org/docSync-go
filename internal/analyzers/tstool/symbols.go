// Ported from packages/analyzer-typescript/src/symbols.ts (F11-1 / P11;
// V-P11-2 groundwork) and, for the extraction engine, from src/parser.ts
// (replaced — plan.md §6 DEV-1).
//
// PURE: ExtractSymbols touches no fs/network/git/clock and is deterministic for
// identical input — the same source string always yields a deep-equal
// SymbolTable. The plugin-level purity contract (ARCH §4.1, core plugin.ts) is
// satisfied: callers own all I/O.
//
// Coverage model (this unit): TOP-LEVEL declarations plus CLASS-MEMBER and
// INTERFACE-MEMBER declarations only. A declaration is kept when every scope it
// sits in is one of: program, export statement, class body, interface body.
// Anything nested deeper (e.g. a function inside a method body, or a
// declaration inside a function/block/namespace) is OUT of scope and is not
// reported.
//
// Known query-coverage gaps (declared, not silent — identical to the tree-sitter
// query this port replaces, so the classification output is unchanged):
//   - re-exports, destructured/renamed exports, `declare` ambient blocks and
//     namespaces are not matched;
//   - variable_declarator patterns use only identifier names (no object/array
//     destructuring targets);
//   - `export default` declarations are matched as plain declarations (their
//     `exported` flag is false unless wrapped in an export statement);
//   - `enum` bodies, computed names, string/number-named members and the
//     tree-sitter `private_field_definition` (`#name`) form are not matched.
//
// DEVIATION (plan.md §6 DEV-1): the TypeScript implementation runs the same
// query over a web-tree-sitter (WASM grammar) parse tree. Go has no tree-sitter
// binding without CGO, so this file implements an EQUIVALENT lightweight
// TypeScript/TSX scanner: a lexer (strings, template literals with nested
// substitutions, comments, regex literals, numbers, identifiers, punctuation)
// plus a statement/member scanner that recognizes exactly the declaration forms
// the tree-sitter query captures. Every value it reports — name, kind, 1-based
// line, whitespace-collapsed signature with the body elided exactly where
// tree-sitter's `body` field starts, the `exported` flag and the
// colon-included type annotations — is produced with tree-sitter's semantics.
// The parser NEVER invents a symbol: a construct it does not recognize yields
// no symbol, which is the same fail-quiet direction as the tree-sitter query's
// own coverage model (under-report, never fabricate) — and the analyzer declares
// its per-file coverage either way (D14).

package tstool

import (
	"strings"
	"unicode"
)

// SymbolKind is the kind of a reported symbol (port of SymbolKind; the same
// 8-value taxonomy the tree-sitter query maps onto).
type SymbolKind string

// Symbol kinds in the TS union order.
const (
	SymbolKindFunction  SymbolKind = "function"
	SymbolKindClass     SymbolKind = "class"
	SymbolKindInterface SymbolKind = "interface"
	SymbolKindTypeAlias SymbolKind = "type_alias"
	SymbolKindEnum      SymbolKind = "enum"
	SymbolKindMethod    SymbolKind = "method"
	SymbolKindProperty  SymbolKind = "property"
	SymbolKindVariable  SymbolKind = "variable"
)

// SymbolKindValues lists every symbol kind in canonical order.
var SymbolKindValues = []SymbolKind{
	SymbolKindFunction, SymbolKindClass, SymbolKindInterface, SymbolKindTypeAlias,
	SymbolKindEnum, SymbolKindMethod, SymbolKindProperty, SymbolKindVariable,
}

// ExtractedSymbol is one extracted symbol, in source order (port of
// ExtractedSymbol).
type ExtractedSymbol struct {
	Name string     `json:"name"`
	Kind SymbolKind `json:"kind"`
	// Line is the 1-based line of the declaration's start.
	Line int `json:"line"`
	// Signature is the single-line declaration text (body elided when the
	// declaration has one; whitespace collapsed).
	Signature string `json:"signature"`
	// Exported is true when the declaration sits under an export statement.
	Exported bool `json:"exported"`
	// TypeAnnotations are the captured annotations as written, colon included
	// (e.g. ": string"), in field order; present only when at least one exists.
	TypeAnnotations []string `json:"typeAnnotations,omitempty"`
}

// SymbolTable is the full result for one source blob: all symbols in source
// order (port of SymbolTable).
type SymbolTable struct {
	Symbols []ExtractedSymbol `json:"symbols"`
}

// ExtractSymbols extracts the symbol table for a TypeScript/TSX source string.
// Deterministic and pure: same source in, deep-equal SymbolTable out (asserted
// by the package test suite). This unit does NOT wire the AnalyzerPlugin
// contract (analyzer.go does); it is the extraction layer only.
func ExtractSymbols(source string) (SymbolTable, error) {
	parser := newTSParser(source)
	return SymbolTable{Symbols: parser.parse()}, nil
}

// TypeScriptParserError is the typed error for any parser-availability failure
// (the port of parser.ts's TypeScriptParserError; the WASM-runtime and
// grammar-load variants do not exist in the Go port — DEV-1). It carries no
// docutil code because the TS class carried none: the parser gate never rejects
// in the Go port either, it only RECORDS the failure and degrades to the
// declared stub shape (D14).
type TypeScriptParserError struct {
	Msg string
}

// Error renders the message (the TS Error#message, preserved verbatim).
func (e *TypeScriptParserError) Error() string { return e.Msg }

// NewTypeScriptParserError builds a TypeScriptParserError.
func NewTypeScriptParserError(message string) *TypeScriptParserError {
	return &TypeScriptParserError{Msg: message}
}

/* ------------------------------------------------------------ the scanner */

// scopeKind is one scope in the structural chain above a declaration. The
// IN_SCOPE set is exactly the tree-sitter IN_SCOPE_ANCESTORS list that keeps a
// declaration in the top-level/class-member scope.
type scopeKind uint8

const (
	scopeProgram scopeKind = iota
	scopeExport
	scopeClassBody
	scopeInterfaceBody
	// scopeOther is everything else (statement blocks, function bodies, object
	// literals, namespaces, ambient declarations): declarations inside it are
	// out of scope.
	scopeOther
)

func (s scopeKind) inScope() bool {
	switch s {
	case scopeProgram, scopeExport, scopeClassBody, scopeInterfaceBody:
		return true
	default:
		return false
	}
}

// tokenKind is the lexical class of one token.
type tokenKind uint8

const (
	tokenEOF tokenKind = iota
	tokenIdent
	tokenString
	tokenTemplate
	tokenNumber
	tokenPunct
	tokenRegex
)

// token is one lexed token with byte offsets (signatures are sliced from the
// raw source, never reassembled from tokens).
type token struct {
	kind          tokenKind
	text          string
	start         int
	end           int
	line          int
	newlineBefore bool
}

// multiCharPuncts are the multi-character operators the lexer recognizes
// (longest first). `<` and `>` are deliberately NOT combined so that nested
// generics (`Array<Array<string>>`) close level by level; `=>` IS, so an arrow
// type inside a type-parameter list never looks like a closing angle bracket.
var multiCharPuncts = []string{
	"...", "===", "!==",
	"=>", "==", "!=", "<=", ">=", "&&", "||", "??", "?.", "++", "--",
	"+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "**",
}

// statementKeywords starts a new statement — the ASI heuristic boundary.
var statementKeywords = map[string]bool{
	"export": true, "import": true, "function": true, "async": true,
	"class": true, "abstract": true, "interface": true, "type": true,
	"enum": true, "const": true, "let": true, "var": true, "declare": true,
	"namespace": true, "module": true, "return": true, "if": true,
	"for": true, "while": true, "switch": true, "try": true, "throw": true,
	"new": true, "delete": true, "await": true, "yield": true, "case": true,
	"break": true, "continue": true, "default": true,
}

// classMemberModifiers precede a class member name.
var classMemberModifiers = map[string]bool{
	"public": true, "private": true, "protected": true, "readonly": true,
	"static": true, "abstract": true, "declare": true, "override": true,
	"async": true, "accessor": true,
}

// keywords that can never BE a member name in the shapes we recognize.
var nonNameKeywords = map[string]bool{
	"new": true, "typeof": true, "keyof": true, "void": true, "null": true,
	"true": true, "false": true, "this": true, "super": true, "function": true,
	"class": true, "interface": true, "enum": true, "extends": true,
	"implements": true, "package": true,
}

// tsParser is the lightweight statement scanner.
type tsParser struct {
	src     string
	toks    []token
	pos     int
	scopes  []scopeKind
	symbols []ExtractedSymbol
}

// declSpec is one pending symbol: the declaration node span, its name/kind and
// the byte offset where tree-sitter's `body` field starts (-1 when none).
type declSpec struct {
	startTok    token
	name        string
	kind        SymbolKind
	end         int
	bodyStart   int
	annotations []string
}

func newTSParser(src string) *tsParser {
	p := &tsParser{src: src, toks: lexTypeScript(src), scopes: []scopeKind{scopeProgram}}
	return p
}

// parse walks the source and returns every in-scope symbol in source order.
func (p *tsParser) parse() []ExtractedSymbol {
	p.symbols = nil
	p.parseStatements(scopeProgram)
	if p.symbols == nil {
		p.symbols = []ExtractedSymbol{}
	}
	return p.symbols
}

/* --------------------------------------------------------------- scanner */

func (p *tsParser) pushScope(kind scopeKind) { p.scopes = append(p.scopes, kind) }
func (p *tsParser) popScope()                { p.scopes = p.scopes[:len(p.scopes)-1] }

// inScope reports whether every scope above the declaration is an allowed
// ancestor (the port of isInScope).
func (p *tsParser) inScope() bool {
	for _, kind := range p.scopes {
		if !kind.inScope() {
			return false
		}
	}
	return true
}

// exportedHere reports whether an export statement sits directly above the
// declaration before a program/class-body/interface-body boundary (the port of
// isExported).
func (p *tsParser) exportedHere() bool {
	for i := len(p.scopes) - 1; i >= 0; i-- {
		switch p.scopes[i] {
		case scopeExport:
			return true
		case scopeProgram, scopeClassBody, scopeInterfaceBody:
			return false
		}
	}
	return false
}

// record emits a symbol when its declaration is in scope.
func (p *tsParser) record(spec declSpec) {
	if !p.inScope() {
		return
	}
	end := spec.end
	if spec.bodyStart >= 0 && spec.bodyStart < end {
		end = spec.bodyStart
	}
	symbol := ExtractedSymbol{
		Name:      spec.name,
		Kind:      spec.kind,
		Line:      spec.startTok.line,
		Signature: collapseWhitespace(p.src[spec.startTok.start:end]),
		Exported:  p.exportedHere(),
	}
	if len(spec.annotations) > 0 {
		symbol.TypeAnnotations = spec.annotations
	}
	p.symbols = append(p.symbols, symbol)
}

func (p *tsParser) tokAt(i int) token {
	if i < 0 || i >= len(p.toks) {
		return token{kind: tokenEOF}
	}
	return p.toks[i]
}

func (p *tsParser) peek() token { return p.tokAt(p.pos) }

func (p *tsParser) next() token {
	t := p.peek()
	if p.pos < len(p.toks) {
		p.pos++
	}
	return t
}

func (p *tsParser) atEOF() bool { return p.pos >= len(p.toks) }

func (p *tsParser) lastEnd() int {
	if p.pos == 0 {
		return 0
	}
	return p.toks[p.pos-1].end
}

func (p *tsParser) peekPunct(text string) bool {
	t := p.peek()
	return t.kind == tokenPunct && t.text == text
}

// startsStatement reports whether t begins a new statement (ASI boundary).
func (p *tsParser) startsStatement(t token) bool {
	if t.kind == tokenIdent {
		return statementKeywords[t.text]
	}
	return t.kind == tokenPunct && t.text == "@"
}

// skipDecorators consumes any run of decorators (`@name`, `@a.b.c`, `@dec(...)`).
func (p *tsParser) skipDecorators() {
	for p.peekPunct("@") {
		p.skipDecorator()
	}
}

// skipDecorator consumes one decorator.
func (p *tsParser) skipDecorator() {
	if !p.peekPunct("@") {
		return
	}
	p.next()
	for {
		t := p.peek()
		if t.kind == tokenIdent || (t.kind == tokenPunct && t.text == ".") {
			p.next()
			continue
		}
		break
	}
	if p.peekPunct("(") {
		p.skipBalanced("(", ")")
	}
}

// parseStatements parses a statement/member list in the given scope. It returns
// before consuming the closing `}` of a braced scope (or at end of input),
// leaving it to the caller.
func (p *tsParser) parseStatements(kind scopeKind) {
	p.pushScope(kind)
	defer p.popScope()
	for {
		for p.peekPunct(";") || (kind == scopeInterfaceBody && p.peekPunct(",")) {
			p.next()
		}
		if p.atEOF() {
			return
		}
		if p.peekPunct("}") {
			// A braced scope leaves its closer to the caller. At the top level a
			// stray closer is RECOVERED FROM (tree-sitter emits an ERROR node and
			// keeps parsing) — the before/after reconstruction of a hunk window
			// is a source fragment, so unbalanced closers are expected there.
			if kind != scopeProgram {
				return
			}
			p.next()
			continue
		}
		before := p.pos
		switch kind {
		case scopeClassBody:
			p.parseMember()
		case scopeInterfaceBody:
			p.parseInterfaceMember()
		default:
			p.parseStatement()
		}
		if p.pos == before {
			p.next() // never spin on an unconsumed token
		}
	}
}

func (p *tsParser) parseStatement() {
	p.skipDecorators()
	t := p.peek()
	if t.kind == tokenPunct && t.text == "{" {
		// Block statement (or an object-literal expression): out of scope.
		p.next()
		p.parseStatements(scopeOther)
		p.expectPunct("}")
		return
	}
	if t.kind != tokenIdent {
		p.skipStatement()
		return
	}
	switch t.text {
	case "export":
		p.parseExport()
	case "declare":
		// Ambient declarations are an `ambient_declaration` node in tree-sitter,
		// i.e. NOT an in-scope ancestor: parse them, match nothing.
		p.next()
		p.pushScope(scopeOther)
		p.parseStatement()
		p.popScope()
	case "import":
		p.skipStatement()
	case "async", "function":
		p.parseFunctionDecl()
	case "abstract", "class":
		p.parseClassDecl()
	case "interface":
		p.parseInterfaceDecl()
	case "type":
		if n := p.tokAt(p.pos + 1); n.kind == tokenIdent && !nonNameKeywords[n.text] {
			if after := p.tokAt(p.pos + 2); after.kind == tokenPunct && (after.text == "=" || after.text == "<") {
				p.parseTypeAlias()
				return
			}
		}
		p.skipStatement()
	case "enum":
		p.parseEnum()
	case "const", "let", "var":
		if t.text == "const" {
			if n := p.tokAt(p.pos + 1); n.kind == tokenIdent && n.text == "enum" {
				p.parseEnum()
				return
			}
		}
		p.parseVariableDecl()
	case "namespace", "module":
		p.parseNamespace()
	default:
		p.skipStatement()
	}
}

// parseExport handles `export` and its forms. Re-exports (`export * …`,
// `export { … } from …`, `export type { … } …`, `export = …`) match nothing.
func (p *tsParser) parseExport() {
	p.next() // export
	t := p.peek()
	if t.kind == tokenIdent && t.text == "default" {
		p.next()
		p.pushScope(scopeExport)
		p.parseStatement()
		p.popScope()
		return
	}
	if t.kind == tokenPunct && (t.text == "*" || t.text == "{") {
		p.skipStatement() // export * … / export { … } from …
		return
	}
	if t.kind == tokenIdent && t.text == "type" && p.tokAt(p.pos+1).text == "{" {
		p.skipStatement() // export type { … } from — type-only re-export
		return
	}
	if t.kind == tokenIdent && t.text == "=" {
		p.skipStatement() // export = <expression> (TS export-assignment)
		return
	}
	p.pushScope(scopeExport)
	p.parseStatement()
	p.popScope()
}

func (p *tsParser) parseFunctionDecl() {
	startTok := p.peek()
	if p.peek().text == "async" {
		p.next()
	}
	if p.peek().text != "function" {
		p.skipStatement()
		return
	}
	p.next()
	if p.peekPunct("*") {
		p.next()
	}
	if p.peek().kind != tokenIdent {
		p.skipStatement()
		return
	}
	name := p.next().text
	if p.peekPunct("<") {
		p.skipAngle()
	}
	if !p.peekPunct("(") {
		p.skipStatement()
		return
	}
	p.skipBalanced("(", ")")
	var annotations []string
	if p.peekPunct(":") {
		annotations = p.typeAnnotation()
	}
	bodyStart := -1
	end := p.lastEnd()
	if p.peekPunct("{") {
		bodyStart = p.peek().start
		p.skipBalanced("{", "}")
		end = p.lastEnd()
	} else if p.peekPunct(";") {
		p.next()
		end = p.lastEnd()
	}
	p.record(declSpec{startTok: startTok, name: name, kind: SymbolKindFunction, end: end, bodyStart: bodyStart, annotations: annotations})
}

func (p *tsParser) parseClassDecl() {
	startTok := p.peek()
	if p.peek().text == "abstract" {
		p.next()
	}
	if p.peek().text != "class" {
		p.skipStatement()
		return
	}
	p.next()
	name := ""
	// A nameless class (`export default class extends Base {}`) reports nothing.
	if p.peek().kind == tokenIdent && p.peek().text != "extends" {
		name = p.next().text
	}
	// Heritage clauses (`extends X<T> implements Y`) end at the class body.
	for !p.atEOF() && !p.peekPunct("{") && !p.peekPunct(";") {
		if p.peekPunct("<") {
			p.skipAngle()
			continue
		}
		p.next()
	}
	if !p.peekPunct("{") {
		p.skipStatement()
		return
	}
	bodyStart := p.peek().start
	if name != "" {
		p.record(declSpec{startTok: startTok, name: name, kind: SymbolKindClass, end: bodyStart, bodyStart: bodyStart})
	}
	p.next()
	p.parseStatements(scopeClassBody)
	p.expectPunct("}")
}

func (p *tsParser) parseInterfaceDecl() {
	startTok := p.peek()
	if p.peek().text != "interface" {
		p.skipStatement()
		return
	}
	p.next()
	if p.peek().kind != tokenIdent {
		p.skipStatement()
		return
	}
	name := p.next().text
	if p.peekPunct("<") {
		p.skipAngle()
	}
	for !p.atEOF() && !p.peekPunct("{") && !p.peekPunct(";") {
		if p.peekPunct("<") {
			p.skipAngle()
			continue
		}
		p.next()
	}
	if !p.peekPunct("{") {
		p.skipStatement()
		return
	}
	bodyStart := p.peek().start
	p.record(declSpec{startTok: startTok, name: name, kind: SymbolKindInterface, end: bodyStart, bodyStart: bodyStart})
	p.next()
	p.parseStatements(scopeInterfaceBody)
	p.expectPunct("}")
}

func (p *tsParser) parseTypeAlias() {
	startTok := p.peek()
	p.next() // type
	nameTok := p.peek()
	if nameTok.kind != tokenIdent {
		p.skipStatement()
		return
	}
	p.next()
	if p.peekPunct("<") {
		p.skipAngle()
	}
	if !p.peekPunct("=") {
		p.skipStatement()
		return
	}
	p.next()
	p.skipType()
	end := p.lastEnd()
	if p.peekPunct(";") {
		p.next()
		end = p.lastEnd()
	}
	// A type alias has no `body` field in tree-sitter: the node text runs to
	// the terminating semicolon, which is therefore part of the signature.
	p.record(declSpec{startTok: startTok, name: nameTok.text, kind: SymbolKindTypeAlias, end: end, bodyStart: -1})
}

func (p *tsParser) parseEnum() {
	startTok := p.peek()
	if p.peek().text == "const" {
		p.next()
	}
	if p.peek().text != "enum" {
		p.skipStatement()
		return
	}
	p.next()
	name := ""
	if p.peek().kind == tokenIdent {
		name = p.next().text
	}
	if !p.peekPunct("{") {
		p.skipStatement()
		return
	}
	bodyStart := p.peek().start
	if name != "" {
		p.record(declSpec{startTok: startTok, name: name, kind: SymbolKindEnum, end: bodyStart, bodyStart: bodyStart})
	}
	p.skipBalanced("{", "}")
}

// parseVariableDecl parses a lexical/variable declaration. tree-sitter captures
// ONE match per identifier declarator, each with @decl = the WHOLE declaration
// node, so every recorded symbol carries the same (whole-statement) signature.
func (p *tsParser) parseVariableDecl() {
	startTok := p.peek()
	p.next() // const | let | var
	type declarator struct {
		name        string
		kind        SymbolKind
		annotations []string
	}
	var declarators []declarator
	for {
		t := p.peek()
		if nonNameKeywords[t.text] {
			p.skipStatement()
			return
		}
		if t.kind == tokenIdent {
			p.next()
			var annotations []string
			if p.peekPunct(":") {
				annotations = p.typeAnnotation()
			}
			kind := SymbolKindVariable
			if p.peekPunct("=") {
				p.next()
				if p.isFunctionInitializer() {
					kind = SymbolKindFunction
				}
				p.skipToDeclaratorEnd()
			}
			declarators = append(declarators, declarator{name: t.text, kind: kind, annotations: annotations})
		} else {
			// Destructuring targets are not `identifier` names → not matched.
			if p.peekPunct("{") {
				p.skipBalanced("{", "}")
			} else if p.peekPunct("[") {
				p.skipBalanced("[", "]")
			}
			if p.peekPunct(":") {
				p.next()
				p.skipType()
			}
			if p.peekPunct("=") {
				p.next()
				p.skipToDeclaratorEnd()
			}
		}
		if p.peekPunct(",") {
			p.next()
			continue
		}
		break
	}
	end := p.lastEnd()
	if p.peekPunct(";") {
		p.next()
		end = p.lastEnd()
	}
	for _, d := range declarators {
		p.record(declSpec{startTok: startTok, name: d.name, kind: d.kind, end: end, bodyStart: -1, annotations: d.annotations})
	}
}

// parseNamespace parses a namespace/module block: its body is an
// `statement_block` node in tree-sitter, so declarations inside it are out of
// scope.
func (p *tsParser) parseNamespace() {
	p.next() // namespace | module
	if p.peek().kind == tokenString {
		p.skipStatement()
		return
	}
	for p.peek().kind == tokenIdent || p.peekPunct(".") {
		p.next()
	}
	if !p.peekPunct("{") {
		p.skipStatement()
		return
	}
	p.next()
	p.parseStatements(scopeOther)
	p.expectPunct("}")
}

/* ----------------------------------------------------- class/interface members */

// parseMember parses one class member. It is called with the position at the
// member start (decorators included). Decorators are NOT part of the signature
// text: the TS query's @decl capture starts at the declaration keyword, and
// treating decorator text as signature evidence would fire a spurious
// 'signature' change when only a metadata decorator changes (D14: ambiguity
// degrades toward less automation, never more — declared here).
func (p *tsParser) parseMember() {
	p.skipDecorators()
	startTok := p.peek()
	modStart := p.pos
	for {
		t := p.peek()
		if t.kind == tokenIdent && classMemberModifiers[t.text] {
			p.next()
			continue
		}
		break
	}
	// A modifier keyword that is really the member name (`static = 1`,
	// `private static = 1`): rewind one token so the name is the identifier.
	switch p.peek().text {
	case "=", ":", ";", "}", "!", "?", ",":
		if p.pos > modStart {
			p.pos--
		}
	}
	if p.peekPunct("*") {
		p.next() // generator method
	}
	if t := p.peek(); t.text == "get" || t.text == "set" {
		n := p.tokAt(p.pos + 1)
		if n.kind == tokenIdent && !nonNameKeywords[n.text] {
			p.next() // accessor
		}
	}
	nameTok := p.peek()
	if nameTok.kind != tokenIdent || nonNameKeywords[nameTok.text] {
		// Computed (`[expr]`), string- and number-named members, and keywords
		// such as `new` are not `property_identifier`s → not matched.
		p.skipMember()
		return
	}
	p.next()
	if p.peekPunct("?") || p.peekPunct("!") {
		p.next()
	}
	if p.peekPunct("<") {
		p.skipAngle()
	}
	if p.peekPunct("(") {
		p.skipBalanced("(", ")")
		var annotations []string
		if p.peekPunct(":") {
			annotations = p.typeAnnotation()
		}
		bodyStart := -1
		end := p.lastEnd()
		if p.peekPunct("{") {
			bodyStart = p.peek().start
			p.skipBalanced("{", "}")
			end = p.lastEnd()
		}
		p.record(declSpec{startTok: startTok, name: nameTok.text, kind: SymbolKindMethod, end: end, bodyStart: bodyStart, annotations: annotations})
		return
	}
	// public_field_definition: no body, and tree-sitter's node text ends before
	// the member-terminating semicolon.
	var annotations []string
	if p.peekPunct(":") {
		annotations = p.typeAnnotation()
	}
	end := p.lastEnd()
	if p.peekPunct("=") {
		p.next()
		p.skipToDeclaratorEnd()
		end = p.lastEnd()
	}
	p.record(declSpec{startTok: startTok, name: nameTok.text, kind: SymbolKindProperty, end: end, bodyStart: -1, annotations: annotations})
}

// parseInterfaceMember parses one interface member (method_signature or
// property_signature — interface members have no bodies). Decorators are not
// part of the signature text (see parseMember).
func (p *tsParser) parseInterfaceMember() {
	p.skipDecorators()
	startTok := p.peek()
	modStart := p.pos
	if p.peek().text == "readonly" {
		p.next()
	}
	switch p.peek().text {
	case "=", ":", ";", "}", "!", "?", ",":
		if p.pos > modStart {
			p.pos--
		}
	}
	if t := p.peek(); t.text == "get" || t.text == "set" {
		n := p.tokAt(p.pos + 1)
		if n.kind == tokenIdent && !nonNameKeywords[n.text] {
			p.next()
		}
	}
	nameTok := p.peek()
	if nameTok.kind != tokenIdent || nonNameKeywords[nameTok.text] {
		p.skipMember()
		return
	}
	p.next()
	if p.peekPunct("?") {
		p.next()
	}
	if p.peekPunct("(") {
		p.skipBalanced("(", ")")
		var annotations []string
		if p.peekPunct(":") {
			annotations = p.typeAnnotation()
		}
		p.record(declSpec{startTok: startTok, name: nameTok.text, kind: SymbolKindMethod, end: p.lastEnd(), bodyStart: -1, annotations: annotations})
		return
	}
	var annotations []string
	if p.peekPunct(":") {
		annotations = p.typeAnnotation()
	}
	p.record(declSpec{startTok: startTok, name: nameTok.text, kind: SymbolKindProperty, end: p.lastEnd(), bodyStart: -1, annotations: annotations})
}

// skipMember consumes the rest of an unrecognized member (index signatures,
// call signatures, computed names, …).
func (p *tsParser) skipMember() {
	depth := 0
	for !p.atEOF() {
		t := p.peek()
		if t.kind == tokenPunct {
			switch t.text {
			case "(", "[", "{":
				depth++
				p.next()
				continue
			case ")", "]", "}":
				if depth == 0 {
					return
				}
				depth--
				p.next()
				continue
			case ";", ",":
				if depth == 0 {
					p.next()
					return
				}
			}
		}
		if depth == 0 && t.newlineBefore {
			return // ASI inside a member list
		}
		p.next()
	}
}

/* ------------------------------------------------------------- skipping */

// skipStatement consumes an uninteresting statement up to its terminator
// (`;` at depth 0), the end of the enclosing block, or an ASI boundary.
func (p *tsParser) skipStatement() {
	depth := 0
	for !p.atEOF() {
		t := p.peek()
		if t.kind == tokenPunct {
			switch t.text {
			case "(", "[", "{":
				depth++
				p.next()
				continue
			case ")", "]", "}":
				if depth == 0 {
					return
				}
				depth--
				p.next()
				continue
			case ";":
				if depth == 0 {
					p.next()
					return
				}
			}
		}
		if depth == 0 && t.newlineBefore && p.startsStatement(t) {
			return
		}
		p.next()
	}
}

// skipToDeclaratorEnd consumes an initializer up to the `,`/`;` that ends the
// declarator (or an ASI boundary), leaving the terminator unconsumed.
func (p *tsParser) skipToDeclaratorEnd() {
	depth := 0
	for !p.atEOF() {
		t := p.peek()
		if t.kind == tokenPunct {
			switch t.text {
			case "(", "[", "{":
				depth++
				p.next()
				continue
			case ")", "]", "}":
				if depth == 0 {
					return
				}
				depth--
				p.next()
				continue
			case ",", ";":
				if depth == 0 {
					return
				}
			}
		}
		if depth == 0 && t.newlineBefore && p.startsStatement(t) {
			return
		}
		p.next()
	}
}

// skipBalanced consumes a balanced bracket pair.
func (p *tsParser) skipBalanced(open, close string) {
	if !p.peekPunct(open) {
		return
	}
	p.next()
	depth := 1
	for !p.atEOF() && depth > 0 {
		t := p.next()
		if t.kind == tokenPunct {
			switch t.text {
			case open:
				depth++
			case close:
				depth--
			}
		}
	}
}

// skipAngle consumes a balanced `<…>` type-parameter/argument list.
func (p *tsParser) skipAngle() {
	if !p.peekPunct("<") {
		return
	}
	p.next()
	depth := 1
	for !p.atEOF() && depth > 0 {
		t := p.next()
		if t.kind != tokenPunct {
			continue
		}
		switch t.text {
		case "<":
			depth++
		case ">":
			depth--
		case "{":
			p.skipBalanced("{", "}")
		case ";":
			if depth == 0 {
				return
			}
		}
	}
}

// typeAnnotation consumes `:` plus the following type expression and returns
// the annotation text AS WRITTEN, colon included (the tree-sitter `type` /
// `return_type` node text).
func (p *tsParser) typeAnnotation() []string {
	if !p.peekPunct(":") {
		return nil
	}
	start := p.peek().start
	p.next()
	p.skipType()
	return []string{collapseWhitespace(p.src[start:p.lastEnd()])}
}

// skipType consumes one (shallow) type expression: it stops at the first token
// that cannot continue a type at depth 0.
func (p *tsParser) skipType() {
	p.skipTypeAtom()
	for {
		t := p.peek()
		if t.kind == tokenPunct {
			switch t.text {
			case "|", "&", ".", "?", ":":
				p.next()
				p.skipTypeAtom()
				continue
			case "[":
				p.skipBalanced("[", "]") // tuple / array type
				continue
			case "<":
				p.skipAngle()
				continue
			}
			return
		}
		if t.kind == tokenIdent {
			switch t.text {
			case "extends", "is", "asserts", "infer", "keyof", "typeof", "readonly":
				p.next()
				p.skipTypeAtom()
				continue
			}
		}
		return
	}
}

// skipTypeAtom consumes ONE type atom: a function/parenthesized type, an object
// type literal, a type-parameter list, a tuple/array type, or a type reference
// or literal type.
func (p *tsParser) skipTypeAtom() {
	if p.atEOF() {
		return
	}
	switch {
	case p.peekPunct("("):
		p.skipBalanced("(", ")")
		if p.peekPunct("=>") {
			p.next()
			p.skipType()
		}
		return
	case p.peekPunct("{"):
		p.skipBalanced("{", "}") // object type literal
		return
	case p.peekPunct("<"):
		p.skipAngle()
		return
	case p.peekPunct("["):
		p.skipBalanced("[", "]") // tuple / array type
		return
	}
	t := p.peek()
	if t.kind == tokenIdent || t.kind == tokenString || t.kind == tokenNumber || t.kind == tokenTemplate {
		// A type atom can legitimately be named after any keyword-ish identifier
		// (`null`, `void`, `this`, `undefined`, …), so no keyword filter here.
		p.next()
	}
}

// isFunctionInitializer reports whether the initializer at the cursor is a
// function expression or an arrow function (tree-sitter classifies those
// declarators as 'function', everything else as 'variable').
func (p *tsParser) isFunctionInitializer() bool {
	t := p.peek()
	switch {
	case t.text == "function":
		return true
	case t.text == "async":
		n := p.tokAt(p.pos + 1)
		if n.text == "function" {
			return true
		}
		return p.arrowAhead(p.pos + 1)
	case t.text == "(":
		return p.arrowAhead(p.pos)
	case t.text == "<":
		// A generic arrow: skip the balanced type-parameter list, then expect
		// `(…) => …` or `x => …`.
		depth := 1
		index := p.pos + 1
		for ; index < len(p.toks) && depth > 0; index++ {
			next := p.toks[index]
			if next.kind != tokenPunct {
				continue
			}
			switch next.text {
			case "<":
				depth++
			case ">":
				depth--
			}
		}
		if depth <= 0 && index < len(p.toks) {
			return p.arrowAhead(index)
		}
		return false
	case t.kind == tokenIdent:
		return p.tokAt(p.pos+1).text == "=>"
	default:
		return false
	}
}

// arrowAhead scans forward for a depth-0 `=>` before the declarator ends.
func (p *tsParser) arrowAhead(from int) bool {
	depth := 0
	for i := from; i < len(p.toks); i++ {
		t := p.toks[i]
		if t.kind != tokenPunct {
			continue
		}
		switch t.text {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			if depth == 0 {
				return false
			}
			depth--
		case "=>":
			if depth == 0 {
				return true
			}
		case ",", ";":
			if depth == 0 {
				return false
			}
		}
	}
	return false
}

func (p *tsParser) expectPunct(text string) {
	if p.peekPunct(text) {
		p.next()
	}
}

/* ------------------------------------------------------------- utilities */

// collapseWhitespace is the port of `text.replace(/\s+/g, ' ').trim()`.
func collapseWhitespace(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	pendingSpace := false
	for _, r := range text {
		if unicode.IsSpace(r) {
			pendingSpace = true
			continue
		}
		if pendingSpace && b.Len() > 0 {
			b.WriteByte(' ')
		}
		pendingSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

/* ---------------------------------------------------------------- the lexer */

// lexer is the DEV-1 lightweight TypeScript/TSX lexer: strings, template
// literals (including nested `${…}` substitutions), comments, regex literals,
// numbers, identifiers and operators. It exists so the statement scanner never
// mistakes text inside a literal for structure.
type lexer struct {
	src            string
	pos            int
	line           int
	toks           []token
	pendingNewline bool
}

// lexTypeScript tokenizes src (always at least the EOF token).
func lexTypeScript(src string) []token {
	l := &lexer{src: src, line: 1}
	l.run()
	l.toks = append(l.toks, token{kind: tokenEOF, start: len(src), end: len(src), line: l.line, newlineBefore: l.pendingNewline})
	return l.toks
}

func (l *lexer) run() {
	for l.pos < len(l.src) {
		start := l.pos
		c := l.src[l.pos]
		switch {
		case c == '\n':
			l.pendingNewline = true
			l.line++
			l.pos++
		case c == ' ' || c == '\t' || c == '\r' || c == '\v' || c == '\f':
			l.pos++
		case c == '/' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '/':
			l.skipLineComment()
		case c == '/' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '*':
			l.skipBlockComment()
		case c == '\'' || c == '"':
			l.scanString(c)
			l.push(tokenString, start)
		case c == '`':
			l.scanTemplate()
			l.push(tokenTemplate, start)
		case isDigit(c) || (c == '.' && l.pos+1 < len(l.src) && isDigit(l.src[l.pos+1])):
			l.scanNumber()
			l.push(tokenNumber, start)
		case isIdentStart(c):
			l.scanIdent()
			l.push(tokenIdent, start)
		case c == '/':
			if l.regexAllowed() {
				l.scanRegex()
				l.push(tokenRegex, start)
			} else {
				l.pos++
				l.push(tokenPunct, start)
			}
		default:
			l.scanPunct()
			l.push(tokenPunct, start)
		}
	}
}

// push records the token spanning [start, l.pos).
func (l *lexer) push(kind tokenKind, start int) {
	l.toks = append(l.toks, token{
		kind:          kind,
		text:          l.src[start:l.pos],
		start:         start,
		end:           l.pos,
		line:          l.line,
		newlineBefore: l.pendingNewline,
	})
	l.pendingNewline = false
}

// regexAllowed is the standard regex/division heuristic: `/` starts a regex
// literal unless the previous token can end an expression.
func (l *lexer) regexAllowed() bool {
	if len(l.toks) == 0 {
		return true
	}
	prev := l.toks[len(l.toks)-1]
	switch prev.kind {
	case tokenIdent:
		switch prev.text {
		case "return", "typeof", "instanceof", "in", "of", "new", "delete", "void",
			"throw", "case", "do", "else", "yield", "await":
			return true
		}
		return false
	case tokenNumber, tokenString, tokenTemplate, tokenRegex:
		return false
	case tokenPunct:
		switch prev.text {
		case ")", "]", "}":
			return false
		}
		return true
	default:
		return true
	}
}

func (l *lexer) skipLineComment() {
	for l.pos < len(l.src) && l.src[l.pos] != '\n' {
		l.pos++
	}
}

func (l *lexer) skipBlockComment() {
	l.pos += 2 // /*
	for l.pos < len(l.src) {
		if l.src[l.pos] == '*' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '/' {
			l.pos += 2
			return
		}
		if l.src[l.pos] == '\n' {
			l.line++
			l.pendingNewline = true
		}
		l.pos++
	}
}

func (l *lexer) scanString(quote byte) {
	l.pos++ // opening quote
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\\':
			l.pos += 2
		case c == quote:
			l.pos++
			return
		case c == '\n':
			l.line++
			l.pendingNewline = true
			l.pos++
		default:
			l.pos++
		}
	}
}

// scanTemplate consumes a template literal as ONE token, recursing into the
// braces of every `${…}` substitution so nested structure never confuses the
// scanner.
func (l *lexer) scanTemplate() {
	l.pos++ // `
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\\':
			l.pos += 2
		case c == '`':
			l.pos++
			return
		case c == '$' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '{':
			l.pos += 2
			l.skipTemplateSubstitution()
		case c == '\n':
			l.line++
			l.pendingNewline = true
			l.pos++
		default:
			l.pos++
		}
	}
}

func (l *lexer) skipTemplateSubstitution() {
	depth := 1
	for l.pos < len(l.src) && depth > 0 {
		c := l.src[l.pos]
		switch {
		case c == '\\':
			l.pos += 2
		case c == '\n':
			l.line++
			l.pendingNewline = true
			l.pos++
		case c == '\'' || c == '"':
			l.scanString(c)
		case c == '`':
			l.scanTemplate()
		case c == '/' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '/':
			l.skipLineComment()
		case c == '/' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '*':
			l.skipBlockComment()
		case c == '{':
			depth++
			l.pos++
		case c == '}':
			depth--
			l.pos++
		default:
			l.pos++
		}
	}
}

func (l *lexer) scanNumber() {
	if l.pos+1 < len(l.src) && l.src[l.pos] == '0' {
		switch l.src[l.pos+1] {
		case 'x', 'X', 'b', 'B', 'o', 'O':
			l.pos += 2
			for l.pos < len(l.src) && (isHexDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
				l.pos++
			}
			if l.pos < len(l.src) && l.src[l.pos] == 'n' {
				l.pos++ // bigint suffix
			}
			return
		}
	}
	for l.pos < len(l.src) && (isDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
		l.pos++
	}
	// A decimal point only counts when a digit follows (never `1..toString()`
	// and never member access on an integer literal).
	if l.pos+1 < len(l.src) && l.src[l.pos] == '.' && isDigit(l.src[l.pos+1]) {
		l.pos++
		for l.pos < len(l.src) && (isDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
			l.pos++
		}
	}
	if l.pos < len(l.src) && (l.src[l.pos] == 'e' || l.src[l.pos] == 'E') {
		save := l.pos
		l.pos++
		if l.pos < len(l.src) && (l.src[l.pos] == '+' || l.src[l.pos] == '-') {
			l.pos++
		}
		if l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			for l.pos < len(l.src) && (isDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
				l.pos++
			}
		} else {
			l.pos = save // not an exponent after all
		}
	}
	if l.pos < len(l.src) && l.src[l.pos] == 'n' {
		l.pos++
	}
}

func (l *lexer) scanIdent() {
	for l.pos < len(l.src) && isIdentPart(l.src[l.pos]) {
		l.pos++
	}
}

func (l *lexer) scanRegex() {
	l.pos++ // opening /
	inClass := false
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\\':
			l.pos += 2
		case c == '\n':
			return // not a regex literal after all: stop at the line end
		case inClass:
			if c == ']' {
				inClass = false
			}
			l.pos++
		case c == '[':
			inClass = true
			l.pos++
		case c == '/':
			l.pos++
			for l.pos < len(l.src) && isIdentPart(l.src[l.pos]) {
				l.pos++ // flags
			}
			return
		default:
			l.pos++
		}
	}
}

func (l *lexer) scanPunct() {
	for _, p := range multiCharPuncts {
		if strings.HasPrefix(l.src[l.pos:], p) {
			l.pos += len(p)
			return
		}
	}
	l.pos++ // any other single character
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentPart(c byte) bool { return isIdentStart(c) || isDigit(c) }

// Package askama는 pfortainer-rs가 쓰는 askama 템플릿 문법의 부분집합을 실행 시점에 해석한다.
//
// Rust 템플릿 파일(templates/*.html)을 그대로 읽어 같은 바이트를 내는 것이 목적이다.
// 지원: {{ expr }}, {{ expr|safe }}, {% if/else if/else/endif %}, {% if let Some(x) = expr %},
// {% for x in expr %}, {% for (a, b) in expr %}, {% let x = expr %}, {% extends %}, {% block %},
// {% macro %}/{% call %}, {% raw %}, {# 주석 #}, 공백 제어(-).
// 식: 문자열·정수·bool 리터럴, a.b.c, 메서드(is_empty len contains starts_with ends_with iter
// enumerate trim to_string), ! && || == != < > <= >= + - * / %, 괄호.
//
// 값은 Go map[string]any·구조체(json 태그 이름)·슬라이스·기본형. 출력 이스케이프는 askama와 같다
// (" & ' < > → &#34; &#38; &#39; &#60; &#62;).
package askama

import (
	"fmt"
	"io/fs"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ── 노드 ─────────────────────────────────────────────────────────────────────

type node interface{}

type textNode struct{ s string }
type exprNode struct {
	e    expr
	safe bool
}
type ifBranch struct {
	cond    expr
	letName string // if let Some(name) = cond
	body    []node
}
type ifNode struct {
	branches []ifBranch
	els      []node
}
type forNode struct {
	vars []string
	iter expr
	body []node
	els  []node // {% for %}…{% else %}…{% endfor %}: 비었을 때
}
type letNode struct {
	name string
	e    expr
}
type blockNode struct {
	name string
	body []node
}
type callNode struct {
	name string
	args []expr
}

type macro struct {
	params []string
	body   []node
}

// Template: 파싱된 템플릿 하나.
type Template struct {
	name    string
	extends string
	root    []node
	blocks  map[string][]node
	macros  map[string]*macro
}

// Set: 이름 → 템플릿.
type Set struct {
	mu    sync.Mutex
	fsys  fs.FS
	cache map[string]*Template
}

func NewSet(fsys fs.FS) *Set { return &Set{fsys: fsys, cache: map[string]*Template{}} }

func (s *Set) get(name string) (*Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.cache[name]; ok {
		return t, nil
	}
	b, err := fs.ReadFile(s.fsys, name)
	if err != nil {
		return nil, err
	}
	t, err := Parse(name, string(b))
	if err != nil {
		return nil, err
	}
	s.cache[name] = t
	return t, nil
}

// Render: name 템플릿을 ctx(map[string]any 또는 구조체)로 그린다.
func (s *Set) Render(name string, ctx any) (string, error) {
	t, err := s.get(name)
	if err != nil {
		return "", err
	}
	r := &renderer{set: s, blocks: map[string][]node{}, macros: map[string]*macro{}}
	r.push(toScope(ctx))
	// 상속 체인: 자식 블록이 우선
	chain := []*Template{t}
	for cur := t; cur.extends != ""; {
		p, err := s.get(cur.extends)
		if err != nil {
			return "", err
		}
		chain = append(chain, p)
		cur = p
	}
	for _, tt := range chain {
		for k, v := range tt.blocks {
			if _, ok := r.blocks[k]; !ok {
				r.blocks[k] = v
			}
		}
		for k, v := range tt.macros {
			if _, ok := r.macros[k]; !ok {
				r.macros[k] = v
			}
		}
	}
	top := chain[len(chain)-1]
	var b strings.Builder
	if err := r.nodes(&b, top.root); err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return b.String(), nil
}

// ── 파서 ─────────────────────────────────────────────────────────────────────

type token struct {
	kind      byte // 't' 텍스트, 'e' 식, 's' 문장
	s         string
	trimLeft  bool
	trimRight bool
}

func lex(src string) ([]token, error) {
	var toks []token
	for len(src) > 0 {
		i := -1
		for k := 0; k+1 < len(src); k++ {
			if src[k] == '{' && (src[k+1] == '{' || src[k+1] == '%' || src[k+1] == '#') {
				i = k
				break
			}
		}
		if i < 0 {
			toks = append(toks, token{kind: 't', s: src})
			break
		}
		if i > 0 {
			toks = append(toks, token{kind: 't', s: src[:i]})
		}
		open := src[i : i+2]
		var close string
		var kind byte
		switch open {
		case "{{":
			close, kind = "}}", 'e'
		case "{%":
			close, kind = "%}", 's'
		case "{#":
			close, kind = "#}", 'c'
		}
		rest := src[i+2:]
		j := strings.Index(rest, close)
		if j < 0 {
			return nil, fmt.Errorf("닫히지 않은 %s", open)
		}
		inner := rest[:j]
		tk := token{kind: kind}
		if strings.HasPrefix(inner, "-") {
			tk.trimLeft = true
			inner = inner[1:]
		}
		if strings.HasSuffix(inner, "-") {
			tk.trimRight = true
			inner = inner[:len(inner)-1]
		}
		tk.s = strings.TrimSpace(inner)
		src = rest[j+2:]
		if kind == 's' && tk.s == "raw" {
			k := strings.Index(src, "{% endraw %}")
			if k < 0 {
				return nil, fmt.Errorf("endraw 없음")
			}
			toks = append(toks, token{kind: 't', s: src[:k]})
			src = src[k+len("{% endraw %}"):]
			continue
		}
		toks = append(toks, tk)
	}
	// 공백 제어
	for i := range toks {
		if toks[i].kind == 't' {
			continue
		}
		if toks[i].trimLeft && i > 0 && toks[i-1].kind == 't' {
			toks[i-1].s = strings.TrimRight(toks[i-1].s, " \t\r\n")
		}
		if toks[i].trimRight && i+1 < len(toks) && toks[i+1].kind == 't' {
			toks[i+1].s = strings.TrimLeft(toks[i+1].s, " \t\r\n")
		}
	}
	return toks, nil
}

type parser struct {
	toks []token
	pos  int
	t    *Template
}

// Parse: 템플릿 소스를 파싱한다.
func Parse(name, src string) (*Template, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	p := &parser{toks: toks, t: &Template{name: name, blocks: map[string][]node{}, macros: map[string]*macro{}}}
	body, end, err := p.parseUntil()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if end != "" {
		return nil, fmt.Errorf("%s: 짝 없는 {%% %s %%}", name, end)
	}
	p.t.root = body
	return p.t, nil
}

func firstWord(s string) (string, string) {
	s = strings.TrimSpace(s)
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i+1:])
}

// parseUntil: end*/else 문장을 만날 때까지 노드를 모은다. 만난 문장을 돌려준다.
func (p *parser) parseUntil() ([]node, string, error) {
	var out []node
	for p.pos < len(p.toks) {
		tk := p.toks[p.pos]
		p.pos++
		switch tk.kind {
		case 't':
			if tk.s != "" {
				out = append(out, textNode{tk.s})
			}
		case 'c':
		case 'e':
			src := tk.s
			safe := false
			if i := strings.LastIndex(src, "|"); i >= 0 && !strings.Contains(src[i:], "||") && (i == 0 || src[i-1] != '|') {
				f := strings.TrimSpace(src[i+1:])
				switch f {
				case "safe":
					safe = true
					src = src[:i]
				case "e", "escape":
					src = src[:i]
				}
			}
			e, err := parseExpr(src)
			if err != nil {
				return nil, "", err
			}
			out = append(out, exprNode{e, safe})
		case 's':
			kw, rest := firstWord(tk.s)
			switch kw {
			case "endif", "endfor", "endblock", "endmacro", "endcall", "else":
				return out, tk.s, nil
			case "extends":
				p.t.extends = strings.Trim(rest, `"`)
			case "if":
				n, err := p.parseIf(rest)
				if err != nil {
					return nil, "", err
				}
				out = append(out, n)
			case "for":
				n, err := p.parseFor(rest)
				if err != nil {
					return nil, "", err
				}
				out = append(out, n)
			case "let":
				i := strings.Index(rest, "=")
				if i < 0 {
					return nil, "", fmt.Errorf("let 문법: %s", rest)
				}
				e, err := parseExpr(rest[i+1:])
				if err != nil {
					return nil, "", err
				}
				out = append(out, letNode{strings.TrimSpace(rest[:i]), e})
			case "block":
				name := strings.TrimSpace(rest)
				body, end, err := p.parseUntil()
				if err != nil {
					return nil, "", err
				}
				if !strings.HasPrefix(end, "endblock") {
					return nil, "", fmt.Errorf("block %s: endblock 없음", name)
				}
				if _, ok := p.t.blocks[name]; !ok {
					p.t.blocks[name] = body
				}
				out = append(out, blockNode{name, body})
			case "macro":
				i := strings.Index(rest, "(")
				name := strings.TrimSpace(rest[:i])
				params := splitArgs(rest[i+1 : strings.LastIndex(rest, ")")])
				body, end, err := p.parseUntil()
				if err != nil {
					return nil, "", err
				}
				if !strings.HasPrefix(end, "endmacro") {
					return nil, "", fmt.Errorf("macro %s: endmacro 없음", name)
				}
				var ps []string
				for _, a := range params {
					ps = append(ps, strings.TrimSpace(a))
				}
				p.t.macros[name] = &macro{params: ps, body: body}
			case "call":
				i := strings.Index(rest, "(")
				name := strings.TrimSpace(rest[:i])
				var args []expr
				for _, a := range splitArgs(rest[i+1 : strings.LastIndex(rest, ")")]) {
					e, err := parseExpr(a)
					if err != nil {
						return nil, "", err
					}
					args = append(args, e)
				}
				// {% call %} … {% endcall %} (본문은 caller()용, 여기선 쓰지 않음)
				_, end, err := p.parseUntil()
				if err != nil {
					return nil, "", err
				}
				if !strings.HasPrefix(end, "endcall") {
					return nil, "", fmt.Errorf("call %s: endcall 없음", name)
				}
				out = append(out, callNode{name, args})
			default:
				return nil, "", fmt.Errorf("모르는 문장 {%% %s %%}", tk.s)
			}
		}
	}
	return out, "", nil
}

func splitArgs(s string) []string {
	var out []string
	depth, inStr, start := 0, false, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr:
			if c == '\\' {
				i++
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if strings.TrimSpace(s[start:]) != "" {
		out = append(out, s[start:])
	}
	return out
}

func (p *parser) parseIf(cond string) (node, error) {
	n := &ifNode{}
	for {
		br := ifBranch{}
		c := strings.TrimSpace(cond)
		if strings.HasPrefix(c, "let ") {
			// let Some(name) = expr
			c = strings.TrimSpace(c[4:])
			i := strings.Index(c, "=")
			pat := strings.TrimSpace(c[:i])
			if !strings.HasPrefix(pat, "Some(") {
				return nil, fmt.Errorf("if let 패턴: %s", pat)
			}
			br.letName = strings.TrimSuffix(strings.TrimPrefix(pat, "Some("), ")")
			c = c[i+1:]
		}
		e, err := parseExpr(c)
		if err != nil {
			return nil, err
		}
		br.cond = e
		body, end, err := p.parseUntil()
		if err != nil {
			return nil, err
		}
		br.body = body
		n.branches = append(n.branches, br)
		kw, rest := firstWord(end)
		switch {
		case kw == "endif":
			return n, nil
		case kw == "else" && strings.HasPrefix(rest, "if "):
			cond = rest[3:]
			continue
		case kw == "else" && rest == "":
			els, end2, err := p.parseUntil()
			if err != nil {
				return nil, err
			}
			if end2 != "endif" {
				return nil, fmt.Errorf("else 뒤 endif 없음: %q", end2)
			}
			n.els = els
			return n, nil
		default:
			return nil, fmt.Errorf("if: 예상 못한 %q", end)
		}
	}
}

func (p *parser) parseFor(rest string) (node, error) {
	i := strings.Index(rest, " in ")
	if i < 0 {
		return nil, fmt.Errorf("for 문법: %s", rest)
	}
	lhs := strings.TrimSpace(rest[:i])
	var vars []string
	if strings.HasPrefix(lhs, "(") {
		for _, v := range strings.Split(strings.Trim(lhs, "()"), ",") {
			vars = append(vars, strings.TrimSpace(v))
		}
	} else {
		vars = []string{lhs}
	}
	e, err := parseExpr(rest[i+4:])
	if err != nil {
		return nil, err
	}
	body, end, err := p.parseUntil()
	if err != nil {
		return nil, err
	}
	n := &forNode{vars: vars, iter: e, body: body}
	if end == "else" {
		n.els, end, err = p.parseUntil()
		if err != nil {
			return nil, err
		}
	}
	if end != "endfor" {
		return nil, fmt.Errorf("for: endfor 없음 (%q)", end)
	}
	return n, nil
}

// ── 식 ───────────────────────────────────────────────────────────────────────

type expr interface{}

type litExpr struct{ v any }
type varExpr struct{ name string }
type fieldExpr struct {
	x    expr
	name string
}
type callExpr struct {
	x    expr
	name string
	args []expr
}
type unExpr struct {
	op string
	x  expr
}
type binExpr struct {
	op   string
	l, r expr
}

type exprLexer struct {
	s   string
	pos int
}

func (l *exprLexer) skip() {
	for l.pos < len(l.s) && strings.ContainsRune(" \t\r\n", rune(l.s[l.pos])) {
		l.pos++
	}
}

func (l *exprLexer) peek(tok string) bool {
	l.skip()
	return strings.HasPrefix(l.s[l.pos:], tok)
}

func (l *exprLexer) eat(tok string) bool {
	if l.peek(tok) {
		l.pos += len(tok)
		return true
	}
	return false
}

func parseExpr(s string) (expr, error) {
	l := &exprLexer{s: s}
	e, err := l.or()
	if err != nil {
		return nil, err
	}
	l.skip()
	if l.pos != len(l.s) {
		return nil, fmt.Errorf("식 끝에 남은 것: %q (%q)", l.s[l.pos:], s)
	}
	return e, nil
}

func (l *exprLexer) or() (expr, error) {
	x, err := l.and()
	for err == nil && l.eat("||") {
		var y expr
		y, err = l.and()
		x = binExpr{"||", x, y}
	}
	return x, err
}

func (l *exprLexer) and() (expr, error) {
	x, err := l.cmp()
	for err == nil && l.eat("&&") {
		var y expr
		y, err = l.cmp()
		x = binExpr{"&&", x, y}
	}
	return x, err
}

func (l *exprLexer) cmp() (expr, error) {
	x, err := l.add()
	if err != nil {
		return nil, err
	}
	for _, op := range []string{"==", "!=", "<=", ">=", "<", ">"} {
		if l.eat(op) {
			y, err := l.add()
			return binExpr{op, x, y}, err
		}
	}
	return x, nil
}

func (l *exprLexer) add() (expr, error) {
	x, err := l.mul()
	for err == nil {
		var op string
		switch {
		case l.eat("+"):
			op = "+"
		case l.peek("-") && !l.peek("->"):
			l.eat("-")
			op = "-"
		default:
			return x, nil
		}
		var y expr
		y, err = l.mul()
		x = binExpr{op, x, y}
	}
	return x, err
}

func (l *exprLexer) mul() (expr, error) {
	x, err := l.unary()
	for err == nil {
		var op string
		switch {
		case l.eat("*"):
			op = "*"
		case l.eat("/"):
			op = "/"
		case l.eat("%"):
			op = "%"
		default:
			return x, nil
		}
		var y expr
		y, err = l.unary()
		x = binExpr{op, x, y}
	}
	return x, err
}

func (l *exprLexer) unary() (expr, error) {
	if l.peek("!") && !l.peek("!=") {
		l.eat("!")
		x, err := l.unary()
		return unExpr{"!", x}, err
	}
	if l.eat("-") {
		x, err := l.unary()
		return unExpr{"-", x}, err
	}
	return l.postfix()
}

func (l *exprLexer) postfix() (expr, error) {
	x, err := l.primary()
	if err != nil {
		return nil, err
	}
	for {
		if l.peek("..") {
			return x, nil
		}
		if !l.eat(".") {
			return x, nil
		}
		l.skip()
		name := l.ident()
		if name == "" {
			return nil, fmt.Errorf("필드 이름 없음: %q", l.s)
		}
		if l.eat("(") {
			var args []expr
			for !l.eat(")") {
				a, err := l.or()
				if err != nil {
					return nil, err
				}
				args = append(args, a)
				l.eat(",")
			}
			x = callExpr{x, name, args}
		} else {
			x = fieldExpr{x, name}
		}
	}
}

func (l *exprLexer) ident() string {
	start := l.pos
	for l.pos < len(l.s) {
		c := l.s[l.pos]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			l.pos++
		} else {
			break
		}
	}
	return l.s[start:l.pos]
}

func (l *exprLexer) primary() (expr, error) {
	l.skip()
	if l.pos >= len(l.s) {
		return nil, fmt.Errorf("식이 비었음")
	}
	c := l.s[l.pos]
	switch {
	case c == '(':
		l.pos++
		x, err := l.or()
		if err != nil {
			return nil, err
		}
		if !l.eat(")") {
			return nil, fmt.Errorf("괄호 닫힘 없음")
		}
		return x, nil
	case c == '"':
		l.pos++
		var b strings.Builder
		for l.pos < len(l.s) && l.s[l.pos] != '"' {
			if l.s[l.pos] == '\\' && l.pos+1 < len(l.s) {
				l.pos++
				switch l.s[l.pos] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				default:
					b.WriteByte(l.s[l.pos])
				}
			} else {
				b.WriteByte(l.s[l.pos])
			}
			l.pos++
		}
		l.pos++
		return litExpr{b.String()}, nil
	case c >= '0' && c <= '9':
		start := l.pos
		for l.pos < len(l.s) && (l.s[l.pos] >= '0' && l.s[l.pos] <= '9' || l.s[l.pos] == '.' && l.pos+1 < len(l.s) && l.s[l.pos+1] != '.') {
			l.pos++
		}
		t := l.s[start:l.pos]
		if strings.Contains(t, ".") {
			f, _ := strconv.ParseFloat(t, 64)
			return litExpr{f}, nil
		}
		n, _ := strconv.ParseInt(t, 10, 64)
		return litExpr{n}, nil
	}
	name := l.ident()
	switch name {
	case "":
		return nil, fmt.Errorf("예상 못한 문자 %q in %q", c, l.s)
	case "true":
		return litExpr{true}, nil
	case "false":
		return litExpr{false}, nil
	}
	return varExpr{name}, nil
}

// ── 렌더러 ───────────────────────────────────────────────────────────────────

type renderer struct {
	set    *Set
	scopes []map[string]any
	blocks map[string][]node
	macros map[string]*macro
}

func (r *renderer) push(m map[string]any) { r.scopes = append(r.scopes, m) }
func (r *renderer) pop()                  { r.scopes = r.scopes[:len(r.scopes)-1] }

func (r *renderer) lookup(name string) (any, bool) {
	for i := len(r.scopes) - 1; i >= 0; i-- {
		if v, ok := r.scopes[i][name]; ok {
			return v, true
		}
	}
	return nil, false
}

func toScope(ctx any) map[string]any {
	if m, ok := ctx.(map[string]any); ok {
		return m
	}
	out := map[string]any{}
	v := reflect.Indirect(reflect.ValueOf(ctx))
	if v.Kind() == reflect.Struct {
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).IsExported() {
				out[fieldName(t.Field(i))] = v.Field(i).Interface()
			}
		}
	}
	return out
}

func fieldName(f reflect.StructField) string {
	if tg := f.Tag.Get("tpl"); tg != "" {
		return tg
	}
	if tg := strings.Split(f.Tag.Get("json"), ",")[0]; tg != "" && tg != "-" {
		return tg
	}
	return snake(f.Name)
}

func snake(s string) string {
	var b strings.Builder
	for i, c := range s {
		if c >= 'A' && c <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(c + 32)
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func (r *renderer) nodes(b *strings.Builder, ns []node) error {
	for _, n := range ns {
		if err := r.node(b, n); err != nil {
			return err
		}
	}
	return nil
}

func (r *renderer) node(b *strings.Builder, n node) error {
	switch n := n.(type) {
	case textNode:
		b.WriteString(n.s)
	case exprNode:
		v, err := r.eval(n.e)
		if err != nil {
			return err
		}
		s := Display(v)
		if n.safe {
			b.WriteString(s)
		} else {
			b.WriteString(Escape(s))
		}
	case *ifNode:
		for _, br := range n.branches {
			v, err := r.eval(br.cond)
			if err != nil {
				return err
			}
			if br.letName != "" {
				inner, some := optionValue(v)
				if !some {
					continue
				}
				r.push(map[string]any{br.letName: inner})
				err := r.nodes(b, br.body)
				r.pop()
				return err
			}
			if truthy(v) {
				r.push(map[string]any{})
				err := r.nodes(b, br.body)
				r.pop()
				return err
			}
		}
		r.push(map[string]any{})
		err := r.nodes(b, n.els)
		r.pop()
		return err
	case *forNode:
		v, err := r.eval(n.iter)
		if err != nil {
			return err
		}
		items, err := iterate(v)
		if err != nil {
			return err
		}
		if len(items) == 0 && n.els != nil {
			r.push(map[string]any{})
			err := r.nodes(b, n.els)
			r.pop()
			return err
		}
		for idx, it := range items {
			sc := map[string]any{"loop": map[string]any{"index": int64(idx + 1), "index0": int64(idx), "first": idx == 0, "last": idx == len(items)-1}}
			if len(n.vars) == 1 {
				sc[n.vars[0]] = it
			} else {
				pair, ok := it.([2]any)
				if !ok {
					return fmt.Errorf("for (a, b): 쌍이 아님")
				}
				sc[n.vars[0]], sc[n.vars[1]] = pair[0], pair[1]
			}
			r.push(sc)
			err := r.nodes(b, n.body)
			r.pop()
			if err != nil {
				return err
			}
		}
	case letNode:
		v, err := r.eval(n.e)
		if err != nil {
			return err
		}
		r.scopes[len(r.scopes)-1][n.name] = v
	case blockNode:
		return r.nodes(b, r.blocks[n.name])
	case callNode:
		m, ok := r.macros[n.name]
		if !ok {
			return fmt.Errorf("매크로 없음: %s", n.name)
		}
		sc := map[string]any{}
		for i, p := range m.params {
			if i < len(n.args) {
				v, err := r.eval(n.args[i])
				if err != nil {
					return err
				}
				sc[p] = v
			}
		}
		r.push(sc)
		err := r.nodes(b, m.body)
		r.pop()
		return err
	default:
		return fmt.Errorf("모르는 노드 %T", n)
	}
	return nil
}

// Escape: askama 기본 HTML 이스케이프.
func Escape(s string) string {
	if !strings.ContainsAny(s, `"&'<>`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			b.WriteString("&#34;")
		case '&':
			b.WriteString("&#38;")
		case '\'':
			b.WriteString("&#39;")
		case '<':
			b.WriteString("&#60;")
		case '>':
			b.WriteString("&#62;")
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// Displayer: Rust Display가 필요한 타입(Role 등).
type Displayer interface{ Display() string }

// Display: Rust Display와 같은 문자열.
func Display(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case Displayer:
		return x.Display()
	case fmt.Stringer:
		return x.String()
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return RustF64(x)
	case float32:
		return RustF64(float64(x))
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(rv.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(rv.Uint(), 10)
	case reflect.Pointer:
		if rv.IsNil() {
			return ""
		}
		return Display(rv.Elem().Interface())
	}
	return fmt.Sprint(v)
}

// RustF64: Rust f64 Display(지수 표기 없음, 정수면 소수점 없음).
func RustF64(f float64) string {
	if math.IsInf(f, 1) {
		return "inf"
	}
	if math.IsInf(f, -1) {
		return "-inf"
	}
	if math.IsNaN(f) {
		return "NaN"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func optionValue(v any) (any, bool) {
	if v == nil {
		return nil, false
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, false
		}
		return rv.Elem().Interface(), true
	}
	return v, true
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case nil:
		return false
	}
	return true
}

func iterate(v any) ([]any, error) {
	if v == nil {
		return nil, nil
	}
	if it, ok := v.(iterList); ok {
		return it, nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		out := make([]any, rv.Len())
		for i := range out {
			out[i] = rv.Index(i).Interface()
		}
		return out, nil
	case reflect.Map:
		keys := rv.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i].Interface()) < fmt.Sprint(keys[j].Interface()) })
		out := make([]any, len(keys))
		for i, k := range keys {
			out[i] = [2]any{k.Interface(), rv.MapIndex(k).Interface()}
		}
		return out, nil
	case reflect.Pointer:
		if rv.IsNil() {
			return nil, nil
		}
		return iterate(rv.Elem().Interface())
	}
	return nil, fmt.Errorf("반복할 수 없는 값 %T", v)
}

type iterList []any

func (r *renderer) eval(e expr) (any, error) {
	switch e := e.(type) {
	case litExpr:
		return e.v, nil
	case varExpr:
		v, ok := r.lookup(e.name)
		if !ok {
			return nil, fmt.Errorf("변수 없음: %s", e.name)
		}
		return v, nil
	case fieldExpr:
		x, err := r.eval(e.x)
		if err != nil {
			return nil, err
		}
		return field(x, e.name)
	case callExpr:
		x, err := r.eval(e.x)
		if err != nil {
			return nil, err
		}
		var args []any
		for _, a := range e.args {
			v, err := r.eval(a)
			if err != nil {
				return nil, err
			}
			args = append(args, v)
		}
		return method(x, e.name, args)
	case unExpr:
		x, err := r.eval(e.x)
		if err != nil {
			return nil, err
		}
		if e.op == "!" {
			return !truthy(x), nil
		}
		if f, ok := x.(float64); ok {
			return -f, nil
		}
		return -toInt(x), nil
	case binExpr:
		l, err := r.eval(e.l)
		if err != nil {
			return nil, err
		}
		if e.op == "&&" {
			if !truthy(l) {
				return false, nil
			}
			rv, err := r.eval(e.r)
			return truthy(rv), err
		}
		if e.op == "||" {
			if truthy(l) {
				return true, nil
			}
			rv, err := r.eval(e.r)
			return truthy(rv), err
		}
		rv, err := r.eval(e.r)
		if err != nil {
			return nil, err
		}
		return binop(e.op, l, rv)
	}
	return nil, fmt.Errorf("모르는 식 %T", e)
}

func deref(v any) any {
	rv := reflect.ValueOf(v)
	for rv.IsValid() && rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return nil
	}
	return rv.Interface()
}

func field(x any, name string) (any, error) {
	x = deref(x)
	if m, ok := x.(map[string]any); ok {
		v, ok := m[name]
		if !ok {
			return nil, fmt.Errorf("필드 없음: %s", name)
		}
		return v, nil
	}
	rv := reflect.ValueOf(x)
	if rv.Kind() == reflect.Struct {
		t := rv.Type()
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).IsExported() && fieldName(t.Field(i)) == name {
				return rv.Field(i).Interface(), nil
			}
		}
	}
	return nil, fmt.Errorf("필드 없음: %s (%T)", name, x)
}

func lenOf(x any) int64 {
	x = deref(x)
	if s, ok := x.(string); ok {
		return int64(len(s))
	}
	rv := reflect.ValueOf(x)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map, reflect.String:
		return int64(rv.Len())
	}
	return 0
}

func method(x any, name string, args []any) (any, error) {
	switch name {
	case "is_empty":
		return lenOf(x) == 0, nil
	case "len":
		return lenOf(x), nil
	case "iter", "clone", "as_str", "to_string":
		return x, nil
	case "enumerate":
		items, err := iterate(x)
		if err != nil {
			return nil, err
		}
		out := make(iterList, len(items))
		for i, it := range items {
			out[i] = [2]any{int64(i), it}
		}
		return out, nil
	case "contains", "starts_with", "ends_with":
		s, _ := deref(x).(string)
		a := Display(args[0])
		switch name {
		case "contains":
			return strings.Contains(s, a), nil
		case "starts_with":
			return strings.HasPrefix(s, a), nil
		default:
			return strings.HasSuffix(s, a), nil
		}
	case "trim":
		s, _ := deref(x).(string)
		return strings.TrimSpace(s), nil
	}
	// Go 메서드(이름은 snake → Camel)
	rv := reflect.ValueOf(x)
	m := rv.MethodByName(camel(name))
	if m.IsValid() {
		var in []reflect.Value
		for _, a := range args {
			in = append(in, reflect.ValueOf(a))
		}
		out := m.Call(in)
		if len(out) > 0 {
			return out[0].Interface(), nil
		}
		return nil, nil
	}
	return nil, fmt.Errorf("메서드 없음: %s (%T)", name, x)
}

func camel(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

func isNum(v any) bool {
	switch reflect.ValueOf(deref(v)).Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

func isFloat(v any) bool {
	k := reflect.ValueOf(deref(v)).Kind()
	return k == reflect.Float32 || k == reflect.Float64
}

func toInt(v any) int64 {
	rv := reflect.ValueOf(deref(v))
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(rv.Uint())
	case reflect.Float32, reflect.Float64:
		return int64(rv.Float())
	}
	return 0
}

func toFloat(v any) float64 {
	rv := reflect.ValueOf(deref(v))
	switch rv.Kind() {
	case reflect.Float32, reflect.Float64:
		return rv.Float()
	}
	return float64(toInt(v))
}

func binop(op string, l, r any) (any, error) {
	if isNum(l) && isNum(r) {
		if isFloat(l) || isFloat(r) {
			a, b := toFloat(l), toFloat(r)
			switch op {
			case "==":
				return a == b, nil
			case "!=":
				return a != b, nil
			case "<":
				return a < b, nil
			case ">":
				return a > b, nil
			case "<=":
				return a <= b, nil
			case ">=":
				return a >= b, nil
			case "+":
				return a + b, nil
			case "-":
				return a - b, nil
			case "*":
				return a * b, nil
			case "/":
				return a / b, nil
			}
		}
		a, b := toInt(l), toInt(r)
		switch op {
		case "==":
			return a == b, nil
		case "!=":
			return a != b, nil
		case "<":
			return a < b, nil
		case ">":
			return a > b, nil
		case "<=":
			return a <= b, nil
		case ">=":
			return a >= b, nil
		case "+":
			return a + b, nil
		case "-":
			return a - b, nil
		case "*":
			return a * b, nil
		case "/":
			if b == 0 {
				return nil, fmt.Errorf("0으로 나눔")
			}
			return a / b, nil
		case "%":
			if b == 0 {
				return nil, fmt.Errorf("0으로 나눔")
			}
			return a % b, nil
		}
	}
	ls, rs := Display(deref(l)), Display(deref(r))
	switch op {
	case "==":
		return ls == rs, nil
	case "!=":
		return ls != rs, nil
	case "<":
		return ls < rs, nil
	case ">":
		return ls > rs, nil
	case "<=":
		return ls <= rs, nil
	case ">=":
		return ls >= rs, nil
	case "+":
		return ls + rs, nil
	}
	return nil, fmt.Errorf("연산 %s 불가: %T %T", op, l, r)
}

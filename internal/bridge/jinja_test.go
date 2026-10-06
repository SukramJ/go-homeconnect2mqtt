// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A Jinja subset, evaluated the way Home Assistant's MQTT integration
// evaluates a value or command template: `value` is the payload as a
// string, `value_json` is the payload parsed as JSON when it parses
// (components/mqtt/models.py, MqttValueTemplate), and the result is the
// Python str() of what the template produced.
//
// It covers exactly what this daemon's documents contain — go-hamqtt's
// StatusValueTemplate, StatusBoolValueTemplate, the enum label map from
// EnumTemplates and ConnectedTemplate:
//
//   - text, `{{ expr }}`, `{% set name = expr %}`, `{% if expr %}…{% endif %}`
//     (with an optional `{% else %}`);
//   - literals: single- and double-quoted strings, integers, floats, dict
//     literals, true/false/none in either case;
//   - `.attr` on a dict, `.get(key[, default])` on a dict;
//   - the filters `lower` and `int(default)`;
//   - the tests `is defined`, `is none`, and their `not` forms;
//   - `not`, `and`, `or`, the six comparisons, and `a if c else b`.
//
// Anything else is an ERROR, never a pass-through: a template this cannot
// evaluate fails the test that asked, so a new construct in a rendered
// document has to be taught here before it is trusted. Rendering an
// undefined value, or reading an attribute of one, is an error too — Home
// Assistant logs it and the entity gets no state.

// jUndefined is Jinja's Undefined.
type jUndefined struct{}

// jInt is a Python int; float64 is a Python float. JSON numbers are decoded
// into one or the other the way Python's json module does.
type jInt int64

type jDict map[string]any

// renderTemplate renders tmpl against payload (value_json is defined only
// when payload is JSON) with the given extra variables.
func renderTemplate(tmpl string, payload []byte, vars map[string]any) (string, error) {
	scope := map[string]any{"value": string(payload)}
	if v, err := decodePython(payload); err == nil {
		scope["value_json"] = v
	}
	for k, v := range vars {
		scope[k] = v
	}
	nodes, err := parseJinja(tmpl)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	if err := execNodes(nodes, scope, &out); err != nil {
		return "", err
	}
	return out.String(), nil
}

// decodePython is json.loads.
func decodePython(payload []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data")
	}
	return pythonize(v)
}

func pythonize(v any) (any, error) {
	switch x := v.(type) {
	case json.Number:
		if strings.ContainsAny(x.String(), ".eE") {
			f, err := x.Float64()
			return f, err
		}
		i, err := x.Int64()
		return jInt(i), err
	case map[string]any:
		out := jDict{}
		for k, e := range x {
			p, err := pythonize(e)
			if err != nil {
				return nil, err
			}
			out[k] = p
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			p, err := pythonize(e)
			if err != nil {
				return nil, err
			}
			out[i] = p
		}
		return out, nil
	default:
		return v, nil
	}
}

// ---------------------------------------------------------------------------
// template structure
// ---------------------------------------------------------------------------

type jNode interface{}

type jText struct{ s string }

type jOutput struct{ e jExpr }

type jSet struct {
	name string
	e    jExpr
}

type jIf struct {
	cond            jExpr
	then, otherwise []jNode
}

func parseJinja(src string) ([]jNode, error) {
	nodes, rest, stop, err := parseBlock(src, nil)
	if err != nil {
		return nil, err
	}
	if stop != "" || rest != "" {
		return nil, fmt.Errorf("jinja: unexpected {%% %s %%}", stop)
	}
	return nodes, nil
}

// parseBlock parses until one of the stop tags; it returns the tag that
// stopped it ("" at the end of input) and the input after that tag.
func parseBlock(src string, stops []string) (nodes []jNode, rest, stop string, err error) {
	for src != "" {
		i := strings.Index(src, "{")
		for i >= 0 && i+1 < len(src) && src[i+1] != '{' && src[i+1] != '%' && src[i+1] != '#' {
			j := strings.Index(src[i+1:], "{")
			if j < 0 {
				i = -1
				break
			}
			i += 1 + j
		}
		if i < 0 || i+1 >= len(src) {
			nodes = append(nodes, jText{src})
			return nodes, "", "", nil
		}
		if i > 0 {
			nodes = append(nodes, jText{src[:i]})
		}
		src = src[i:]
		switch src[1] {
		case '#':
			return nil, "", "", errors.New("jinja: comments are not supported")
		case '{':
			end := strings.Index(src, "}}")
			if end < 0 {
				return nil, "", "", errors.New("jinja: unterminated {{")
			}
			inner := src[2:end]
			if strings.HasPrefix(inner, "-") || strings.HasSuffix(inner, "-") {
				return nil, "", "", errors.New("jinja: whitespace control is not supported")
			}
			e, err := parseExpr(inner)
			if err != nil {
				return nil, "", "", err
			}
			nodes = append(nodes, jOutput{e})
			src = src[end+2:]
		case '%':
			end := strings.Index(src, "%}")
			if end < 0 {
				return nil, "", "", errors.New("jinja: unterminated {%")
			}
			inner := src[2:end]
			if strings.HasPrefix(inner, "-") || strings.HasSuffix(inner, "-") {
				return nil, "", "", errors.New("jinja: whitespace control is not supported")
			}
			tag := strings.TrimSpace(inner)
			src = src[end+2:]
			word, arg, _ := strings.Cut(tag, " ")
			switch word {
			case "set":
				name, expr, ok := strings.Cut(arg, "=")
				name = strings.TrimSpace(name)
				if !ok || !isIdent(name) {
					return nil, "", "", fmt.Errorf("jinja: unsupported set %q", tag)
				}
				e, err := parseExpr(expr)
				if err != nil {
					return nil, "", "", err
				}
				nodes = append(nodes, jSet{name, e})
			case "if":
				cond, err := parseExpr(arg)
				if err != nil {
					return nil, "", "", err
				}
				then, after, stopped, err := parseBlock(src, []string{"else", "endif"})
				if err != nil {
					return nil, "", "", err
				}
				var els []jNode
				if stopped == "else" {
					els, after, stopped, err = parseBlock(after, []string{"endif"})
					if err != nil {
						return nil, "", "", err
					}
				}
				if stopped != "endif" {
					return nil, "", "", errors.New("jinja: {% if %} without {% endif %}")
				}
				nodes = append(nodes, jIf{cond, then, els})
				src = after
			default:
				for _, s := range stops {
					if tag == s {
						return nodes, src, s, nil
					}
				}
				return nil, "", "", fmt.Errorf("jinja: unsupported tag {%% %s %%}", tag)
			}
		}
	}
	return nodes, "", "", nil
}

func execNodes(nodes []jNode, scope map[string]any, out *strings.Builder) error {
	for _, n := range nodes {
		switch x := n.(type) {
		case jText:
			out.WriteString(x.s)
		case jOutput:
			v, err := x.e.eval(scope)
			if err != nil {
				return err
			}
			s, err := pyStr(v)
			if err != nil {
				return err
			}
			out.WriteString(s)
		case jSet:
			v, err := x.e.eval(scope)
			if err != nil {
				return err
			}
			scope[x.name] = v
		case jIf:
			v, err := x.cond.eval(scope)
			if err != nil {
				return err
			}
			branch := x.otherwise
			if truthy(v) {
				branch = x.then
			}
			if err := execNodes(branch, scope, out); err != nil {
				return err
			}
		default:
			return fmt.Errorf("jinja: node %T", n)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// expressions
// ---------------------------------------------------------------------------

type jExpr interface {
	eval(scope map[string]any) (any, error)
}

type jTok struct {
	kind string // "str", "num", "name", "op"
	s    string
}

func lexExpr(src string) ([]jTok, error) {
	var toks []jTok
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			i++
		case c == '\'' || c == '"':
			var b strings.Builder
			j := i + 1
			for ; j < len(src) && src[j] != c; j++ {
				if src[j] == '\\' {
					j++
					if j >= len(src) {
						return nil, errors.New("jinja: dangling escape")
					}
					switch src[j] {
					case '\\', '\'', '"':
						b.WriteByte(src[j])
					case 'n':
						b.WriteByte('\n')
					default:
						return nil, fmt.Errorf("jinja: unsupported escape \\%c", src[j])
					}
					continue
				}
				b.WriteByte(src[j])
			}
			if j >= len(src) {
				return nil, errors.New("jinja: unterminated string")
			}
			toks = append(toks, jTok{"str", b.String()})
			i = j + 1
		case c >= '0' && c <= '9':
			j := i
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.') {
				j++
			}
			toks = append(toks, jTok{"num", src[i:j]})
			i = j
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			j := i
			for j < len(src) && (src[j] == '_' || src[j] >= 'a' && src[j] <= 'z' || src[j] >= 'A' && src[j] <= 'Z' || src[j] >= '0' && src[j] <= '9') {
				j++
			}
			toks = append(toks, jTok{"name", src[i:j]})
			i = j
		default:
			two := ""
			if i+1 < len(src) {
				two = src[i : i+2]
			}
			switch two {
			case ">=", "<=", "==", "!=":
				toks = append(toks, jTok{"op", two})
				i += 2
				continue
			}
			if strings.IndexByte("|.,:(){}<>-", c) < 0 {
				return nil, fmt.Errorf("jinja: unsupported character %q", c)
			}
			toks = append(toks, jTok{"op", string(c)})
			i++
		}
	}
	return toks, nil
}

func isIdent(s string) bool {
	toks, err := lexExpr(s)
	return err == nil && len(toks) == 1 && toks[0].kind == "name"
}

type jParser struct {
	toks []jTok
	pos  int
}

func parseExpr(src string) (jExpr, error) {
	toks, err := lexExpr(src)
	if err != nil {
		return nil, err
	}
	p := &jParser{toks: toks}
	e, err := p.cond()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.toks) {
		return nil, fmt.Errorf("jinja: unexpected %q in %q", p.toks[p.pos].s, src)
	}
	return e, nil
}

func (p *jParser) peek() jTok {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return jTok{}
}

func (p *jParser) accept(kind, s string) bool {
	if t := p.peek(); t.kind == kind && t.s == s {
		p.pos++
		return true
	}
	return false
}

func (p *jParser) expect(kind, s string) error {
	if !p.accept(kind, s) {
		return fmt.Errorf("jinja: expected %q, got %q", s, p.peek().s)
	}
	return nil
}

type jFunc func(scope map[string]any) (any, error)

func (f jFunc) eval(scope map[string]any) (any, error) { return f(scope) }

func (p *jParser) cond() (jExpr, error) {
	then, err := p.or()
	if err != nil {
		return nil, err
	}
	if !p.accept("name", "if") {
		return then, nil
	}
	c, err := p.or()
	if err != nil {
		return nil, err
	}
	if err := p.expect("name", "else"); err != nil {
		return nil, err
	}
	els, err := p.cond()
	if err != nil {
		return nil, err
	}
	return jFunc(func(s map[string]any) (any, error) {
		v, err := c.eval(s)
		if err != nil {
			return nil, err
		}
		if truthy(v) {
			return then.eval(s)
		}
		return els.eval(s)
	}), nil
}

func (p *jParser) or() (jExpr, error) {
	l, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.accept("name", "or") {
		r, err := p.and()
		if err != nil {
			return nil, err
		}
		left := l
		l = jFunc(func(s map[string]any) (any, error) {
			v, err := left.eval(s)
			if err != nil || truthy(v) {
				return v, err
			}
			return r.eval(s)
		})
	}
	return l, nil
}

func (p *jParser) and() (jExpr, error) {
	l, err := p.not()
	if err != nil {
		return nil, err
	}
	for p.accept("name", "and") {
		r, err := p.not()
		if err != nil {
			return nil, err
		}
		left := l
		l = jFunc(func(s map[string]any) (any, error) {
			v, err := left.eval(s)
			if err != nil || !truthy(v) {
				return v, err
			}
			return r.eval(s)
		})
	}
	return l, nil
}

func (p *jParser) not() (jExpr, error) {
	if p.accept("name", "not") {
		e, err := p.not()
		if err != nil {
			return nil, err
		}
		return jFunc(func(s map[string]any) (any, error) {
			v, err := e.eval(s)
			return !truthy(v), err
		}), nil
	}
	return p.compare()
}

func (p *jParser) compare() (jExpr, error) {
	l, err := p.filtered()
	if err != nil {
		return nil, err
	}
	if p.accept("name", "is") {
		negate := p.accept("name", "not")
		t := p.peek()
		p.pos++
		var test func(any) bool
		switch {
		case t.kind == "name" && t.s == "defined":
			test = func(v any) bool { _, undef := v.(jUndefined); return !undef }
		case t.kind == "name" && (t.s == "none" || t.s == "None"):
			test = func(v any) bool { return v == nil }
		default:
			return nil, fmt.Errorf("jinja: unsupported test %q", t.s)
		}
		return jFunc(func(s map[string]any) (any, error) {
			v, err := l.eval(s)
			if err != nil {
				return nil, err
			}
			return test(v) != negate, nil
		}), nil
	}
	t := p.peek()
	if t.kind != "op" {
		return l, nil
	}
	switch t.s {
	case "==", "!=", ">=", "<=", ">", "<":
	default:
		return l, nil
	}
	p.pos++
	r, err := p.filtered()
	if err != nil {
		return nil, err
	}
	return jFunc(func(s map[string]any) (any, error) {
		a, err := l.eval(s)
		if err != nil {
			return nil, err
		}
		b, err := r.eval(s)
		if err != nil {
			return nil, err
		}
		return pyCompare(t.s, a, b)
	}), nil
}

func (p *jParser) filtered() (jExpr, error) {
	e, err := p.postfix()
	if err != nil {
		return nil, err
	}
	for p.accept("op", "|") {
		name := p.peek()
		if name.kind != "name" {
			return nil, fmt.Errorf("jinja: expected a filter name, got %q", name.s)
		}
		p.pos++
		var args []jExpr
		if p.accept("op", "(") {
			if args, err = p.args(); err != nil {
				return nil, err
			}
		}
		inner := e
		switch name.s {
		case "lower":
			if len(args) != 0 {
				return nil, errors.New("jinja: lower takes no arguments")
			}
			e = jFunc(func(s map[string]any) (any, error) {
				v, err := inner.eval(s)
				if err != nil {
					return nil, err
				}
				str, err := pyStr(v)
				return strings.ToLower(str), err
			})
		case "int":
			if len(args) > 1 {
				return nil, errors.New("jinja: int takes at most a default")
			}
			e = jFunc(func(s map[string]any) (any, error) {
				v, err := inner.eval(s)
				if err != nil {
					return nil, err
				}
				var def any = jInt(0)
				if len(args) == 1 {
					if def, err = args[0].eval(s); err != nil {
						return nil, err
					}
				}
				return pyIntFilter(v, def), nil
			})
		default:
			return nil, fmt.Errorf("jinja: unsupported filter %q", name.s)
		}
	}
	return e, nil
}

func (p *jParser) args() ([]jExpr, error) {
	var out []jExpr
	if p.accept("op", ")") {
		return out, nil
	}
	for {
		e, err := p.cond()
		if err != nil {
			return nil, err
		}
		out = append(out, e)
		if p.accept("op", ")") {
			return out, nil
		}
		if err := p.expect("op", ","); err != nil {
			return nil, err
		}
	}
}

func (p *jParser) postfix() (jExpr, error) {
	e, err := p.primary()
	if err != nil {
		return nil, err
	}
	for p.accept("op", ".") {
		name := p.peek()
		if name.kind != "name" {
			return nil, fmt.Errorf("jinja: expected an attribute, got %q", name.s)
		}
		p.pos++
		obj := e
		if p.accept("op", "(") {
			args, err := p.args()
			if err != nil {
				return nil, err
			}
			if name.s != "get" || len(args) < 1 || len(args) > 2 {
				return nil, fmt.Errorf("jinja: unsupported method %s/%d", name.s, len(args))
			}
			e = jFunc(func(s map[string]any) (any, error) {
				o, err := obj.eval(s)
				if err != nil {
					return nil, err
				}
				d, ok := o.(jDict)
				if !ok {
					return nil, fmt.Errorf("jinja: .get on %T", o)
				}
				k, err := args[0].eval(s)
				if err != nil {
					return nil, err
				}
				var def any
				if len(args) == 2 {
					if def, err = args[1].eval(s); err != nil {
						return nil, err
					}
				}
				key, isStr := k.(string)
				if !isStr {
					return def, nil // a non-string key matches no key of a string-keyed dict
				}
				if v, ok := d[key]; ok {
					return v, nil
				}
				return def, nil
			})
			continue
		}
		attr := name.s
		e = jFunc(func(s map[string]any) (any, error) {
			o, err := obj.eval(s)
			if err != nil {
				return nil, err
			}
			if _, undef := o.(jUndefined); undef {
				return nil, fmt.Errorf("jinja: %s of an undefined value", attr)
			}
			if d, ok := o.(jDict); ok {
				if v, ok := d[attr]; ok {
					return v, nil
				}
			}
			return jUndefined{}, nil
		})
	}
	return e, nil
}

func (p *jParser) primary() (jExpr, error) {
	t := p.peek()
	p.pos++
	switch {
	case t.kind == "str":
		return constant(t.s), nil
	case t.kind == "num":
		if strings.Contains(t.s, ".") {
			f, err := strconv.ParseFloat(t.s, 64)
			return constant(f), err
		}
		i, err := strconv.ParseInt(t.s, 10, 64)
		return constant(jInt(i)), err
	case t.kind == "op" && t.s == "-":
		inner, err := p.primary()
		if err != nil {
			return nil, err
		}
		return jFunc(func(s map[string]any) (any, error) {
			v, err := inner.eval(s)
			switch n := v.(type) {
			case jInt:
				return -n, err
			case float64:
				return -n, err
			}
			return nil, fmt.Errorf("jinja: unary minus on %T", v)
		}), nil
	case t.kind == "op" && t.s == "(":
		e, err := p.cond()
		if err != nil {
			return nil, err
		}
		return e, p.expect("op", ")")
	case t.kind == "op" && t.s == "{":
		type kv struct{ k, v jExpr }
		var items []kv
		if !p.accept("op", "}") {
			for {
				k, err := p.cond()
				if err != nil {
					return nil, err
				}
				if err := p.expect("op", ":"); err != nil {
					return nil, err
				}
				v, err := p.cond()
				if err != nil {
					return nil, err
				}
				items = append(items, kv{k, v})
				if p.accept("op", "}") {
					break
				}
				if err := p.expect("op", ","); err != nil {
					return nil, err
				}
			}
		}
		return jFunc(func(s map[string]any) (any, error) {
			d := jDict{}
			for _, it := range items {
				k, err := it.k.eval(s)
				if err != nil {
					return nil, err
				}
				key, ok := k.(string)
				if !ok {
					return nil, fmt.Errorf("jinja: dict key %T", k)
				}
				if d[key], err = it.v.eval(s); err != nil {
					return nil, err
				}
			}
			return d, nil
		}), nil
	case t.kind == "name":
		switch t.s {
		case "true", "True":
			return constant(true), nil
		case "false", "False":
			return constant(false), nil
		case "none", "None":
			return constant(nil), nil
		}
		name := t.s
		return jFunc(func(s map[string]any) (any, error) {
			if v, ok := s[name]; ok {
				return v, nil
			}
			return jUndefined{}, nil
		}), nil
	}
	return nil, fmt.Errorf("jinja: unexpected %q", t.s)
}

func constant(v any) jExpr { return jFunc(func(map[string]any) (any, error) { return v, nil }) }

// ---------------------------------------------------------------------------
// Python semantics
// ---------------------------------------------------------------------------

func truthy(v any) bool {
	switch x := v.(type) {
	case nil, jUndefined:
		return false
	case bool:
		return x
	case jInt:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	case jDict:
		return len(x) > 0
	case []any:
		return len(x) > 0
	}
	return true
}

func pyNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case jInt:
		return float64(x), true
	case float64:
		return x, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func pyCompare(op string, a, b any) (any, error) {
	fa, an := pyNumber(a)
	fb, bn := pyNumber(b)
	if an && bn {
		switch op {
		case "==":
			return fa == fb, nil
		case "!=":
			return fa != fb, nil
		case ">=":
			return fa >= fb, nil
		case "<=":
			return fa <= fb, nil
		case ">":
			return fa > fb, nil
		case "<":
			return fa < fb, nil
		}
	}
	sa, aok := a.(string)
	sb, bok := b.(string)
	switch op {
	case "==":
		return aok && bok && sa == sb, nil
	case "!=":
		return !aok || !bok || sa != sb, nil
	}
	return nil, fmt.Errorf("jinja: %T %s %T", a, op, b)
}

// pyIntFilter is Jinja's do_int with base 10.
func pyIntFilter(v, def any) any {
	switch x := v.(type) {
	case jInt:
		return x
	case bool:
		if x {
			return jInt(1)
		}
		return jInt(0)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return def
		}
		return jInt(int64(x))
	case string:
		t := strings.TrimSpace(x)
		if i, err := strconv.ParseInt(t, 10, 64); err == nil {
			return jInt(i)
		}
		if f, err := strconv.ParseFloat(t, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return jInt(int64(f))
		}
	}
	return def
}

// pyStr is Python's str().
func pyStr(v any) (string, error) {
	switch x := v.(type) {
	case jUndefined:
		return "", errors.New("jinja: rendering an undefined value")
	case string:
		return x, nil
	default:
		return pyRepr(v)
	}
}

// pyRepr is Python's repr() for the values JSON can produce.
func pyRepr(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "None", nil
	case bool:
		if x {
			return "True", nil
		}
		return "False", nil
	case jInt:
		return strconv.FormatInt(int64(x), 10), nil
	case float64:
		return pyFloatRepr(x), nil
	case string:
		return pyStrRepr(x), nil
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			s, err := pyRepr(e)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case jDict:
		// Python keeps insertion order, which for json.loads is document
		// order; Go's map has none. Sorted keys are deterministic and give
		// the same length, which is all an assertion here reads.
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			s, err := pyRepr(x[k])
			if err != nil {
				return "", err
			}
			parts[i] = pyStrRepr(k) + ": " + s
		}
		return "{" + strings.Join(parts, ", ") + "}", nil
	}
	return "", fmt.Errorf("jinja: cannot render %T", v)
}

func pyStrRepr(s string) string {
	quote := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = `"`
	}
	r := strings.ReplaceAll(s, `\`, `\\`)
	if quote == "'" {
		r = strings.ReplaceAll(r, "'", `\'`)
	}
	return quote + r + quote
}

// pyFloatRepr is Python's repr(float): the shortest round-tripping digits,
// fixed notation for decimal exponents -4..15 with at least one fractional
// digit, scientific with a two-digit exponent otherwise.
func pyFloatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	sci := strconv.FormatFloat(f, 'e', -1, 64) // -d.ddde±XX
	mant, expStr, _ := strings.Cut(sci, "e")
	exp, _ := strconv.Atoi(expStr)
	sign := ""
	if strings.HasPrefix(mant, "-") {
		sign, mant = "-", mant[1:]
	}
	digits := strings.Replace(mant, ".", "", 1)
	if exp < -4 || exp >= 16 {
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		es := "+"
		if exp < 0 {
			es, exp = "-", -exp
		}
		return fmt.Sprintf("%s%se%s%02d", sign, m, es, exp)
	}
	if exp < 0 {
		return sign + "0." + strings.Repeat("0", -exp-1) + digits
	}
	if len(digits) <= exp+1 {
		return sign + digits + strings.Repeat("0", exp+1-len(digits)) + ".0"
	}
	return sign + digits[:exp+1] + "." + digits[exp+1:]
}

// TestTheJinjaSubsetAgreesWithJinja pins the evaluator against outputs
// produced by Jinja2 3.1.6 itself for these exact inputs (default
// Undefined, `value_json` set only when the payload parses), so the
// contract test rests on an evaluator that was checked rather than assumed.
//
// It is stricter than Jinja in one place, on purpose: rendering an
// undefined value is an error here, where Jinja renders "" and Home
// Assistant logs a template warning and gives the entity no state.
func TestTheJinjaSubsetAgreesWithJinja(t *testing.T) {
	enum := `{% set m = {'A.On': 'Ein', 'A.Off': 'Aus', 'it\'s': 'x'} %}` +
		`{% if value_json is defined and value_json.val is not none %}{{ m.get(value_json.val, value_json.val) }}{% endif %}`
	cases := []struct {
		tmpl, payload, want string
		vars                map[string]any
	}{
		{`{{ value_json.val }}`, `{"val":true,"ts":1}`, "True", nil},
		{`{{ value_json.val | lower }}`, `{"val":true}`, "true", nil},
		{`{{ value_json.val | lower }}`, `{"val":false}`, "false", nil},
		{`{{ value_json.val }}`, `{"val":42}`, "42", nil},
		{`{{ value_json.val }}`, `{"val":2.5}`, "2.5", nil},
		{`{{ value_json.val }}`, `{"val":1e21}`, "1e+21", nil},
		{`{{ value_json.val }}`, `{"val":0.0001}`, "0.0001", nil},
		{`{{ value_json.val }}`, `{"val":0.00001}`, "1e-05", nil},
		{`{{ value_json.val }}`, `{"val":100.0}`, "100.0", nil},
		{`{{ value_json.val }}`, `{"val":"None"}`, "None", nil},
		{`{{ value_json.val }}`, `{"val":{"a":null,"b":[1,"x"]}}`, `{'a': None, 'b': [1, 'x']}`, nil},
		{enum, `{"val":"A.On"}`, "Ein", nil},
		{enum, `{"val":"A.Unknown"}`, "A.Unknown", nil},
		{enum, `{"val":"it's"}`, "x", nil},
		{enum, `not json`, "", nil},
		{`{% set m = {'Ein': 'A.On'} %}{{ m.get(value, value) }}`, ``, "A.On", map[string]any{"value": "Ein"}},
		{`{{ 'online' if value | int(0) >= 2 else 'offline' }}`, `2`, "online", nil},
		{`{{ 'online' if value | int(0) >= 2 else 'offline' }}`, `1`, "offline", nil},
		{`{{ 'online' if value | int(0) >= 2 else 'offline' }}`, `0`, "offline", nil},
		{`{{ 'online' if value | int(0) >= 2 else 'offline' }}`, `garbage`, "offline", nil},
		{`{{ 'online' if value | int(0) >= 2 else 'offline' }}`, `2.7`, "online", nil},
	}
	for _, c := range cases {
		got, err := renderTemplate(c.tmpl, []byte(c.payload), c.vars)
		if err != nil {
			t.Errorf("%s on %s: %v", c.tmpl, c.payload, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s on %s = %q, want %q", c.tmpl, c.payload, got, c.want)
		}
	}
	for _, bad := range []string{
		`{{ value_json.val | round }}`,
		`{{ value_json.val }}`, // against a payload with no val: undefined
		`{% for x in y %}{% endfor %}`,
		`{{ value_json.val.x }}`,
		`{{- value -}}`,
		`{{ states('sensor.x') }}`,
	} {
		if _, err := renderTemplate(bad, []byte(`{"ts":1}`), nil); err == nil {
			t.Errorf("%s: evaluated, want an error", bad)
		}
	}
}

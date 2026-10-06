package compose

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// interpolateNode substitutes ${VAR}-style references in every scalar value
// under n (mapping keys are left alone, as compose does), in place, and
// returns the names of variables that were referenced without a default
// but aren't set in vars. Supported forms match compose's:
//
//	$VAR ${VAR}           value, or "" if unset
//	${VAR:-default}       default if unset or empty
//	${VAR-default}        default if unset
//	${VAR:?message}       error if unset or empty
//	${VAR?message}        error if unset
//	${VAR:+replacement}   replacement if set and non-empty, else ""
//	${VAR+replacement}    replacement if set, else ""
//	$$                    a literal "$"
//
// Defaults/replacements may themselves contain references.
func interpolateNode(n *yaml.Node, vars map[string]string) ([]string, error) {
	in := interpolator{vars: vars, missing: map[string]bool{}, seen: map[*yaml.Node]bool{}}
	if err := in.walk(n); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(in.missing))
	for name := range in.missing {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

type interpolator struct {
	vars    map[string]string
	missing map[string]bool
	seen    map[*yaml.Node]bool
}

func (in *interpolator) walk(n *yaml.Node) error {
	if n == nil || in.seen[n] {
		return nil
	}
	in.seen[n] = true
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			if err := in.walk(c); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			if err := in.walk(n.Content[i]); err != nil {
				return err
			}
		}
	case yaml.AliasNode:
		return in.walk(n.Alias)
	case yaml.ScalarNode:
		if !strings.Contains(n.Value, "$") {
			return nil
		}
		v, err := in.expand(n.Value)
		if err != nil {
			return fmt.Errorf("line %d: %w", n.Line, err)
		}
		if v != n.Value {
			n.Value = v
			// A plain scalar like `replicas: ${N}` was resolved as !!str
			// while it still held the reference; clear the tag so it
			// re-resolves from the substituted value (here, !!int).
			if n.Style == 0 {
				n.Tag = ""
			}
		}
	}
	return nil
}

func (in *interpolator) expand(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '$' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		next := s[i+1]
		switch {
		case next == '$':
			b.WriteByte('$')
			i++
		case next == '{':
			end := matchingBrace(s, i+1)
			if end == -1 {
				return "", fmt.Errorf("unterminated variable reference in %q", s)
			}
			v, err := in.braced(s[i+2 : end])
			if err != nil {
				return "", err
			}
			b.WriteString(v)
			i = end
		case isNameStart(next):
			j := i + 1
			for j < len(s) && isNameChar(s[j]) {
				j++
			}
			b.WriteString(in.lookup(s[i+1 : j]))
			i = j - 1
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), nil
}

// braced evaluates the inside of a ${...} reference.
func (in *interpolator) braced(expr string) (string, error) {
	j := 0
	for j < len(expr) && isNameChar(expr[j]) {
		j++
	}
	name, rest := expr[:j], expr[j:]
	if name == "" || !isNameStart(name[0]) {
		return "", fmt.Errorf("invalid variable reference ${%s}", expr)
	}
	val, set := in.vars[name]
	if rest == "" {
		return in.lookup(name), nil
	}

	colon := strings.HasPrefix(rest, ":")
	op := rest
	if colon {
		op = rest[1:]
	}
	if op == "" {
		return "", fmt.Errorf("invalid variable reference ${%s}", expr)
	}
	arg := op[1:]
	present := set && (!colon || val != "")
	switch op[0] {
	case '-':
		if present {
			return val, nil
		}
		return in.expand(arg)
	case '?':
		if present {
			return val, nil
		}
		msg, err := in.expand(arg)
		if err != nil {
			return "", err
		}
		if msg == "" {
			msg = "is required"
		}
		return "", fmt.Errorf("variable %s: %s", name, msg)
	case '+':
		if present {
			return in.expand(arg)
		}
		return "", nil
	default:
		return "", fmt.Errorf("invalid variable reference ${%s}", expr)
	}
}

func (in *interpolator) lookup(name string) string {
	v, ok := in.vars[name]
	if !ok {
		in.missing[name] = true
	}
	return v
}

// matchingBrace returns the index of the "}" closing the "{" at s[open],
// honoring nested ${...} references, or -1.
func matchingBrace(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isNameChar(c byte) bool {
	return isNameStart(c) || (c >= '0' && c <= '9')
}

// ParseVars parses .env-style variable definitions: one KEY=value per line,
// blank lines and #-comments ignored, an optional leading "export ", and
// surrounding single/double quotes stripped from the value.
func ParseVars(text string) (map[string]string, error) {
	vars := map[string]string{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || !isNameStart(key[0]) || strings.IndexFunc(key, func(r rune) bool { return r > 127 || !isNameChar(byte(r)) }) != -1 {
			return nil, fmt.Errorf("variables line %d: expected KEY=value", i+1)
		}
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		vars[key] = val
	}
	return vars, nil
}

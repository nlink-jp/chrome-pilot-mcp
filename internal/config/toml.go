// Package config loads chrome-pilot-mcp's config.toml.
//
// It contains a hand-written parser for the TOML subset a config file
// needs — the zero-dependency policy (CLAUDE.md rule 1) rules out
// BurntSushi/toml. Unsupported syntax and unknown keys are rejected with
// a line number rather than silently ignored, so a typo never turns into
// a surprising default. See ADR-0002.
package config

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// value is one parsed scalar or array.
type value struct {
	line   int
	str    string
	num    int64
	b      bool
	list   []string
	kind   kind
	rawStr string
}

type kind int

const (
	kindString kind = iota
	kindInt
	kindBool
	kindStringList
)

// document is the parsed file: section → key → value.
type document map[string]map[string]value

// parse reads TOML-subset text.
//
// Supported: [section] headers, key = value pairs, # comments, basic
// strings with \\ \" \n \t escapes, integers, booleans, and single-line
// arrays of strings. Everything else is an error.
func parse(r io.Reader) (document, error) {
	doc := document{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)

	section := ""
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(stripComment(scanner.Text()))
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "[") {
			if strings.HasPrefix(line, "[[") {
				return nil, fmt.Errorf("line %d: unsupported TOML syntax (array of tables)", lineNo)
			}
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("line %d: malformed section header %q", lineNo, line)
			}
			name := strings.TrimSpace(line[1 : len(line)-1])
			if name == "" || strings.ContainsAny(name, ".\"' \t") {
				return nil, fmt.Errorf("line %d: unsupported section name %q (only simple [section] names)", lineNo, name)
			}
			section = name
			if _, ok := doc[section]; !ok {
				doc[section] = map[string]value{}
			}
			continue
		}

		key, rawVal, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected key = value, got %q", lineNo, line)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("line %d: empty key", lineNo)
		}
		if strings.ContainsAny(key, ".\"' \t") {
			return nil, fmt.Errorf("line %d: unsupported key %q (only simple keys, no dotted or quoted keys)", lineNo, key)
		}
		if section == "" {
			return nil, fmt.Errorf("line %d: key %q is outside any [section]", lineNo, key)
		}
		if _, dup := doc[section][key]; dup {
			return nil, fmt.Errorf("line %d: duplicate key %q in [%s]", lineNo, key, section)
		}

		v, err := parseValue(strings.TrimSpace(rawVal), lineNo)
		if err != nil {
			return nil, err
		}
		doc[section][key] = v
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return doc, nil
}

// stripComment removes a trailing # comment that is not inside a string.
func stripComment(line string) string {
	inString := false
	escaped := false
	for i, r := range line {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && inString:
			escaped = true
		case r == '"':
			inString = !inString
		case r == '#' && !inString:
			return line[:i]
		}
	}
	return line
}

func parseValue(raw string, lineNo int) (value, error) {
	v := value{line: lineNo, rawStr: raw}
	switch {
	case raw == "":
		return v, fmt.Errorf("line %d: missing value", lineNo)

	case strings.HasPrefix(raw, `"""`) || strings.HasPrefix(raw, "'''"):
		return v, fmt.Errorf("line %d: unsupported TOML syntax (multi-line string)", lineNo)

	case strings.HasPrefix(raw, "'"):
		return v, fmt.Errorf("line %d: unsupported TOML syntax (literal string); use a basic \"string\"", lineNo)

	case strings.HasPrefix(raw, `"`):
		s, err := parseBasicString(raw, lineNo)
		if err != nil {
			return v, err
		}
		v.kind, v.str = kindString, s
		return v, nil

	case strings.HasPrefix(raw, "{"):
		return v, fmt.Errorf("line %d: unsupported TOML syntax (inline table)", lineNo)

	case strings.HasPrefix(raw, "["):
		list, err := parseStringArray(raw, lineNo)
		if err != nil {
			return v, err
		}
		v.kind, v.list = kindStringList, list
		return v, nil

	case raw == "true" || raw == "false":
		v.kind, v.b = kindBool, raw == "true"
		return v, nil

	default:
		n, err := strconv.ParseInt(strings.ReplaceAll(raw, "_", ""), 10, 64)
		if err != nil {
			return v, fmt.Errorf("line %d: unsupported value %q (expected string, integer, boolean, or string array)", lineNo, raw)
		}
		v.kind, v.num = kindInt, n
		return v, nil
	}
}

// parseBasicString unquotes a basic string, which must span the whole
// remainder of the line.
func parseBasicString(raw string, lineNo int) (string, error) {
	var sb strings.Builder
	escaped := false
	for i := 1; i < len(raw); i++ {
		c := raw[i]
		if escaped {
			switch c {
			case '\\':
				sb.WriteByte('\\')
			case '"':
				sb.WriteByte('"')
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case 'r':
				sb.WriteByte('\r')
			default:
				return "", fmt.Errorf("line %d: unsupported escape \\%c", lineNo, c)
			}
			escaped = false
			continue
		}
		switch c {
		case '\\':
			escaped = true
		case '"':
			if rest := strings.TrimSpace(raw[i+1:]); rest != "" {
				return "", fmt.Errorf("line %d: trailing characters after string: %q", lineNo, rest)
			}
			return sb.String(), nil
		default:
			sb.WriteByte(c)
		}
	}
	return "", fmt.Errorf("line %d: unterminated string", lineNo)
}

// parseStringArray parses a single-line array of basic strings.
func parseStringArray(raw string, lineNo int) ([]string, error) {
	if !strings.HasSuffix(raw, "]") {
		return nil, fmt.Errorf("line %d: unsupported TOML syntax (multi-line array) or unterminated array", lineNo)
	}
	inner := strings.TrimSpace(raw[1 : len(raw)-1])
	if inner == "" {
		return []string{}, nil
	}
	var out []string
	for _, part := range splitTopLevel(inner) {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("line %d: empty element in array", lineNo)
		}
		if !strings.HasPrefix(part, `"`) {
			return nil, fmt.Errorf("line %d: array elements must be quoted strings, got %q", lineNo, part)
		}
		s, err := parseBasicString(part, lineNo)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// splitTopLevel splits on commas that are not inside a string.
func splitTopLevel(s string) []string {
	var parts []string
	start := 0
	inString := false
	escaped := false
	for i, r := range s {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && inString:
			escaped = true
		case r == '"':
			inString = !inString
		case r == ',' && !inString:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	tail := strings.TrimSpace(s[start:])
	if tail != "" {
		parts = append(parts, s[start:])
	}
	return parts
}

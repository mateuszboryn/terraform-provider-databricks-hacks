package provider

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// NormalizeYAML converts a metric view YAML definition into a canonical form so that
// definitions that differ only in formatting compare equal. Databricks re-serializes the
// YAML it stores (quotes keys like "on", folds long strings, drops comments), so a raw
// string compare would always show drift.
//
// Canonical form:
//   - comments, blank lines, indentation and quoting style are ignored
//   - map keys are sorted
//   - list order is preserved (dimension / measure order is meaningful)
//   - scalars become strings (so version: 1.1 == version: "1.1")
//   - whitespace inside strings is collapsed to single spaces and trimmed
//     (so folded `>` / literal `|` / single-line SQL compare equal)
//
// gopkg.in/yaml.v3 follows YAML 1.2 when decoding into interface{}, so `on:` stays the
// string "on" instead of turning into a boolean.
func NormalizeYAML(s string) (string, error) {
	var doc interface{}
	if err := yaml.Unmarshal([]byte(s), &doc); err != nil {
		return "", fmt.Errorf("invalid YAML: %w", err)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(normalizeNode(doc)); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func normalizeNode(n interface{}) interface{} {
	switch v := n.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for k, val := range v {
			out[k] = normalizeNode(val)
		}
		return out
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(v))
		for k, val := range v {
			out[fmt.Sprint(k)] = normalizeNode(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, val := range v {
			out[i] = normalizeNode(val)
		}
		return out
	case nil:
		return nil
	case string:
		return strings.Join(strings.Fields(v), " ")
	default:
		return strings.Join(strings.Fields(fmt.Sprint(v)), " ")
	}
}

// YAMLDiff returns a line diff of the canonical forms of two YAML documents, or "" if they
// are semantically equal. Unchanged runs longer than 2*context lines are collapsed.
func YAMLDiff(oldYAML, newYAML string) (string, error) {
	a, err := NormalizeYAML(oldYAML)
	if err != nil {
		return "", err
	}
	b, err := NormalizeYAML(newYAML)
	if err != nil {
		return "", err
	}
	if a == b {
		return "", nil
	}
	return lineDiff(strings.Split(strings.TrimRight(a, "\n"), "\n"), strings.Split(strings.TrimRight(b, "\n"), "\n"), 2), nil
}

type diffLine struct {
	op   byte // ' ', '-', '+'
	text string
}

// lineDiff computes an LCS-based line diff (definitions are small, O(n*m) is fine).
func lineDiff(a, b []string, context int) string {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var lines []diffLine
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			lines = append(lines, diffLine{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			lines = append(lines, diffLine{'-', a[i]})
			i++
		default:
			lines = append(lines, diffLine{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		lines = append(lines, diffLine{'-', a[i]})
	}
	for ; j < m; j++ {
		lines = append(lines, diffLine{'+', b[j]})
	}

	// keep only lines within `context` of a change
	keep := make([]bool, len(lines))
	for k, l := range lines {
		if l.op != ' ' {
			for c := k - context; c <= k+context; c++ {
				if c >= 0 && c < len(lines) {
					keep[c] = true
				}
			}
		}
	}

	var sb strings.Builder
	skipped := false
	for k, l := range lines {
		if !keep[k] {
			if !skipped {
				sb.WriteString("  ...\n")
				skipped = true
			}
			continue
		}
		skipped = false
		sb.WriteByte(l.op)
		sb.WriteByte(' ')
		sb.WriteString(l.text)
		sb.WriteByte('\n')
	}
	return sb.String()
}

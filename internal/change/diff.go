package change

import (
	"fmt"
	"strings"
)

func Unified(path, before, after string) string {
	if before == after {
		return ""
	}
	a, b := splitLines(before), splitLines(after)
	head := fmt.Sprintf("--- a%s\n+++ b%s\n", path, path)
	if len(a) > 2000 || len(b) > 2000 {
		return head + fmt.Sprintf("@@ large file: %d -> %d bytes @@\n", len(before), len(after))
	}
	// LCS 表
	n, m := len(a), len(b)
	l := make([][]int, n+1)
	for i := range l {
		l[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				l[i][j] = l[i+1][j+1] + 1
			} else {
				l[i][j] = max(l[i+1][j], l[i][j+1])
			}
		}
	}
	type op struct {
		c    byte
		text string
		ai   int
		bi   int
	}
	var ops []op
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, op{' ', a[i], i, j})
			i, j = i+1, j+1
		case l[i+1][j] >= l[i][j+1]:
			ops = append(ops, op{'-', a[i], i, j})
			i++
		default:
			ops = append(ops, op{'+', b[j], i, j})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, op{'-', a[i], i, j})
	}
	for ; j < m; j++ {
		ops = append(ops, op{'+', b[j], i, j})
	}
	// 按上下文 3 行切 hunk
	var out strings.Builder
	out.WriteString(head)
	const ctx = 3
	for k := 0; k < len(ops); {
		if ops[k].c == ' ' {
			k++
			continue
		}
		start := max(0, k-ctx)
		end := k
		for end < len(ops) {
			if ops[end].c != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].c == ' ' {
				run++
			}
			if run-end > 2*ctx || run == len(ops) {
				end = min(len(ops), end+ctx)
				break
			}
			end = run
		}
		var as, bs int
		for _, o := range ops[start:end] {
			if o.c != '+' {
				as++
			}
			if o.c != '-' {
				bs++
			}
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", ops[start].ai+1, as, ops[start].bi+1, bs)
		for _, o := range ops[start:end] {
			out.WriteByte(o.c)
			out.WriteString(o.text)
			out.WriteByte('\n')
		}
		k = end
	}
	return out.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package core

import "strings"

// Glob matches s against a pattern in which '*' stands for any run of
// characters, '/' and '.' included. Everything else matches itself.
func Glob(pattern, s string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == s
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, last) && len(s) >= len(last)
}

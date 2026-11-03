// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"path/filepath"
	"testing"
)

func TestFilePathOnWindows(t *testing.T) {
	cases := []struct{ name, goos, want string }{
		{"/C:/work/api/v1/models", "windows", "C:/work/api/v1/models"},
		{"/tmp/api/v1/models", "linux", "/tmp/api/v1/models"},
		{"/tmp/api/v1/models", "darwin", "/tmp/api/v1/models"},
	}
	for _, c := range cases {
		if got := filepath.ToSlash(filePath(c.name, c.goos)); got != c.want {
			t.Errorf("filePath(%q, %s) = %q, want %q", c.name, c.goos, got, c.want)
		}
	}
}

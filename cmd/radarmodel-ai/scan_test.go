// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/radarmodel/radarmodel/core"
)

func runScan(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(append([]string{"scan"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

const userConfig = `{
  "interval": "2h",
  "providers": [
    {"type": "openai"},
    {"type": "anthropic", "api_key_env": "MY_ANTHROPIC_KEY"}
  ],
  "watch": ["openai:gpt-4*"],
  "alerts": {"min_severity": "breaking", "webhook": {"url_env": "HOOK"}},
  "confirm_removals": true
}
`

func TestScanWriteKeepsTheConfig(t *testing.T) {
	dir := t.TempDir()
	code := filepath.Join(dir, "src")
	os.MkdirAll(code, 0o755)
	os.WriteFile(filepath.Join(code, "app.py"), []byte("a = 'gpt-4o'\nb = 'claude-sonnet-4-5'\nc = 'gemini-2.5-flash'\n"), 0o644)
	cfg := filepath.Join(dir, "radarmodel.json")
	os.WriteFile(cfg, []byte(userConfig), 0o600)

	// -check fails while the watch list misses a model.
	if c, out, _ := runScan(t, "-config", cfg, "-check", code); c != core.ExitBreaking || !hasRow(out, "anthropic:claude-sonnet-4-5 new") || !hasRow(out, "google:gemini-2.5-flash no google provider configured") {
		t.Fatalf("check: %d\n%s", c, out)
	}
	if c, _, errOut := runScan(t, "-config", cfg, "-write", code); c != core.ExitOK || !strings.Contains(errOut, "1 model added") {
		t.Fatalf("write: %d %s", c, errOut)
	}
	raw, _ := os.ReadFile(cfg)
	got := string(raw)
	for _, want := range []string{`"interval": "2h"`, `"api_key_env": "MY_ANTHROPIC_KEY"`, `"url_env": "HOOK"`, `"confirm_removals": true`,
		"\"watch\": [\n    \"openai:gpt-4*\",\n    \"anthropic:claude-sonnet-4-5\"\n  ]"} {
		if !strings.Contains(got, want) {
			t.Errorf("rewritten config lacks %s:\n%s", want, got)
		}
	}
	if strings.Index(got, `"interval"`) > strings.Index(got, `"providers"`) || strings.Index(got, `"alerts"`) > strings.Index(got, `"confirm_removals"`) {
		t.Errorf("key order changed:\n%s", got)
	}
	if info, _ := os.Stat(cfg); info.Mode().Perm() != 0o600 {
		t.Errorf("file mode changed to %v", info.Mode().Perm())
	}
	// A second write adds nothing; gemini still needs a provider, so -check
	// keeps failing until one is added.
	runScan(t, "-config", cfg, "-write", code)
	again, _ := os.ReadFile(cfg)
	if string(again) != got {
		t.Errorf("second write changed the file:\n%s", again)
	}
	if c, _, _ := runScan(t, "-config", cfg, "-check", code); c != core.ExitBreaking {
		t.Errorf("check with an unserved model: %d", c)
	}
	os.WriteFile(filepath.Join(code, "app.py"), []byte("a = 'gpt-4o'\nb = 'claude-sonnet-4-5'\n"), 0o644)
	if c, out, _ := runScan(t, "-config", cfg, "-check", code); c != core.ExitOK {
		t.Errorf("check with everything watched: %d\n%s", c, out)
	}
}

func TestScanWithoutConfigWritesOne(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "app.ts"), []byte("const m = 'claude-opus-4-1';\nconst r = \"meta-llama/llama-3.3-70b-instruct\";\n"), 0o644)
	cfg := filepath.Join(dir, "radarmodel.json")
	c, out, _ := runScan(t, "-config", cfg, "-json", dir)
	if c != core.ExitOK {
		t.Fatal(c)
	}
	var res struct {
		Models []struct{ Source, ID string } `json:"models"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res.Models) != 2 {
		t.Fatalf("%v %s", err, out)
	}
	if c, _, errOut := runScan(t, "-config", cfg, "-write", dir); c != core.ExitOK || !strings.Contains(errOut, "written to a new") {
		t.Fatalf("%d %s", c, errOut)
	}
	if _, w, _, err := load(cfg); err != nil || !w.Watched("anthropic", "claude-opus-4-1") || !w.Watched("openrouter", "meta-llama/llama-3.3-70b-instruct") {
		t.Fatalf("new config: %v", err)
	}
	raw, _ := os.ReadFile(cfg)
	if strings.Contains(string(raw), `"type": "google"`) || strings.Contains(string(raw), `"type": "openai"`) {
		t.Errorf("providers the code doesn't use were added:\n%s", raw)
	}
}

func TestScanRefusesBrokenConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "radarmodel.json")
	os.WriteFile(cfg, []byte(`{"providers": [{"type": "nope"}]}`), 0o644)
	before, _ := os.ReadFile(cfg)
	if c, _, _ := runScan(t, "-config", cfg, "-write", dir); c != core.ExitError {
		t.Errorf("broken config: exit %d", c)
	}
	after, _ := os.ReadFile(cfg)
	if !bytes.Equal(before, after) {
		t.Error("a broken config was rewritten")
	}
}

// hasRow reports whether a printed table row, with its padding collapsed,
// starts with want.
func hasRow(out, want string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.Join(strings.Fields(line), " "), want) {
			return true
		}
	}
	return false
}

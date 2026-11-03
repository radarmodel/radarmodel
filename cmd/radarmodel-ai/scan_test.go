// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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
	// Windows has no Unix permission bits to keep.
	if info, _ := os.Stat(cfg); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
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

// The config file names its watched models; the scan must not count them as
// code that uses them, or every watched model would look used forever.
func TestScanSkipsItsOwnConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "radarmodel.json")
	if err := os.WriteFile(cfg, []byte(`{"providers":[{"type":"openai"}],"watch":["openai:gpt-4o"],"state_dir":"state"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state", "models.json"), []byte(`{"id":"gpt-4.1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("m = \"gpt-4o-mini\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := run([]string{"scan", "-config", cfg, "-json", dir}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	s := out.String()
	if !strings.Contains(s, "gpt-4o-mini") || strings.Contains(s, `"gpt-4o"`) || strings.Contains(s, "gpt-4.1") {
		t.Fatalf("config or state counted as code:\n%s", s)
	}
}

// -check -write adds unwatched models, but a model with no source configured
// can't be added, so the check must still fail.
func TestScanCheckWriteStillFailsWithoutSource(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "radarmodel.json")
	if err := os.WriteFile(cfg, []byte(`{"providers":[{"type":"openai"}],"watch":["openai:gpt-4o"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("a = \"gpt-4o-mini\"\nb = \"claude-sonnet-4-5\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"scan", "-config", cfg, "-check", "-write", dir}, &out, &errOut); code != core.ExitBreaking {
		t.Fatalf("exit %d, want %d: %s%s", code, core.ExitBreaking, out.String(), errOut.String())
	}
	b, _ := os.ReadFile(cfg)
	if !strings.Contains(string(b), "openai:gpt-4o-mini") {
		t.Fatalf("gpt-4o-mini not written:\n%s", b)
	}
	// Only openai models used: written, so the check passes.
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("a = \"gpt-4o-mini\"\nc = \"gpt-4.1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run([]string{"scan", "-config", cfg, "-check", "-write", dir}, &out, &errOut); code != core.ExitOK {
		t.Fatalf("exit %d, want 0: %s", code, out.String())
	}
}

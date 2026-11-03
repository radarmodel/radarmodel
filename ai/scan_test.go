// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/radarmodel/radarmodel/core"
)

func TestRecognizeModel(t *testing.T) {
	yes := map[string]string{
		"gpt-4o": "openai:gpt-4o", "gpt-4.1-mini": "openai:gpt-4.1-mini", "o3": "openai:o3", "o4-mini": "openai:o4-mini",
		"text-embedding-3-small": "openai:text-embedding-3-small", "chatgpt-4o-latest": "openai:chatgpt-4o-latest",
		"claude-sonnet-4-5": "anthropic:claude-sonnet-4-5", "claude-3-5-haiku-20241022": "anthropic:claude-3-5-haiku-20241022",
		"gemini-2.5-flash": "google:gemini-2.5-flash", "models/gemini-2.5-pro": "google:gemini-2.5-pro",
		"text-embedding-004": "google:text-embedding-004", "gemma-3-27b-it": "google:gemma-3-27b-it",
		"openai:gpt-4o": "openai:gpt-4o", "anthropic:claude-opus-4-1": "anthropic:claude-opus-4-1",
		"openrouter:anthropic/claude-3.5-sonnet": "openrouter:anthropic/claude-3.5-sonnet",
		"anthropic/claude-3.5-sonnet":            "openrouter:anthropic/claude-3.5-sonnet", "meta-llama/llama-3.3-70b-instruct": "openrouter:meta-llama/llama-3.3-70b-instruct",
		"deepseek/deepseek-r1:free": "openrouter:deepseek/deepseek-r1:free", "gemini/gemini-2.0-flash": "openrouter:gemini/gemini-2.0-flash",
	}
	for in, want := range yes {
		c, ok := RecognizeModel(in)
		if !ok || c.Provider+":"+c.ID != want {
			t.Errorf("%q: got %v %+v, want %s", in, ok, c, want)
		}
	}
	for _, in := range []string{"", "gpt", "claude", "claude-code", "gemini-pro-vision-x", "Use gpt-4o here", "gpt-4o!", "openai", "o", "oauth",
		"https://example.com/gpt-4", "openai:claude-3-opus", "google:gpt-4o", "acme/gpt-4o", "anthropic/claude", "models/gpt-4o", "o2x", "GPT-4O",
		"node_modules/x", "a/b/c", "${MODEL}", "{model}"} {
		if c, ok := RecognizeModel(in); ok {
			t.Errorf("%q taken for a model: %+v", in, c)
		}
	}
	c, _ := RecognizeModel("openai/gpt-4o-mini")
	if c.VendorProvider != OpenAI || c.VendorID != "gpt-4o-mini" {
		t.Errorf("vendor form: %+v", c)
	}
	c, _ = RecognizeModel("anthropic/claude-3.5-sonnet")
	if c.VendorID != "" {
		t.Errorf("OpenRouter's dotted name offered as Anthropic's: %+v", c)
	}
}

func TestQuotedAndConfigValues(t *testing.T) {
	got := quoted(`model = "gpt-4o"; alt = 'claude-sonnet-4-5' ` + "`gemini-2.5-flash`" + ` don't 'x\'y'`)
	want := []string{"gpt-4o", "claude-sonnet-4-5", "gemini-2.5-flash", `x\'y`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("quoted: %q", got)
	}
	for in, want := range map[string]string{
		"model: gpt-4o": "gpt-4o", "MODEL=gpt-4o # main model": "gpt-4o", `model = "o3"`: "o3", "- claude-3-opus-20240229": "claude-3-opus-20240229",
		"- name: anthropic/claude-3-5-haiku-20241022": "anthropic/claude-3-5-haiku-20241022", "url: https://x.example": "", "plain": "",
	} {
		if v, _ := configValue(in); v != want {
			t.Errorf("%q: got %q, want %q", in, v, want)
		}
	}
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

var scanFixture = map[string]string{
	"app/llm.py": `MODEL = "gpt-4o-mini"
FALLBACK = 'claude-sonnet-4-5'
# MODEL = "gpt-3.5-turbo"   (a comment: not used)
"""Docs: we used to call gpt-4-turbo here."""
if name.startswith("gemini-2.5"):
    pass
if "o1" in name:
    pass
if name in ("gpt-5", "gpt-5-mini"):
    pass
model = raw.replace("gpt_4_1", "gpt-4.1")
MODELS = """
claude-3-haiku-20240307
"""
`,
	"web/client.ts":        "const m = `models/gemini-2.0-flash`;\n// const old = 'gpt-4';\nconst r = \"openai/gpt-4o\";\n",
	"config/settings.yaml": "llm:\n  model: claude-opus-4-1  # main\n  embed: \"text-embedding-3-small\"\nmodels:\n  - name: gemini/gemini-1.5-pro\n  - o3-mini\n",
	".env.example":         "OPENAI_MODEL=gpt-4.1\nANTHROPIC_MODEL=\n",
	"README.md":            "Works with gpt-4o and claude-opus-4-1.\n",
	"node_modules/x/i.js":  "const m = 'gpt-3.5-turbo';\n",
	"package-lock.json":    `{"name": "gpt-4"}`,
	".venv/lib/a.py":       "m = 'gpt-3.5-turbo'\n",
	"assets/blob.json":     "\x00\"gpt-3.5-turbo\"",
	"tests/test_llm.py":    "def test(): assert run('gpt-4o-mini')\n",
}

func ids(r ScanResult) []string {
	var out []string
	for _, f := range r.Found {
		e := f.Entry()
		if f.Source == "" {
			e = "?" + f.Provider + ":" + f.ID
		}
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

func TestScanFindsUsedModelsOnly(t *testing.T) {
	dir := writeTree(t, scanFixture)
	providers := []ProviderConfig{{Type: OpenAI}, {Type: Anthropic}, {Type: Google}}
	res, err := Scan([]string{dir}, providers, WatchList{}, nil, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"anthropic:claude-3-haiku-20240307", "anthropic:claude-opus-4-1", "anthropic:claude-sonnet-4-5",
		"google:gemini-1.5-pro", "google:gemini-2.0-flash",
		"openai:gpt-4.1", "openai:gpt-4o", "openai:gpt-4o-mini", "openai:gpt-5", "openai:gpt-5-mini", "openai:o3-mini", "openai:text-embedding-3-small",
	}
	if got := ids(res); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("found\n %v\nwant\n %v", got, want)
	}
	for _, f := range res.Found {
		if f.ID == "gpt-4o-mini" {
			if len(f.Locations) != 2 || f.Locations[0].File != "app/llm.py" || f.Locations[0].Line != 1 || f.Locations[1].File != "tests/test_llm.py" {
				t.Errorf("locations %+v", f.Locations)
			}
		}
	}
	// Excluding tests drops the model only they use from that location.
	res, _ = Scan([]string{dir}, providers, WatchList{}, nil, ScanOptions{Exclude: []string{"tests/*"}})
	for _, f := range res.Found {
		for _, l := range f.Locations {
			if strings.HasPrefix(l.File, "tests/") {
				t.Errorf("excluded file read: %v", l)
			}
		}
	}
}

func TestScanAssignsSources(t *testing.T) {
	dir := writeTree(t, map[string]string{"a.py": `a = "anthropic/claude-3.5-sonnet"
b = "openai/gpt-4o-mini"
c = "gemini/gemini-2.0-flash"
d = "claude-sonnet-4-5"
`})
	// With OpenRouter configured, vendor/model is OpenRouter's.
	res, _ := Scan([]string{dir}, []ProviderConfig{{Type: OpenRouter, Name: "router"}, {Type: OpenAI}}, WatchList{}, nil, ScanOptions{})
	if got := strings.Join(ids(res), " "); got != "?anthropic:claude-sonnet-4-5 google:gemini-2.0-flash router:anthropic/claude-3.5-sonnet router:openai/gpt-4o-mini" &&
		got != "?anthropic:claude-sonnet-4-5 ?google:gemini-2.0-flash router:anthropic/claude-3.5-sonnet router:openai/gpt-4o-mini" {
		t.Errorf("with OpenRouter: %s", got)
	}
	// Without it, the vendor's own API takes the vendor/model form (LiteLLM
	// style), when its name fits the vendor's own pattern.
	res, _ = Scan([]string{dir}, []ProviderConfig{{Type: OpenAI}, {Type: Anthropic}, {Type: Google}}, WatchList{}, nil, ScanOptions{})
	want := "?openrouter:anthropic/claude-3.5-sonnet anthropic:claude-sonnet-4-5 google:gemini-2.0-flash openai:gpt-4o-mini"
	if got := strings.Join(ids(res), " "); got != want {
		t.Errorf("without OpenRouter: %s, want %s", got, want)
	}
}

func TestScanMarksWatchedAndUnlisted(t *testing.T) {
	dir := writeTree(t, map[string]string{"a.py": "a = 'gpt-4o'\nb = 'gpt-o4-mini'\nc = 'claude-sonnet-4-5'\n"})
	providers := []ProviderConfig{{Type: OpenAI}, {Type: Anthropic}}
	w, err := ParseWatch([]string{"openai:gpt-4*"}, map[string]bool{OpenAI: true, Anthropic: true})
	if err != nil {
		t.Fatal(err)
	}
	store := &core.Store{Dir: t.TempDir()}
	taken := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if err := store.Save(&core.Snapshot{Source: OpenAI, Kind: Product, TakenAt: taken, Items: []core.Item{{ID: "gpt-4o"}, {ID: "o4-mini"}}}); err != nil {
		t.Fatal(err)
	}
	res, err := Scan([]string{dir}, providers, w, store, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Found{}
	for _, f := range res.Found {
		got[f.ID] = f
	}
	if f := got["gpt-4o"]; !f.Watched || f.Unlisted {
		t.Errorf("gpt-4o: %+v", f)
	}
	if f := got["gpt-o4-mini"]; f.Watched || !f.Unlisted || f.ListedAt == nil || !f.ListedAt.Equal(taken) {
		t.Errorf("a name OpenAI doesn't list: %+v", f)
	}
	if f := got["claude-sonnet-4-5"]; f.Watched || f.Unlisted {
		t.Errorf("no Anthropic snapshot: %+v", f)
	}
}

func TestScanRejectsBadInput(t *testing.T) {
	if _, err := Scan([]string{filepath.Join(t.TempDir(), "missing")}, nil, WatchList{}, nil, ScanOptions{}); err == nil {
		t.Error("a missing folder was accepted")
	}
	if _, err := Scan([]string{t.TempDir()}, nil, WatchList{}, nil, ScanOptions{Exclude: []string{"["}}); err == nil {
		t.Error("a bad exclude pattern was accepted")
	}
}

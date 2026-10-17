// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/radarmodel/radarmodel/core"
)

func TestFirstRunRecordsBaselineThenNothingChanges(t *testing.T) {
	h := newHarness(t, "openai:gpt-4o")
	r := h.run(false)
	for _, res := range r.Results {
		if !res.Baseline || res.Error != "" || len(res.Changes) != 0 || res.Items != 3 {
			t.Fatalf("first run, %s: %+v", res.Source, res)
		}
	}
	if r.ExitCode() != core.ExitOK {
		t.Fatalf("exit %d on baseline", r.ExitCode())
	}
	r = h.run(false)
	for _, res := range r.Results {
		if res.Baseline || res.Error != "" || len(res.Changes) != 0 {
			t.Fatalf("second run, %s: %+v", res.Source, res)
		}
	}
}

// The main promise, first half: a model the app uses going away is reported,
// on every provider.
func TestWatchedModelRemovedIsBreakingOnEveryProvider(t *testing.T) {
	h := newHarness(t, "openai:gpt-4o", "anthropic:claude-opus-4-1",
		"google:gemini-2.5-pro", "openrouter:openai/gpt-4o")
	h.run(false)
	h.fake.openai = []string{"gpt-4o-mini", "o3"}
	h.fake.anthropic = h.fake.anthropic[:2]
	h.fake.google = []googleModel{h.fake.google[0], h.fake.google[2]}
	h.fake.openrouter = []orModel{h.fake.openrouter[0], h.fake.openrouter[2]}
	r := h.run(false)
	for src, id := range map[string]string{"openai": "gpt-4o", "anthropic": "claude-opus-4-1",
		"google": "gemini-2.5-pro", "openrouter": "openai/gpt-4o"} {
		c := find(result(t, r, src), core.Removed, id, "")
		if c == nil || c.Severity != core.Breaking || !c.Watched {
			t.Errorf("%s: removal of %s: %+v\n%s", src, id, c, dump(r))
		}
		// Reported once as a removal, not again as unlisted in the same run.
		if find(result(t, r, src), core.Unlisted, id, "") != nil {
			t.Errorf("%s: %s reported as both removed and unlisted", src, id)
		}
	}
	if r.ExitCode() != core.ExitBreaking {
		t.Fatalf("exit %d, want %d", r.ExitCode(), core.ExitBreaking)
	}
	// The next run reminds as a warning: the model is still watched and still gone.
	r = h.run(false)
	c := find(result(t, r, "openai"), core.Unlisted, "gpt-4o", "")
	if c == nil || c.Severity != core.Warning {
		t.Fatalf("unlisted reminder: %+v\n%s", c, dump(r))
	}
	if r.ExitCode() != core.ExitOK {
		t.Fatalf("exit %d for a reminder", r.ExitCode())
	}
}

func TestUnwatchedChangesAreInfo(t *testing.T) {
	h := newHarness(t, "openai:gpt-4o")
	h.run(false)
	h.fake.openai = []string{"gpt-4o", "o3", "gpt-5"}
	r := h.run(false)
	res := result(t, r, "openai")
	gone, added := find(res, core.Removed, "gpt-4o-mini", ""), find(res, core.Added, "gpt-5", "")
	if gone == nil || gone.Severity != core.Info || added == nil || added.Severity != core.Info {
		t.Fatalf("%s", dump(r))
	}
	if r.ExitCode() != core.ExitOK {
		t.Fatalf("exit %d", r.ExitCode())
	}
}

func TestRenameIsNoted(t *testing.T) {
	h := newHarness(t, "anthropic:claude-opus-4-1")
	h.run(false)
	h.fake.anthropic[2] = [2]string{"claude-opus-4-1-20250805", "Claude Opus 4.1"}
	r := h.run(false)
	c := find(result(t, r, "anthropic"), core.Removed, "claude-opus-4-1", "")
	if c == nil || c.Severity != core.Breaking || !strings.Contains(c.Note, "claude-opus-4-1-20250805") {
		t.Fatalf("%+v\n%s", c, dump(r))
	}
}

func TestLimitAndPriceChanges(t *testing.T) {
	h := newHarness(t, "openrouter:anthropic/*", "openrouter:openai/gpt-4o", "google:gemini-2.5-flash")
	h.run(false)
	exp := "2026-12-31"
	h.fake.openrouter[0].Context = 200000                  // shrank: breaking
	h.fake.openrouter[0].In = "0.0000035"                  // up: warning
	h.fake.openrouter[1].Out = "0.000008"                  // down: info
	h.fake.openrouter[1].MaxOut = 32768                    // grew: info
	h.fake.openrouter[1].Expiration = &exp                 // retiring: warning
	h.fake.openrouter[2].Context = 8000                    // unwatched: info
	h.fake.google[0].Methods = []string{"generateContent"} // lost countTokens: warning
	h.fake.google[0].Out = 8192                            // shrank: breaking
	r := h.run(false)
	or, g := result(t, r, "openrouter"), result(t, r, "google")
	want := []struct {
		res       core.SourceResult
		id, field string
		sev       core.Severity
	}{
		{or, "anthropic/claude-sonnet-4.5", AttrContext, core.Breaking},
		{or, "anthropic/claude-sonnet-4.5", AttrInputPrice, core.Warning},
		{or, "openai/gpt-4o", AttrOutputPrice, core.Info},
		{or, "openai/gpt-4o", AttrMaxOutput, core.Info},
		{or, "openai/gpt-4o", AttrExpires, core.Warning},
		{or, "meta-llama/llama-3.3-70b-instruct", AttrContext, core.Info},
		{g, "gemini-2.5-flash", AttrMethods, core.Warning},
		{g, "gemini-2.5-flash", AttrOutputLimit, core.Breaking},
	}
	for _, w := range want {
		c := find(w.res, core.Changed, w.id, w.field)
		if c == nil || c.Severity != w.sev {
			t.Errorf("%s %s: got %+v, want %s", w.id, w.field, c, w.sev)
		}
	}
	if t.Failed() {
		t.Log(dump(r))
	}
}

func TestEquivalentNumbersAreNotChanges(t *testing.T) {
	h := newHarness(t, "openrouter:openai/gpt-4o")
	h.run(false)
	h.fake.openrouter[1].In = 2.5e-06       // same value as "0.0000025"
	h.fake.openrouter[1].Context = "128000" // same value as 128000
	h.fake.openrouter[1].MaxOut = 16384.0
	r := h.run(false)
	if n := len(result(t, r, "openrouter").Changes); n != 0 {
		t.Fatalf("%d spurious changes\n%s", n, dump(r))
	}
}

func TestUnlistedWatchedModelWarnsFromTheFirstRun(t *testing.T) {
	h := newHarness(t, "openai:gpt-4o-typo")
	r := h.run(false)
	c := find(result(t, r, "openai"), core.Unlisted, "gpt-4o-typo", "")
	if c == nil || c.Severity != core.Warning {
		t.Fatalf("%s", dump(r))
	}
}

func TestPaginationReadsEveryPage(t *testing.T) {
	h := newHarness(t, "anthropic:claude-opus-4-1", "google:text-embedding-004")
	h.fake.pageSize = 1
	r := h.run(false)
	if result(t, r, "anthropic").Items != 3 || result(t, r, "google").Items != 3 {
		t.Fatalf("%+v", r.Results)
	}
	r = h.run(false)
	if len(result(t, r, "anthropic").Changes)+len(result(t, r, "google").Changes) != 0 {
		t.Fatalf("%s", dump(r))
	}
}

func TestKeysGoInHeadersAndNeverIntoErrors(t *testing.T) {
	h := newHarness(t)
	h.fake.hook = func(w http.ResponseWriter, r *http.Request, n int) bool {
		http.Error(w, `{"error":"nope"}`, http.StatusUnauthorized)
		return true
	}
	r := h.run(false)
	for _, res := range r.Results {
		if res.Error == "" || strings.Contains(res.Error, "SECRET") {
			t.Fatalf("%s: %q", res.Source, res.Error)
		}
	}
	for _, req := range h.fake.seen {
		if strings.Contains(req.URL.String(), "SECRET") {
			t.Fatalf("key in URL: %s", req.URL)
		}
	}
	got := map[string]string{}
	for _, req := range h.fake.seen {
		switch {
		case req.Header.Get("x-goog-api-key") != "":
			got["google"] = req.Header.Get("x-goog-api-key")
		case req.Header.Get("x-api-key") != "":
			got["anthropic"] = req.Header.Get("x-api-key")
		case strings.HasPrefix(req.Header.Get("Authorization"), "Bearer sk-test-openai"):
			got["openai"] = req.Header.Get("Authorization")
		}
	}
	if len(got) != 3 {
		t.Fatalf("keys sent: %v", got)
	}
	// 401 is not retried.
	if h.fake.calls != 4 {
		t.Fatalf("%d calls for four providers' 401s", h.fake.calls)
	}
}

func TestMissingKeyFailsOnlyThatProvider(t *testing.T) {
	h := newHarness(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	r := h.run(false)
	if e := result(t, r, "anthropic").Error; !strings.Contains(e, "ANTHROPIC_API_KEY is not set") {
		t.Fatalf("anthropic: %q", e)
	}
	for _, s := range []string{"openai", "google", "openrouter"} {
		if res := result(t, r, s); res.Error != "" || !res.Baseline {
			t.Fatalf("%s: %+v", s, res)
		}
	}
	if r.ExitCode() != core.ExitError {
		t.Fatalf("exit %d", r.ExitCode())
	}
}

func TestDryRunKeepsTheStoredSnapshot(t *testing.T) {
	h := newHarness(t, "openai:gpt-4o")
	h.run(false)
	h.fake.openai = []string{"o3"}
	h.cfg.MinFraction = new(float64)
	for i := 0; i < 2; i++ {
		r := h.run(true)
		if c := find(result(t, r, "openai"), core.Removed, "gpt-4o", ""); c == nil {
			t.Fatalf("dry run %d: %s", i, dump(r))
		}
	}
	s, _ := h.store.Latest("openai")
	if len(s.Items) != 3 {
		t.Fatalf("dry run changed the stored snapshot: %d items", len(s.Items))
	}
}

func TestTwoProvidersOfOneType(t *testing.T) {
	env(t)
	_, srv := newFake(t)
	t.Setenv("AZURE_KEY", "k")
	cfg := &Config{Providers: []ProviderConfig{
		{Type: OpenAI, BaseURL: srv.URL},
		{Type: OpenAI, Name: "azure", BaseURL: srv.URL, APIKeyEnv: "AZURE_KEY"},
	}, Watch: []string{"azure:gpt-4o"}}
	w, err := cfg.Validate()
	if err != nil {
		t.Fatal(err)
	}
	r := Run(context.Background(), cfg, w, &core.Store{Dir: t.TempDir()}, Options{Fetcher: fastFetcher(nil)})
	if result(t, r, "openai").Error != "" || result(t, r, "azure").Error != "" {
		t.Fatalf("%+v", r.Results)
	}
	cfg.Providers[1].Name = ""
	if _, err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "used twice") {
		t.Fatalf("duplicate names accepted: %v", err)
	}
}

func TestConfigRejectsTypos(t *testing.T) {
	for _, c := range []struct {
		cfg  Config
		want string
	}{
		{Config{}, "no providers"},
		{Config{Providers: []ProviderConfig{{Type: "openia"}}}, "not one of"},
		{Config{Providers: []ProviderConfig{{Type: OpenAI}}, Watch: []string{"opneai:gpt-4o"}}, "isn't configured"},
		{Config{Providers: []ProviderConfig{{Type: OpenAI}}, Watch: []string{"gpt-4o"}}, "source:model"},
		{Config{Providers: []ProviderConfig{{Type: OpenAI}}, Interval: core.Duration(time.Second)}, "at least"},
		{Config{Providers: []ProviderConfig{{Type: OpenAI}}, MinFraction: ptr(1.5)}, "between 0 and 1"},
	} {
		if _, err := c.cfg.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: got %v, want %q", c.cfg, err, c.want)
		}
	}
}

func ptr(f float64) *float64 { return &f }

func TestGlob(t *testing.T) {
	for _, c := range []struct {
		p, s string
		ok   bool
	}{
		{"gpt-4o", "gpt-4o", true},
		{"gpt-4o", "gpt-4o-mini", false},
		{"gpt-4o*", "gpt-4o-mini", true},
		{"anthropic/*", "anthropic/claude-sonnet-4.5", true},
		{"anthropic/*", "openai/gpt-4o", false},
		{"*claude*", "anthropic/claude-sonnet-4.5:beta", true},
		{"*", "", true},
		{"a*b*c", "abc", true},
		{"a*b*c", "ac", false},
		{"ab*ba", "aba", false},
	} {
		if got := glob(c.p, c.s); got != c.ok {
			t.Errorf("glob(%q, %q) = %v", c.p, c.s, got)
		}
	}
}

// storedBytes reads a source's stored snapshot file, to prove a failed scan
// left it exactly as it was.
func storedBytes(t *testing.T, st *core.Store, source string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(st.Dir, "latest", source+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sameStored(t *testing.T, st *core.Store, source string, before []byte) {
	t.Helper()
	if after := storedBytes(t, st, source); !bytes.Equal(before, after) {
		t.Fatalf("%s: a failed scan changed the stored snapshot", source)
	}
}

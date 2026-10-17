// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
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
	gone := map[string]string{"openai": "gpt-4o", "anthropic": "claude-opus-4-1",
		"google": "gemini-2.5-pro", "openrouter": "openai/gpt-4o"}
	// The first scan that misses them warns; nothing breaks on one scan.
	r := h.run(false)
	for src, id := range gone {
		c := find(result(t, r, src), core.Removed, id, "")
		if c == nil || c.Severity != core.Warning || !c.Watched || !strings.Contains(c.Note, "next scan") {
			t.Errorf("%s: first miss of %s: %+v\n%s", src, id, c, dump(r))
		}
	}
	if r.ExitCode() != core.ExitOK {
		t.Fatalf("exit %d after one miss", r.ExitCode())
	}
	// The second scan confirms them as breaking.
	r = h.run(false)
	for src, id := range gone {
		c := find(result(t, r, src), core.Removed, id, "")
		if c == nil || c.Severity != core.Breaking || !c.Watched || !strings.Contains(c.Note, "confirmed") {
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
	h.cfg.ConfirmRemovals = new(bool) // report on the first scan
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

func TestAModelBackAfterOneMissIsNotARemoval(t *testing.T) {
	h := newHarness(t, "openai:gpt-4o")
	h.run(false)
	h.fake.openai = []string{"gpt-4o-mini", "o3"}
	if c := find(result(t, h.run(false), "openai"), core.Removed, "gpt-4o", ""); c == nil || c.Severity != core.Warning {
		t.Fatalf("first miss: %+v", c)
	}
	h.fake.openai = []string{"gpt-4o", "gpt-4o-mini", "o3"}
	r := h.run(false)
	res := result(t, r, "openai")
	back := find(res, core.Added, "gpt-4o", "")
	if back == nil || back.Severity != core.Info || r.ExitCode() != core.ExitOK {
		t.Fatalf("%s", dump(r))
	}
	// And the next scan is quiet: the pending mark is gone from the store.
	if r := h.run(false); len(result(t, r, "openai").Changes) != 0 {
		t.Fatalf("%s", dump(r))
	}
	s, _ := h.store.Latest("openai")
	for _, it := range s.Items {
		if it.Attrs[pendingAttr] != "" {
			t.Fatalf("pending mark left on %s", it.ID)
		}
	}
}

// A list that comes back short with no visible error (here Google drops its
// page token, so page two is silently missing) stays a warning for one scan.
func TestSilentlyShortListOnlyWarnsOnce(t *testing.T) {
	h := newHarness(t, "google:gemini-2.5-pro", "google:text-embedding-004")
	h.fake.pageSize = 2
	h.run(false)
	h.fake.hook = func(w http.ResponseWriter, r *http.Request, n int) bool {
		if r.URL.Path != "/v1beta/models" {
			return false
		}
		w.Write([]byte(`{"models":[{"name":"models/gemini-2.5-flash"},{"name":"models/gemini-2.5-pro"}]}`))
		return true
	}
	r := h.run(false)
	c := find(result(t, r, "google"), core.Removed, "text-embedding-004", "")
	if c == nil || c.Severity != core.Warning || r.ExitCode() != core.ExitOK {
		t.Fatalf("%s", dump(r))
	}
	h.fake.hook = nil
	if r := h.run(false); r.ExitCode() != core.ExitOK || find(result(t, r, "google"), core.Removed, "text-embedding-004", "") != nil {
		t.Fatalf("%s", dump(r))
	}
}

func TestAnthropicLimitsAndLifecycle(t *testing.T) {
	h := newHarness(t, "anthropic:claude-sonnet-4-5", "anthropic:claude-haiku-4-5")
	extra := map[string]map[string]any{}
	h.fake.anthropicExtra = extra
	extra["claude-sonnet-4-5"] = map[string]any{"max_input_tokens": 1000000, "max_tokens": 64000, "lifecycle": "active", "retires_at": nil}
	extra["claude-haiku-4-5"] = map[string]any{"max_input_tokens": 200000, "max_tokens": 64000, "lifecycle": "active", "retires_at": nil}
	h.run(false)
	extra["claude-sonnet-4-5"] = map[string]any{"max_input_tokens": 200000, "max_tokens": 64000, "lifecycle": "deprecated", "retires_at": "2027-03-01T00:00:00Z"}
	extra["claude-haiku-4-5"] = map[string]any{"max_input_tokens": 200000, "max_tokens": 64000, "lifecycle": "retired", "retires_at": nil}
	r := h.run(false)
	res := result(t, r, "anthropic")
	for _, w := range []struct {
		id, field string
		sev       core.Severity
	}{
		{"claude-sonnet-4-5", AttrContext, core.Breaking},
		{"claude-sonnet-4-5", AttrLifecycle, core.Warning},
		{"claude-sonnet-4-5", AttrExpires, core.Warning},
		{"claude-haiku-4-5", AttrLifecycle, core.Breaking},
	} {
		if c := find(res, core.Changed, w.id, w.field); c == nil || c.Severity != w.sev {
			t.Errorf("%s %s: %+v, want %s", w.id, w.field, c, w.sev)
		}
	}
	if t.Failed() {
		t.Log(dump(r))
	}
}

func TestOpenRouterPagingIsAFailedScan(t *testing.T) {
	for name, extra := range map[string]string{
		"links.next":  `,"links":{"next":"/api/v1/models?offset=3"}`,
		"total_count": `,"total_count":340`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, "openrouter:openai/gpt-4o")
			h.run(false)
			h.fake.hook = func(w http.ResponseWriter, r *http.Request, n int) bool {
				if r.URL.Path != "/api/v1/models" {
					return false
				}
				w.Write([]byte(`{"data":[{"id":"a/1"},{"id":"a/2"},{"id":"openai/gpt-4o"}]` + extra + `}`))
				return true
			}
			r := h.run(false)
			if res := result(t, r, "openrouter"); res.Error == "" || len(res.Changes) != 0 {
				t.Fatalf("%s", dump(r))
			}
		})
	}
}

func TestNonNumericPriceIsKeptNotFatal(t *testing.T) {
	h := newHarness(t, "openrouter:openai/gpt-4o")
	h.fake.openrouter[2].In = "varies"
	r := h.run(false)
	if res := result(t, r, "openrouter"); res.Error != "" {
		t.Fatal(res.Error)
	}
	s, _ := h.store.Latest("openrouter")
	if s.Index()["meta-llama/llama-3.3-70b-instruct"].Attrs[AttrInputPrice] != "varies" {
		t.Fatalf("%+v", s.Index()["meta-llama/llama-3.3-70b-instruct"])
	}
}

func TestSourceNamesThatWouldShareAFile(t *testing.T) {
	for _, pair := range [][2]string{{"OpenAI", "openai"}, {"my proxy", "my/proxy"}} {
		cfg := &Config{Providers: []ProviderConfig{{Type: OpenAI, Name: pair[0]}, {Type: OpenAI, Name: pair[1]}}}
		if _, err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "too close") {
			t.Errorf("%v: %v", pair, err)
		}
	}
}

func TestFailedSaveKeepsTheOldSnapshot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("folder permissions work differently on Windows")
	}
	h := newHarness(t, "openai:gpt-4o")
	h.run(false)
	before := storedBytes(t, h.store, "openai")
	hist := filepath.Join(h.store.Dir, "history", "openai")
	os.Chmod(hist, 0o500)
	t.Cleanup(func() { os.Chmod(hist, 0o755) })
	if f, err := os.Create(filepath.Join(hist, "probe")); err == nil {
		f.Close()
		t.Skip("running as a user who can write anywhere")
	}
	h.fake.openai = []string{"gpt-4o", "gpt-5", "o3"}
	r := h.run(false)
	if res := result(t, r, "openai"); !strings.Contains(res.Error, "saving the snapshot") || len(res.Changes) != 0 {
		t.Fatalf("%s", dump(r))
	}
	sameStored(t, h.store, "openai", before)
}

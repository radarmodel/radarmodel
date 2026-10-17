// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package ai

// The main promise, second half: RadarModel AI never reports a model as gone
// because the provider, the network or a proxy misbehaved. Every test here
// takes a baseline, breaks the next answer in one way, and checks three
// things: the source is reported as an error (not as changes), the stored
// snapshot is byte-for-byte unchanged, and once the provider behaves again
// the next scan reports no changes at all.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/radarmodel/radarmodel/core"
)

type breakage struct {
	name   string
	source string
	setup  func(h *harness)
	// hook breaks responses for the target provider; nil when setup does it.
	hook func(w http.ResponseWriter, r *http.Request) bool
	want string // substring of the error
}

func isProvider(r *http.Request, source string) bool {
	switch source {
	case "openai":
		return r.URL.Path == "/v1/models" && r.Header.Get("anthropic-version") == ""
	case "anthropic":
		return r.URL.Path == "/v1/models" && r.Header.Get("anthropic-version") != ""
	case "google":
		return r.URL.Path == "/v1beta/models"
	case "openrouter":
		return r.URL.Path == "/api/v1/models"
	}
	return false
}

func TestFailuresAreNeverReportedAsRemovals(t *testing.T) {
	cases := []breakage{
		{name: "server error on every attempt", source: "openai",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				http.Error(w, "upstream down", http.StatusInternalServerError)
				return true
			}, want: "500"},
		{name: "rate limited on every attempt", source: "anthropic",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				w.Header().Set("Retry-After", "7")
				http.Error(w, `{"error":"rate_limited"}`, http.StatusTooManyRequests)
				return true
			}, want: "429"},
		{name: "maintenance page with status 200", source: "openrouter",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				w.Header().Set("Content-Type", "text/html")
				w.Write([]byte("<html><body>Down for maintenance</body></html>"))
				return true
			}, want: "not valid JSON"},
		{name: "body cut off mid-list", source: "openai",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				w.Write([]byte(`{"object":"list","data":[{"id":"gpt-4o","object":"model"},{"id":"gpt-4o-mi`))
				return true
			}, want: "not valid JSON"},
		{name: "error object with status 200", source: "openai",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				w.Write([]byte(`{"error":{"message":"overloaded","type":"server_error"}}`))
				return true
			}, want: `no "data" list`},
		{name: "empty list", source: "anthropic",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				w.Write([]byte(`{"data":[],"has_more":false}`))
				return true
			}, want: "empty model list"},
		{name: "null list", source: "google",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				w.Write([]byte(`{"models":null}`))
				return true
			}, want: `no "models" list`},
		{name: "second page fails", source: "anthropic",
			setup: func(h *harness) { h.fake.pageSize = 1 },
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Query().Get("after_id") == "" {
					return false
				}
				http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
				return true
			}, want: "504"},
		{name: "last page fails", source: "google",
			setup: func(h *harness) { h.fake.pageSize = 1 },
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Query().Get("pageToken") != "p2" {
					return false
				}
				http.Error(w, "bad gateway", http.StatusBadGateway)
				return true
			}, want: "502"},
		{name: "has_more with no cursor", source: "anthropic",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				w.Write([]byte(`{"data":[{"id":"claude-sonnet-4-5","display_name":"Claude Sonnet 4.5"}],"has_more":true}`))
				return true
			}, want: "no next page"},
		{name: "page token that never changes", source: "google",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				w.Write([]byte(`{"models":[{"name":"models/gemini-2.5-flash"}],"nextPageToken":"same"}`))
				return true
			}, want: "repeats its page token"},
		{name: "list suddenly mostly empty", source: "openrouter",
			setup: func(h *harness) {
				// A big baseline, then an answer holding a fraction of it.
				for i := 0; i < 40; i++ {
					h.fake.openrouter = append(h.fake.openrouter, orModel{ID: "vendor/m" + string(rune('a'+i%26)) + strings.Repeat("x", i/26), Name: "M", Context: 1000, In: "0.1", Out: "0.2"})
				}
			},
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				w.Write([]byte(`{"data":[{"id":"openai/gpt-4o","name":"OpenAI: GPT-4o","context_length":128000,"pricing":{"prompt":"0.0000025","completion":"0.00001"},"top_provider":{"max_completion_tokens":16384}}]}`))
				return true
			}, want: "treated as a failed scan"},
		{name: "wrong type for a field", source: "openrouter",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				w.Write([]byte(`{"data":[{"id":"openai/gpt-4o","context_length":"lots"}]}`))
				return true
			}, want: "not valid JSON"},
		{name: "server too slow", source: "google",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				time.Sleep(2500 * time.Millisecond) // the test client gives up at 2s
				return false
			}, want: "Client.Timeout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, "openai:gpt-4o", "anthropic:claude-opus-4-1", "anthropic:claude-sonnet-4-5",
				"google:gemini-2.5-pro", "openrouter:openai/gpt-4o", "openrouter:anthropic/*")
			if c.setup != nil {
				c.setup(h)
			}
			if r := h.run(false); r.Errors() != 0 {
				t.Fatalf("baseline: %s", dump(r))
			}
			before := storedBytes(t, h.store, c.source)

			h.fake.hook = func(w http.ResponseWriter, r *http.Request, n int) bool {
				return isProvider(r, c.source) && c.hook(w, r)
			}
			r := h.run(false)
			res := result(t, r, c.source)
			if res.Error == "" || !strings.Contains(res.Error, c.want) {
				t.Fatalf("error %q, want it to mention %q\n%s", res.Error, c.want, dump(r))
			}
			if len(res.Changes) != 0 {
				t.Fatalf("a failed scan reported changes:\n%s", dump(r))
			}
			if r.ExitCode() != core.ExitError {
				t.Fatalf("exit %d, want %d", r.ExitCode(), core.ExitError)
			}
			sameStored(t, h.store, c.source, before)
			// The other providers were scanned normally.
			for _, other := range r.Results {
				if other.Source != c.source && other.Error != "" {
					t.Fatalf("%s failed too: %s", other.Source, other.Error)
				}
			}

			h.fake.hook = nil
			r = h.run(false)
			if r.Errors() != 0 || len(result(t, r, c.source).Changes) != 0 {
				t.Fatalf("after recovery:\n%s", dump(r))
			}
		})
	}
}

func TestTransientFailuresAreRetried(t *testing.T) {
	h := newHarness(t, "openai:gpt-4o")
	h.run(false)
	// First a rate limit with Retry-After, then a cut-off body, then fine.
	attempts := 0
	h.fake.hook = func(w http.ResponseWriter, r *http.Request, n int) bool {
		if !isProvider(r, "openai") {
			return false
		}
		attempts++
		switch attempts {
		case 1:
			w.Header().Set("Retry-After", "4")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return true
		case 2:
			w.Write([]byte(`{"data":[{"id":"gpt-4`))
			return true
		}
		return false
	}
	h.waits = nil
	r := h.run(false)
	if res := result(t, r, "openai"); res.Error != "" || len(res.Changes) != 0 {
		t.Fatalf("%s", dump(r))
	}
	if attempts != 3 {
		t.Fatalf("%d attempts", attempts)
	}
	if len(h.waits) != 2 || h.waits[0] != 4*time.Second {
		t.Fatalf("waits %v; want Retry-After's 4s first", h.waits)
	}
}

func TestUnreadableStoredSnapshotIsAnErrorNotANewBaseline(t *testing.T) {
	h := newHarness(t, "openai:gpt-4o")
	h.run(false)
	p := filepath.Join(h.store.Dir, "latest", "openai.json")
	if err := os.WriteFile(p, []byte(`{"source":"openai","items":[{"id":"gpt`), 0o644); err != nil {
		t.Fatal(err)
	}
	r := h.run(false)
	res := result(t, r, "openai")
	if !strings.Contains(res.Error, "unreadable") || res.Baseline {
		t.Fatalf("%+v", res)
	}
	if b, _ := os.ReadFile(p); !strings.HasSuffix(string(b), `"gpt`) {
		t.Fatal("the damaged file was overwritten; the evidence is gone")
	}
}

func TestAcceptingARealMassRemoval(t *testing.T) {
	h := newHarness(t, "openai:gpt-4o")
	h.cfg.Providers = h.cfg.Providers[:1]
	h.fake.openai = []string{"a", "b", "c", "d", "gpt-4o", "f", "g", "h", "i", "j"}
	h.run(false)
	h.fake.openai = []string{"gpt-4o", "k"}
	if r := h.run(false); r.Errors() != 1 {
		t.Fatalf("drop from 10 to 2 not held back:\n%s", dump(r))
	}
	h.cfg.MinFraction = ptr(0)
	r := h.run(false)
	res := result(t, r, "openai")
	if res.Error != "" || len(res.Changes) != 10 {
		t.Fatalf("with min_fraction 0: %s", dump(r))
	}
}

func TestCancelledRunLeavesSnapshotsAlone(t *testing.T) {
	h := newHarness(t, "openai:gpt-4o")
	h.run(false)
	before := storedBytes(t, h.store, "openai")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := Run(ctx, h.cfg, h.watch, h.store, Options{Fetcher: fastFetcher(nil)})
	for _, res := range r.Results {
		if res.Error == "" || len(res.Changes) != 0 {
			t.Fatalf("%s: %+v", res.Source, res)
		}
	}
	sameStored(t, h.store, "openai", before)
}

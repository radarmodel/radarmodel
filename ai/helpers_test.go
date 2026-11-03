// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/radarmodel/radarmodel/core"
)

// fakeAPI serves all nine providers' model-list endpoints from one server.
// Tests change its lists between runs, or set a hook to make a response
// fail in a chosen way.
type fakeAPI struct {
	mu        sync.Mutex
	openai    []string
	anthropic [][2]string // id, display name
	// anthropicExtra adds fields (limits, lifecycle) to Anthropic models by ID.
	anthropicExtra map[string]map[string]any
	google         []googleModel
	openrouter     []orModel
	// The OpenAI-style lists of Mistral, Groq and xAI, Azure's and Bedrock's,
	// as the JSON objects each API returns, served under /mistral, /groq,
	// /xai, /azure and /bedrock.
	mistral, groq, xai, azure, bedrock []map[string]any
	pageSize                           int // Anthropic and Google page size; 0 means everything on one page
	// hook may write a response itself and return true to stop the normal one.
	hook  func(w http.ResponseWriter, r *http.Request, n int) bool
	calls int
	seen  []*http.Request
}

type googleModel struct {
	ID, Name   string
	In, Out    int
	Methods    []string
	RawVersion string
}

type orModel struct {
	ID, Name    string
	Context     any
	MaxOut      any
	In, Out     any
	Expiration  *string
	ExtraFields map[string]any
}

func newFake(t *testing.T) (*fakeAPI, *httptest.Server) {
	t.Helper()
	f := &fakeAPI{
		openai:    []string{"gpt-4o", "gpt-4o-mini", "o3"},
		anthropic: [][2]string{{"claude-sonnet-4-5", "Claude Sonnet 4.5"}, {"claude-haiku-4-5", "Claude Haiku 4.5"}, {"claude-opus-4-1", "Claude Opus 4.1"}},
		google: []googleModel{
			{ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash", In: 1048576, Out: 65536, Methods: []string{"generateContent", "countTokens"}},
			{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", In: 1048576, Out: 65536, Methods: []string{"generateContent"}},
			{ID: "text-embedding-004", Name: "Text Embedding 004", In: 2048, Out: 1, Methods: []string{"embedContent"}},
		},
		openrouter: []orModel{
			{ID: "anthropic/claude-sonnet-4.5", Name: "Anthropic: Claude Sonnet 4.5", Context: 1000000, MaxOut: 64000, In: "0.000003", Out: "0.000015"},
			{ID: "openai/gpt-4o", Name: "OpenAI: GPT-4o", Context: 128000, MaxOut: 16384, In: "0.0000025", Out: "0.00001"},
			{ID: "meta-llama/llama-3.3-70b-instruct", Name: "Meta: Llama 3.3 70B", Context: 131072, MaxOut: nil, In: "0.0000001", Out: "0.00000032"},
		},
	}
	f.mistral = []map[string]any{
		{"id": "mistral-large-latest", "object": "model", "owned_by": "mistralai", "name": "mistral-large-2411", "max_context_length": 131072, "deprecation": nil},
		{"id": "mistral-small-2503", "object": "model", "owned_by": "mistralai", "max_context_length": 131072},
		{"id": "codestral-2501", "object": "model", "owned_by": "mistralai", "max_context_length": 262144},
	}
	f.groq = []map[string]any{
		{"id": "llama-3.3-70b-versatile", "object": "model", "owned_by": "Meta", "active": true, "context_window": 131072, "max_completion_tokens": 32768},
		{"id": "openai/gpt-oss-120b", "object": "model", "owned_by": "OpenAI", "active": true, "context_window": 131072, "max_completion_tokens": 65536},
		{"id": "qwen/qwen3-32b", "object": "model", "owned_by": "Alibaba Cloud", "active": true, "context_window": 131072, "max_completion_tokens": 40960},
	}
	f.xai = []map[string]any{
		{"id": "grok-4", "object": "model", "owned_by": "xai", "created": 1752019200},
		{"id": "grok-3-mini", "object": "model", "owned_by": "xai", "created": 1743724800},
		{"id": "grok-code-fast-1", "object": "model", "owned_by": "xai", "created": 1755993600},
	}
	f.azure = []map[string]any{
		{"id": "gpt-4o-2024-11-20", "object": "model", "status": "succeeded", "lifecycle_status": "generally-available", "created_at": 1732060800,
			"capabilities": map[string]any{"chat_completion": true, "inference": true}, "deprecation": map[string]any{"inference": 1798675200}},
		{"id": "gpt-4.1", "object": "model", "status": "succeeded", "lifecycle_status": "generally-available", "created_at": 1744675200,
			"deprecation": map[string]any{"inference": 1807833600}},
		{"id": "o3-mini", "object": "model", "status": "succeeded", "lifecycle_status": "preview", "created_at": 1738281600, "deprecation": map[string]any{}},
	}
	f.bedrock = []map[string]any{
		{"modelId": "anthropic.claude-sonnet-4-5-20250929-v1:0", "modelName": "Claude Sonnet 4.5", "providerName": "Anthropic",
			"modelLifecycle": map[string]any{"status": "ACTIVE"}},
		{"modelId": "meta.llama3-3-70b-instruct-v1:0", "modelName": "Llama 3.3 70B Instruct", "providerName": "Meta",
			"modelLifecycle": map[string]any{"status": "ACTIVE"}},
		{"modelId": "amazon.nova-pro-v1:0", "modelName": "Nova Pro", "providerName": "Amazon",
			"modelLifecycle": map[string]any{"status": "ACTIVE"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls++
	n, hook := f.calls, f.hook
	f.seen = append(f.seen, r.Clone(context.Background()))
	f.mu.Unlock()
	// The hook runs unlocked, so a slow one only delays its own provider.
	if hook != nil && hook(w, r, n) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/mistral/v1/models":
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": f.mistral})
	case "/groq/v1/models":
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": f.groq})
	case "/xai/v1/models":
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": f.xai})
	case "/azure/openai/models":
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": f.azure})
	case "/bedrock/foundation-models":
		json.NewEncoder(w).Encode(map[string]any{"modelSummaries": f.bedrock})
	case "/v1/models":
		if r.Header.Get("anthropic-version") != "" {
			f.serveAnthropic(w, r)
			return
		}
		var data []map[string]any
		for _, id := range f.openai {
			data = append(data, map[string]any{"id": id, "object": "model", "created": 1700000000, "owned_by": "system"})
		}
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	case "/v1beta/models":
		f.serveGoogle(w, r)
	case "/api/v1/models":
		var data []map[string]any
		for _, m := range f.openrouter {
			e := map[string]any{"id": m.ID, "name": m.Name, "context_length": m.Context,
				"pricing":      map[string]any{"prompt": m.In, "completion": m.Out, "request": "0"},
				"top_provider": map[string]any{"max_completion_tokens": m.MaxOut, "is_moderated": false}}
			if m.Expiration != nil {
				e["expiration_date"] = *m.Expiration
			}
			for k, v := range m.ExtraFields {
				e[k] = v
			}
			data = append(data, e)
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeAPI) serveAnthropic(w http.ResponseWriter, r *http.Request) {
	list := f.anthropic
	start := 0
	if a := r.URL.Query().Get("after_id"); a != "" {
		for i, m := range list {
			if m[0] == a {
				start = i + 1
			}
		}
	}
	end := len(list)
	if f.pageSize > 0 && start+f.pageSize < end {
		end = start + f.pageSize
	}
	var data []map[string]any
	for _, m := range list[start:end] {
		e := map[string]any{"id": m[0], "display_name": m[1], "type": "model", "created_at": "2025-09-29T00:00:00Z"}
		for k, v := range f.anthropicExtra[m[0]] {
			e[k] = v
		}
		data = append(data, e)
	}
	if data == nil {
		data = []map[string]any{}
	}
	body := map[string]any{"data": data, "has_more": end < len(list)}
	if len(data) > 0 {
		body["first_id"] = data[0]["id"]
		body["last_id"] = data[len(data)-1]["id"]
	}
	json.NewEncoder(w).Encode(body)
}

func (f *fakeAPI) serveGoogle(w http.ResponseWriter, r *http.Request) {
	list := f.google
	start := 0
	if tok := r.URL.Query().Get("pageToken"); tok != "" {
		fmt.Sscanf(tok, "p%d", &start)
	}
	end := len(list)
	if f.pageSize > 0 && start+f.pageSize < end {
		end = start + f.pageSize
	}
	var models []map[string]any
	for _, m := range list[start:end] {
		v := m.RawVersion
		if v == "" {
			v = "001"
		}
		models = append(models, map[string]any{"name": "models/" + m.ID, "version": v, "displayName": m.Name,
			"inputTokenLimit": m.In, "outputTokenLimit": m.Out, "supportedGenerationMethods": m.Methods})
	}
	body := map[string]any{"models": models}
	if end < len(list) {
		body["nextPageToken"] = fmt.Sprintf("p%d", end)
	}
	json.NewEncoder(w).Encode(body)
}

// env sets the provider keys for the duration of a test.
func env(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test-openai-SECRET")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-SECRET")
	t.Setenv("GEMINI_API_KEY", "AIza-test-SECRET")
	t.Setenv("MISTRAL_API_KEY", "mistral-test-SECRET")
	t.Setenv("GROQ_API_KEY", "gsk-test-SECRET")
	t.Setenv("XAI_API_KEY", "xai-test-SECRET")
	t.Setenv("AZURE_OPENAI_API_KEY", "azure-test-SECRET")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDTEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-test-SECRET")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_REGION", "us-west-2")
}

// config points every provider at the fake server.
func config(t *testing.T, base string, watch ...string) (*Config, WatchList) {
	t.Helper()
	cfg := &Config{
		Providers: []ProviderConfig{
			{Type: OpenAI, BaseURL: base},
			{Type: Anthropic, BaseURL: base},
			{Type: Google, BaseURL: base},
			{Type: OpenRouter, BaseURL: base},
			{Type: Mistral, BaseURL: base + "/mistral"},
			{Type: Groq, BaseURL: base + "/groq"},
			{Type: XAI, BaseURL: base + "/xai"},
			{Type: Azure, BaseURL: base + "/azure"},
			{Type: Bedrock, BaseURL: base + "/bedrock"},
		},
		Watch: watch,
	}
	w, err := cfg.Validate()
	if err != nil {
		t.Fatal(err)
	}
	return cfg, w
}

// fastFetcher retries like the real one but records waits instead of
// sleeping.
func fastFetcher(waits *[]time.Duration) *core.Fetcher {
	var mu sync.Mutex
	return &core.Fetcher{
		Client: core.NewHTTPClient(2 * time.Second),
		Sleep: func(ctx context.Context, d time.Duration) error {
			mu.Lock()
			defer mu.Unlock()
			if waits != nil {
				*waits = append(*waits, d)
			}
			return ctx.Err()
		},
	}
}

type harness struct {
	t     *testing.T
	fake  *fakeAPI
	cfg   *Config
	watch WatchList
	store *core.Store
	clock time.Time
	waits []time.Duration
}

func newHarness(t *testing.T, watch ...string) *harness {
	t.Helper()
	env(t)
	fake, srv := newFake(t)
	cfg, w := config(t, srv.URL, watch...)
	return &harness{t: t, fake: fake, cfg: cfg, watch: w,
		store: &core.Store{Dir: t.TempDir()}, clock: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
}

func (h *harness) run(dry bool) *core.Report {
	h.t.Helper()
	h.clock = h.clock.Add(time.Hour)
	now := h.clock
	return Run(context.Background(), h.cfg, h.watch, h.store, Options{DryRun: dry,
		Now: func() time.Time { return now }, Fetcher: fastFetcher(&h.waits)})
}

func result(t *testing.T, r *core.Report, source string) core.SourceResult {
	t.Helper()
	for _, res := range r.Results {
		if res.Source == source {
			return res
		}
	}
	t.Fatalf("no result for %s", source)
	return core.SourceResult{}
}

func find(res core.SourceResult, kind core.ChangeKind, id, field string) *core.Change {
	for i, c := range res.Changes {
		if c.Kind == kind && c.ID == id && c.Field == field {
			return &res.Changes[i]
		}
	}
	return nil
}

func dump(r *core.Report) string {
	var b strings.Builder
	r.WriteText(&b, core.Info)
	return b.String()
}

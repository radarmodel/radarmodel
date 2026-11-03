// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"strings"
	"testing"

	"github.com/radarmodel/radarmodel/core"
)

// The five providers added in 0.4: what each list says about a watched
// model is classified like the others.
func TestMoreProvidersAttributes(t *testing.T) {
	h := newHarness(t, "mistral:mistral-large-latest", "groq:llama-3.3-70b-versatile",
		"xai:grok-4", "azure:gpt-4.1", "bedrock:anthropic.claude-sonnet-4-5-20250929-v1:0",
		"bedrock:meta.llama3-3-70b-instruct-v1:0")
	r := h.run(false)
	for _, s := range []string{"mistral", "groq", "xai", "azure", "bedrock"} {
		if res := result(t, r, s); res.Error != "" || !res.Baseline {
			t.Fatalf("%s: %+v", s, res)
		}
	}
	snap, err := h.store.Latest("azure")
	if err != nil || snap == nil {
		t.Fatalf("azure snapshot: %v", err)
	}
	if got := snap.Index()["gpt-4.1"].Attrs[AttrExpires]; got != "2027-04-16" {
		t.Fatalf("azure retirement date %q", got)
	}

	h.fake.mistral[0]["deprecation"] = "2027-03-31T00:00:00Z"
	h.fake.groq[0]["active"] = false
	h.fake.groq[0]["context_window"] = 32768
	h.fake.azure[1]["deprecation"] = map[string]any{"inference": 1798675200}
	h.fake.azure[1]["lifecycle_status"] = "preview"
	h.fake.bedrock[0]["modelLifecycle"] = map[string]any{"status": "LEGACY", "endOfLifeTime": "2027-01-15T00:00:00Z"}
	h.fake.xai = h.fake.xai[1:]
	r = h.run(false)
	want := []struct {
		source, id, field string
		sev               core.Severity
	}{
		{"mistral", "mistral-large-latest", AttrExpires, core.Warning},
		{"groq", "llama-3.3-70b-versatile", AttrLifecycle, core.Warning},
		{"groq", "llama-3.3-70b-versatile", AttrContext, core.Breaking},
		{"azure", "gpt-4.1", AttrExpires, core.Warning},
		{"azure", "gpt-4.1", AttrStage, core.Info},
		{"bedrock", "anthropic.claude-sonnet-4-5-20250929-v1:0", AttrLifecycle, core.Warning},
		{"bedrock", "anthropic.claude-sonnet-4-5-20250929-v1:0", AttrExpires, core.Warning},
	}
	for _, w := range want {
		c := find(result(t, r, w.source), core.Changed, w.id, w.field)
		if c == nil || c.Severity != w.sev {
			t.Errorf("%s %s %s: %+v\n%s", w.source, w.id, w.field, c, dump(r))
		}
	}
	if c := find(result(t, r, "xai"), core.Removed, "grok-4", ""); c == nil || c.Severity != core.Warning {
		t.Errorf("xai removal on one scan: %+v", c)
	}
}

func TestMoreProvidersConfig(t *testing.T) {
	for _, tc := range []struct {
		cfg  Config
		want string
	}{
		{Config{Providers: []ProviderConfig{{Type: Azure}}}, "azure needs base_url"},
		{Config{Providers: []ProviderConfig{{Type: "vertex"}}}, "bedrock"},
	} {
		if _, err := tc.cfg.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v", tc.cfg.Providers, err)
		}
	}
	h := newHarness(t)
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	r := h.run(false)
	if e := result(t, r, "bedrock").Error; !strings.Contains(e, "region") {
		t.Fatalf("bedrock without a region: %q", e)
	}
	t.Setenv("AWS_REGION", "us-west-2")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	r = h.run(false)
	if e := result(t, r, "bedrock").Error; !strings.Contains(e, "AWS_SECRET_ACCESS_KEY") {
		t.Fatalf("bedrock without credentials: %q", e)
	}
}

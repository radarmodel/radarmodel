// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

// Package ai is RadarModel AI: it reads the model lists of AI providers,
// compares each with the last one it saw, and reports what changed for the
// models an application uses.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/radarmodel/radarmodel/core"
)

// Provider types RadarModel AI can read.
const (
	OpenAI     = "openai"
	Anthropic  = "anthropic"
	Google     = "google"
	OpenRouter = "openrouter"
)

var defaultBase = map[string]string{
	OpenAI:     "https://api.openai.com",
	Anthropic:  "https://api.anthropic.com",
	Google:     "https://generativelanguage.googleapis.com",
	OpenRouter: "https://openrouter.ai",
}

// defaultKeyEnv is the environment variable read for a provider's key when
// the config doesn't name one.
var defaultKeyEnv = map[string]string{
	OpenAI:    "OPENAI_API_KEY",
	Anthropic: "ANTHROPIC_API_KEY",
	Google:    "GEMINI_API_KEY",
}

// Attribute names. Limits are token counts; prices are US dollars per token,
// as OpenRouter publishes them.
const (
	AttrName          = "name"
	AttrOwner         = "owned_by"
	AttrVersion       = "version"
	AttrContext       = "context_length"
	AttrMaxOutput     = "max_output"
	AttrInputLimit    = "input_token_limit"
	AttrOutputLimit   = "output_token_limit"
	AttrInputPrice    = "input_price"
	AttrOutputPrice   = "output_price"
	AttrMethods       = "methods"
	AttrExpires       = "expires"
	AttrLifecycle     = "lifecycle"
	maxPages          = 200
	anthropicVersion  = "2023-06-01"
	anthropicPageSize = "1000"
)

// ProviderConfig is one model list to watch.
type ProviderConfig struct {
	// Type is openai, anthropic, google or openrouter.
	Type string `json:"type"`
	// Name is the source name used in reports, files and watch entries;
	// it defaults to Type. Two entries of the same type need different names.
	Name string `json:"name,omitempty"`
	// BaseURL overrides the provider's API address, for proxies and
	// compatible endpoints. file:// URLs read saved responses from disk.
	BaseURL string `json:"base_url,omitempty"`
	// APIKeyEnv names the environment variable holding the key. It defaults
	// to OPENAI_API_KEY, ANTHROPIC_API_KEY or GEMINI_API_KEY; OpenRouter's
	// list is public and needs no key.
	APIKeyEnv string `json:"api_key_env,omitempty"`
}

func (p ProviderConfig) source() string {
	if p.Name != "" {
		return p.Name
	}
	return p.Type
}

func (p ProviderConfig) base() string {
	b := p.BaseURL
	if b == "" {
		b = defaultBase[p.Type]
	}
	return strings.TrimRight(b, "/")
}

func (p ProviderConfig) key() (string, error) {
	env := p.APIKeyEnv
	if env == "" {
		env = defaultKeyEnv[p.Type]
	}
	if env == "" {
		return "", nil
	}
	v := os.Getenv(env)
	if v == "" && p.APIKeyEnv == "" && p.Type == OpenRouter {
		return "", nil
	}
	if v == "" && !strings.HasPrefix(p.base(), "file:") {
		return "", fmt.Errorf("%s is not set", env)
	}
	return v, nil
}

// fetchItems reads the provider's whole model list. Any failure, including a
// page in the middle of the list, fails the whole scan: a partial list would
// look like models disappearing.
func fetchItems(ctx context.Context, f *core.Fetcher, p ProviderConfig) ([]core.Item, error) {
	key, err := p.key()
	if err != nil {
		return nil, err
	}
	var items []core.Item
	switch p.Type {
	case OpenAI:
		items, err = fetchOpenAI(ctx, f, p.base(), key)
	case Anthropic:
		items, err = fetchAnthropic(ctx, f, p.base(), key)
	case Google:
		items, err = fetchGoogle(ctx, f, p.base(), key)
	case OpenRouter:
		items, err = fetchOpenRouter(ctx, f, p.base(), key)
	default:
		return nil, fmt.Errorf("unknown provider type %q", p.Type)
	}
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("the provider returned an empty model list; treated as a failed scan")
	}
	return items, nil
}

func fetchOpenAI(ctx context.Context, f *core.Fetcher, base, key string) ([]core.Item, error) {
	var body struct {
		Data *[]struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	h := map[string]string{}
	if key != "" {
		h["Authorization"] = "Bearer " + key
	}
	if err := f.GetJSON(ctx, base+"/v1/models", h, &body); err != nil {
		return nil, err
	}
	if body.Data == nil {
		return nil, fmt.Errorf("response has no \"data\" list")
	}
	var out []core.Item
	for _, m := range *body.Data {
		out = append(out, item(m.ID, AttrOwner, m.OwnedBy))
	}
	return out, nil
}

func fetchAnthropic(ctx context.Context, f *core.Fetcher, base, key string) ([]core.Item, error) {
	h := map[string]string{"anthropic-version": anthropicVersion}
	if key != "" {
		h["x-api-key"] = key
	}
	var out []core.Item
	after := ""
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("model list didn't end after %d pages", maxPages)
		}
		q := url.Values{"limit": {anthropicPageSize}}
		if after != "" {
			q.Set("after_id", after)
		}
		var body struct {
			Data *[]struct {
				ID             string  `json:"id"`
				DisplayName    string  `json:"display_name"`
				MaxInputTokens num     `json:"max_input_tokens"`
				MaxTokens      num     `json:"max_tokens"`
				Lifecycle      string  `json:"lifecycle"`
				RetiresAt      *string `json:"retires_at"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := f.GetJSON(ctx, base+"/v1/models?"+q.Encode(), h, &body); err != nil {
			return nil, err
		}
		if body.Data == nil {
			return nil, fmt.Errorf("response has no \"data\" list")
		}
		for _, m := range *body.Data {
			retires := ""
			if m.RetiresAt != nil {
				retires = *m.RetiresAt
			}
			out = append(out, item(m.ID,
				AttrName, m.DisplayName,
				AttrContext, string(m.MaxInputTokens),
				AttrMaxOutput, string(m.MaxTokens),
				AttrLifecycle, m.Lifecycle,
				AttrExpires, retires))
		}
		if !body.HasMore {
			return out, nil
		}
		if body.LastID == "" || body.LastID == after {
			return nil, fmt.Errorf("model list says there is more but gives no next page")
		}
		after = body.LastID
	}
}

func fetchGoogle(ctx context.Context, f *core.Fetcher, base, key string) ([]core.Item, error) {
	h := map[string]string{}
	if key != "" {
		// In a header, not the query string, so the key stays out of URLs.
		h["x-goog-api-key"] = key
	}
	var out []core.Item
	token := ""
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("model list didn't end after %d pages", maxPages)
		}
		q := url.Values{"pageSize": {"1000"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		var body struct {
			Models *[]struct {
				Name                       string   `json:"name"`
				Version                    string   `json:"version"`
				DisplayName                string   `json:"displayName"`
				InputTokenLimit            num      `json:"inputTokenLimit"`
				OutputTokenLimit           num      `json:"outputTokenLimit"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := f.GetJSON(ctx, base+"/v1beta/models?"+q.Encode(), h, &body); err != nil {
			return nil, err
		}
		if body.Models == nil {
			return nil, fmt.Errorf("response has no \"models\" list")
		}
		for _, m := range *body.Models {
			methods := append([]string(nil), m.SupportedGenerationMethods...)
			sort.Strings(methods)
			out = append(out, item(strings.TrimPrefix(m.Name, "models/"),
				AttrName, m.DisplayName,
				AttrVersion, m.Version,
				AttrInputLimit, string(m.InputTokenLimit),
				AttrOutputLimit, string(m.OutputTokenLimit),
				AttrMethods, strings.Join(methods, ",")))
		}
		if body.NextPageToken == "" {
			return out, nil
		}
		if body.NextPageToken == token {
			return nil, fmt.Errorf("model list repeats its page token")
		}
		token = body.NextPageToken
	}
}

func fetchOpenRouter(ctx context.Context, f *core.Fetcher, base, key string) ([]core.Item, error) {
	var body struct {
		Data *[]struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength num    `json:"context_length"`
			Pricing       struct {
				Prompt     num `json:"prompt"`
				Completion num `json:"completion"`
			} `json:"pricing"`
			TopProvider struct {
				MaxCompletionTokens num `json:"max_completion_tokens"`
			} `json:"top_provider"`
			ExpirationDate *string `json:"expiration_date"`
		} `json:"data"`
		TotalCount *int `json:"total_count"`
		Links      struct {
			Next *string `json:"next"`
		} `json:"links"`
	}
	h := map[string]string{}
	if key != "" {
		h["Authorization"] = "Bearer " + key
	}
	if err := f.GetJSON(ctx, base+"/api/v1/models", h, &body); err != nil {
		return nil, err
	}
	if body.Data == nil {
		return nil, fmt.Errorf("response has no \"data\" list")
	}
	// The full list comes back on one page; any sign of more pages means a
	// proxy or an API change cut it short.
	if body.Links.Next != nil && *body.Links.Next != "" {
		return nil, fmt.Errorf("the list is paged (links.next is set); a partial list would look like removals")
	}
	if body.TotalCount != nil && *body.TotalCount != len(*body.Data) {
		return nil, fmt.Errorf("the list holds %d models but total_count says %d", len(*body.Data), *body.TotalCount)
	}
	var out []core.Item
	for _, m := range *body.Data {
		exp := ""
		if m.ExpirationDate != nil {
			exp = *m.ExpirationDate
		}
		out = append(out, item(m.ID,
			AttrName, m.Name,
			AttrContext, string(m.ContextLength),
			AttrMaxOutput, string(m.TopProvider.MaxCompletionTokens),
			AttrInputPrice, string(m.Pricing.Prompt),
			AttrOutputPrice, string(m.Pricing.Completion),
			AttrExpires, exp))
	}
	return out, nil
}

// item builds an Item from alternating attribute names and values, leaving
// out empty values.
func item(id string, kv ...string) core.Item {
	it := core.Item{ID: id}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			if it.Attrs == nil {
				it.Attrs = map[string]string{}
			}
			it.Attrs[kv[i]] = kv[i+1]
		}
	}
	return it
}

// num reads a JSON number, a number in a string, or null, and keeps numbers
// in a canonical form, so "0.0000025" and 2.5e-06 compare equal. A string
// that isn't a number ("varies") is kept as it is rather than failing the
// whole scan over one model.
type num string

func (n *num) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if string(b) == "null" {
		*n = ""
		return nil
	}
	s := string(b)
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*n = ""
			return nil
		}
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		*n = num(strconv.FormatInt(i, 10))
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if len(b) > 0 && b[0] == '"' {
			*n = num(s)
			return nil
		}
		return fmt.Errorf("%s is not a number", b)
	}
	*n = num(strconv.FormatFloat(f, 'f', -1, 64))
	return nil
}

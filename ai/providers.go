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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/radarmodel/radarmodel/core"
)

// Provider types RadarModel AI can read.
const (
	OpenAI     = "openai"
	Anthropic  = "anthropic"
	Google     = "google"
	OpenRouter = "openrouter"
	Mistral    = "mistral"
	Groq       = "groq"
	XAI        = "xai"
	Azure      = "azure"
	Bedrock    = "bedrock"
)

// defaultBase is each provider's API address. Azure has none (each resource
// has its own, given as base_url) and Bedrock's depends on the region.
var defaultBase = map[string]string{
	OpenAI:     "https://api.openai.com",
	Anthropic:  "https://api.anthropic.com",
	Google:     "https://generativelanguage.googleapis.com",
	OpenRouter: "https://openrouter.ai",
	Mistral:    "https://api.mistral.ai",
	Groq:       "https://api.groq.com/openai",
	XAI:        "https://api.x.ai",
	Azure:      "",
	Bedrock:    "",
}

// providerTypes lists the types in the order messages name them.
var providerTypes = []string{OpenAI, Anthropic, Google, OpenRouter, Mistral, Groq, XAI, Azure, Bedrock}

// ProviderTypes returns the provider types RadarModel AI reads.
func ProviderTypes() []string { return append([]string(nil), providerTypes...) }

// azureAPIVersion is the GA version of Azure OpenAI's data-plane API whose
// Models - List this reads.
const azureAPIVersion = "2024-10-21"

// defaultKeyEnv is the environment variable read for a provider's key when
// the config doesn't name one.
var defaultKeyEnv = map[string]string{
	OpenAI:    "OPENAI_API_KEY",
	Anthropic: "ANTHROPIC_API_KEY",
	Google:    "GEMINI_API_KEY",
	Mistral:   "MISTRAL_API_KEY",
	Groq:      "GROQ_API_KEY",
	XAI:       "XAI_API_KEY",
	Azure:     "AZURE_OPENAI_API_KEY",
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
	AttrStage         = "stage"
	maxPages          = 200
	anthropicVersion  = "2023-06-01"
	anthropicPageSize = "1000"
)

// ProviderConfig is one model list to watch.
type ProviderConfig struct {
	// Type is openai, anthropic, google, openrouter, mistral, groq, xai,
	// azure or bedrock.
	Type string `json:"type"`
	// Name is the source name used in reports, files and watch entries;
	// it defaults to Type. Two entries of the same type need different names.
	Name string `json:"name,omitempty"`
	// BaseURL overrides the provider's API address, for proxies and
	// compatible endpoints. file:// URLs read saved responses from disk.
	BaseURL string `json:"base_url,omitempty"`
	// APIKeyEnv names the environment variable holding the key. It defaults
	// to the provider's usual variable (OPENAI_API_KEY, GROQ_API_KEY,
	// AZURE_OPENAI_API_KEY...); OpenRouter's list is public and needs no key.
	// Bedrock uses AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and
	// AWS_SESSION_TOKEN instead.
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// Region is Bedrock's AWS region; AWS_REGION or AWS_DEFAULT_REGION when
	// empty.
	Region string `json:"region,omitempty"`
}

// region is the Bedrock region to read.
func (p ProviderConfig) region() string {
	if p.Region != "" {
		return p.Region
	}
	if r := os.Getenv("AWS_REGION"); r != "" {
		return r
	}
	return os.Getenv("AWS_DEFAULT_REGION")
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
	if b == "" && p.Type == Bedrock && p.region() != "" {
		host := "amazonaws.com"
		if strings.HasPrefix(p.region(), "cn-") {
			host = "amazonaws.com.cn"
		}
		b = "https://bedrock." + p.region() + "." + host
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
	if p.Type == Bedrock {
		return checkEmpty(fetchBedrock(ctx, f, p))
	}
	if p.base() == "" {
		return nil, fmt.Errorf("%s needs a base_url (the resource's endpoint)", p.Type)
	}
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
	case Mistral, Groq, XAI:
		items, err = fetchCompatible(ctx, f, p.base(), key)
	case Azure:
		items, err = fetchAzure(ctx, f, p.base(), key)
	default:
		return nil, fmt.Errorf("unknown provider type %q", p.Type)
	}
	return checkEmpty(items, err)
}

func checkEmpty(items []core.Item, err error) ([]core.Item, error) {
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("the provider returned an empty model list; treated as a failed scan")
	}
	return items, nil
}

// fetchCompatible reads an OpenAI-style /v1/models list with the fields
// Mistral (max_context_length, deprecation) and Groq (context_window,
// max_completion_tokens, active) add; xAI adds none.
func fetchCompatible(ctx context.Context, f *core.Fetcher, base, key string) ([]core.Item, error) {
	var body struct {
		Data *[]struct {
			ID                  string  `json:"id"`
			OwnedBy             string  `json:"owned_by"`
			Name                string  `json:"name"`
			MaxContextLength    num     `json:"max_context_length"`
			ContextWindow       num     `json:"context_window"`
			MaxCompletionTokens num     `json:"max_completion_tokens"`
			Deprecation         *string `json:"deprecation"`
			Active              *bool   `json:"active"`
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
		ctxLen := string(m.ContextWindow)
		if ctxLen == "" {
			ctxLen = string(m.MaxContextLength)
		}
		dep, life := "", ""
		if m.Deprecation != nil {
			dep = isoDate(*m.Deprecation)
		}
		if m.Active != nil {
			life = "active"
			if !*m.Active {
				life = "inactive"
			}
		}
		out = append(out, item(m.ID,
			AttrName, m.Name,
			AttrOwner, m.OwnedBy,
			AttrContext, ctxLen,
			AttrMaxOutput, string(m.MaxCompletionTokens),
			AttrLifecycle, life,
			AttrExpires, dep))
	}
	return out, nil
}

// fetchAzure reads Azure OpenAI's Models - List: the base models a
// resource can deploy and its fine-tuned ones, with their retirement dates.
func fetchAzure(ctx context.Context, f *core.Fetcher, base, key string) ([]core.Item, error) {
	var body struct {
		Data *[]struct {
			ID              string `json:"id"`
			LifecycleStatus string `json:"lifecycle_status"`
			Deprecation     struct {
				Inference num `json:"inference"`
			} `json:"deprecation"`
		} `json:"data"`
	}
	h := map[string]string{}
	if key != "" {
		h["api-key"] = key
	}
	if err := f.GetJSON(ctx, base+"/openai/models?api-version="+azureAPIVersion, h, &body); err != nil {
		return nil, err
	}
	if body.Data == nil {
		return nil, fmt.Errorf("response has no \"data\" list")
	}
	var out []core.Item
	for _, m := range *body.Data {
		out = append(out, item(m.ID,
			AttrStage, m.LifecycleStatus,
			AttrExpires, epochDate(string(m.Deprecation.Inference))))
	}
	return out, nil
}

var awsRegion = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)

// isoDate keeps the date of an RFC 3339 time, so a change of format alone
// (fractional seconds, a different offset notation) isn't a new date.
func isoDate(s string) string {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t.UTC().Format("2006-01-02")
		}
	}
	return s
}

// epochDate turns Unix seconds into a date; anything else is kept as given.
func epochDate(s string) string {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return s
	}
	return time.Unix(n, 0).UTC().Format("2006-01-02")
}

// fetchBedrock reads Bedrock's ListFoundationModels, signed with the AWS
// credentials in the environment.
func fetchBedrock(ctx context.Context, f *core.Fetcher, p ProviderConfig) ([]core.Item, error) {
	base := p.base()
	if !strings.HasPrefix(base, "file:") {
		if p.region() == "" {
			return nil, fmt.Errorf("bedrock needs a region: set \"region\" or AWS_REGION")
		}
		if !awsRegion.MatchString(p.region()) {
			return nil, fmt.Errorf("bedrock region %q is not an AWS region name", p.region())
		}
	}
	creds := core.AWSCredentials{
		AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
	}
	u := base + "/foundation-models"
	h := map[string]string{}
	if !strings.HasPrefix(base, "file:") {
		if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
			return nil, fmt.Errorf("AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are not set")
		}
		var err error
		if h, err = core.SignAWSGet(u, creds, p.region(), "bedrock", time.Now()); err != nil {
			return nil, err
		}
	}
	var body struct {
		ModelSummaries *[]struct {
			ModelID        string `json:"modelId"`
			ModelName      string `json:"modelName"`
			ProviderName   string `json:"providerName"`
			ModelLifecycle struct {
				Status        string `json:"status"`
				EndOfLifeTime string `json:"endOfLifeTime"`
			} `json:"modelLifecycle"`
		} `json:"modelSummaries"`
	}
	if err := f.GetJSON(ctx, u, h, &body); err != nil {
		return nil, err
	}
	if body.ModelSummaries == nil {
		return nil, fmt.Errorf("response has no \"modelSummaries\" list")
	}
	var out []core.Item
	for _, m := range *body.ModelSummaries {
		out = append(out, item(m.ModelID,
			AttrName, m.ModelName,
			AttrOwner, m.ProviderName,
			AttrLifecycle, strings.ToLower(m.ModelLifecycle.Status),
			AttrExpires, isoDate(m.ModelLifecycle.EndOfLifeTime)))
	}
	return out, nil
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

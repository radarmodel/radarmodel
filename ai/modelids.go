// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"regexp"
	"strings"
)

// A model name is recognised by the shape each provider gives its IDs. Every
// pattern must match a whole string, and every family but o-series needs a
// digit, so prose ("use claude for this") and product names ("claude-code")
// are not taken for model IDs.
var idFamilies = []struct {
	provider string
	re       *regexp.Regexp
}{
	// Google before OpenAI: text-embedding-004 is Google's.
	{Google, regexp.MustCompile(`^(gemini-[0-9a-z][0-9a-z.\-]*|gemma-[0-9a-z][0-9a-z.\-]*|imagen-[0-9a-z][0-9a-z.\-]*|veo-[0-9a-z][0-9a-z.\-]*|text-embedding-00[0-9]|embedding-00[0-9]|text-multilingual-embedding-00[0-9]|learnlm-[0-9a-z.\-]+|aqa)$`)},
	{OpenAI, regexp.MustCompile(`^(gpt-[0-9a-z][0-9a-z.\-]*|chatgpt-[0-9a-z][0-9a-z.\-]*|o[13]|o[1-9]-[0-9a-z][0-9a-z.\-]*|text-embedding-[0-9a-z][0-9a-z.\-]*|dall-e-[23]|whisper-1|tts-1(-hd)?(-[0-9]{4})?|omni-moderation-[0-9a-z.\-]+|text-moderation-[0-9a-z.\-]+|davinci-002|babbage-002|computer-use-preview[0-9a-z.\-]*|codex-mini[0-9a-z.\-]*)$`)},
	// Anthropic's IDs have no dots (claude-3-5-sonnet); claude-3.5-sonnet
	// is how routers such as OpenRouter write it.
	{Anthropic, regexp.MustCompile(`^claude-[0-9a-z][0-9a-z\-]*$`)},
	{XAI, regexp.MustCompile(`^grok-[0-9a-z][0-9a-z.\-]*$`)},
	{Mistral, regexp.MustCompile(`^(mistral|codestral|pixtral|ministral|magistral|devstral|voxtral|open-mistral|open-mixtral)-[0-9a-z][0-9a-z.\-]*$`)},
	// Groq's own names for the open models it hosts.
	{Groq, regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]*-(versatile|instant)$`)},
}

// bedrockID matches Bedrock model IDs (anthropic.claude-sonnet-4-5-...-v1:0),
// with the region prefix of a cross-region inference profile (us., eu.,
// global.), which is left out of the ID: the foundation model list names the
// model without it.
var bedrockID = regexp.MustCompile(`^(?:(?:us|eu|apac|global|us-gov|jp|au|ca)\.)?((?:anthropic|meta|amazon|cohere|ai21|mistral|stability|deepseek|writer|qwen|openai|twelvelabs|luma)\.[a-z0-9][a-z0-9.\-]*(?::[0-9]+)?)$`)

// routedPrefixes are LiteLLM's provider prefixes (bedrock/..., azure/...)
// for the providers RadarModel reads; the rest is that provider's ID.
var routedPrefixes = map[string]string{
	"bedrock": Bedrock, "azure": Azure, "mistral": Mistral, "groq": Groq, "xai": XAI,
}

// openRouterVendors are the prefixes of OpenRouter IDs (vendor/model) that
// the scan recognises. A vendor/model string means OpenRouter when an
// OpenRouter source is configured; otherwise the vendor's own API when that
// is configured (the "openai/gpt-4o" form of LiteLLM and similar routers).
var openRouterVendors = map[string]string{
	"openai": OpenAI, "anthropic": Anthropic, "google": Google,
	"meta-llama": "", "mistralai": "", "deepseek": "", "qwen": "", "x-ai": "", "cohere": "",
	"perplexity": "", "nvidia": "", "microsoft": "", "amazon": "", "moonshotai": "", "z-ai": "",
	"nousresearch": "", "minimax": "", "inflection": "", "ai21": "", "liquid": "", "openrouter": "",
	// LiteLLM's prefix for Google's Gemini API; OpenRouter has no gemini/ vendor.
	"gemini": Google,
}

var routerID = regexp.MustCompile(`^([a-z0-9][a-z0-9\-]*)/([a-z0-9][a-z0-9.\-_]*(:[a-z]+)?)$`)

// fileExts are endings that make a string a file name (gpt-4o.png), not a
// model ID.
var fileExts = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "gif": true, "svg": true, "webp": true, "ico": true,
	"json": true, "yaml": true, "yml": true, "toml": true, "md": true, "txt": true, "csv": true, "log": true,
	"py": true, "js": true, "ts": true, "go": true, "rb": true, "html": true, "css": true, "pdf": true,
	"zip": true, "gz": true, "tar": true, "bin": true, "pt": true, "gguf": true, "safetensors": true, "onnx": true,
	"mp3": true, "mp4": true, "wav": true, "sh": true, "jsonl": true, "parquet": true, "pkl": true,
}

func fileName(s string) bool {
	i := strings.LastIndexByte(s, '.')
	return i > 0 && fileExts[strings.ToLower(s[i+1:])]
}

// hasDigit is true for families whose names carry a version.
func hasDigit(s string) bool { return strings.ContainsAny(s, "0123456789") }

// Candidate is a model name found in a string, before it is assigned to a
// configured source.
type Candidate struct {
	// Provider is the provider type the ID belongs to: openai, anthropic,
	// google, or openrouter for vendor/model IDs.
	Provider string
	// ID is the model ID as the provider's list names it.
	ID string
	// Vendor is set for vendor/model IDs: the vendor and the provider type
	// whose own API serves it, if RadarModel watches that API.
	Vendor, VendorProvider, VendorID string
}

// RecognizeModel says whether s, a whole string or config value, is a model
// ID, and whose. It accepts the forms code uses: the bare ID, Google's
// "models/" prefix, RadarModel's own "source:model", and vendor/model.
func RecognizeModel(s string) (Candidate, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 120 || strings.ContainsAny(s, " \t\"'`{}$%<>()[],;=\\") {
		return Candidate{}, false
	}
	if strings.HasPrefix(s, "models/") {
		if c, ok := RecognizeModel(strings.TrimPrefix(s, "models/")); ok && c.Provider == Google {
			return c, true
		}
		return Candidate{}, false
	}
	// source:model, as in RadarModel's own watch lists and some configs.
	if pre, rest, ok := strings.Cut(s, ":"); ok && !strings.Contains(pre, "/") {
		switch pre {
		case OpenAI, Anthropic, Google, Mistral, XAI, Bedrock:
			if c, ok := RecognizeModel(rest); ok && c.Provider == pre {
				return c, true
			}
			return Candidate{}, false
		case Groq, Azure:
			if c, ok := routed(pre, rest); ok {
				return c, true
			}
			return Candidate{}, false
		case OpenRouter:
			if c, ok := RecognizeModel(rest); ok && c.Provider == OpenRouter {
				return c, true
			}
			return Candidate{}, false
		}
	}
	if fileName(s) {
		return Candidate{}, false
	}
	if m := bedrockID.FindStringSubmatch(s); m != nil && hasDigit(m[1]) {
		return Candidate{Provider: Bedrock, ID: m[1]}, true
	}
	if pre, rest, ok := strings.Cut(s, "/"); ok {
		if p, known := routedPrefixes[pre]; known {
			return routed(p, rest)
		}
	}
	if m := routerID.FindStringSubmatch(s); m != nil {
		vendorProvider, known := openRouterVendors[m[1]]
		if !known || !(hasDigit(m[2]) || strings.HasSuffix(m[2], ":free")) {
			return Candidate{}, false
		}
		c := Candidate{Provider: OpenRouter, ID: s, Vendor: m[1]}
		if vendorProvider != "" {
			// The vendor's own ID: OpenRouter writes claude-3.5-sonnet where
			// Anthropic writes claude-3-5-sonnet, so only an ID the vendor's
			// own pattern accepts is offered.
			if own, ok := RecognizeModel(strings.SplitN(m[2], ":", 2)[0]); ok && own.Provider == vendorProvider {
				c.VendorProvider, c.VendorID = vendorProvider, own.ID
			}
		}
		return c, true
	}
	for _, f := range idFamilies {
		if !f.re.MatchString(s) {
			continue
		}
		if f.provider != OpenAI || !strings.HasPrefix(s, "o") {
			if !hasDigit(s) && s != "aqa" && !(f.provider == Mistral && strings.HasSuffix(s, "-latest")) {
				return Candidate{}, false
			}
		}
		return Candidate{Provider: f.provider, ID: s}, true
	}
	return Candidate{}, false
}

// routed recognises the ID after a LiteLLM provider prefix. Groq's IDs can
// carry a vendor (groq/meta-llama/llama-4-scout-...); an Azure deployment
// counts only when it is named like an OpenAI model.
func routed(provider, rest string) (Candidate, bool) {
	switch provider {
	case Groq:
		if routedName.MatchString(rest) && hasDigit(rest) {
			return Candidate{Provider: Groq, ID: rest}, true
		}
	case Azure:
		if c, ok := RecognizeModel(rest); ok && c.Provider == OpenAI {
			return Candidate{Provider: Azure, ID: c.ID}, true
		}
	default:
		if c, ok := RecognizeModel(rest); ok && c.Provider == provider {
			return c, true
		}
	}
	return Candidate{}, false
}

var routedName = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-_]*(/[a-z0-9][a-z0-9.\-_]*)?$`)

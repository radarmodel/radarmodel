// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

// Package radarmodel watches the models an application depends on and reports
// when one of them changes underneath it.
//
// RadarModel is two products on one engine. RadarModel AI watches the model
// lists of AI providers (OpenAI, Anthropic, Google, OpenRouter) and reports
// models that disappear, get renamed, or change their limits or prices.
// RadarModel Schema, which follows, watches database schemas for drift.
//
// The shared engine lives in package core: it takes a snapshot of a source,
// compares it with the last one, classifies each change as breaking, warning
// or info, and sends the result to the configured outputs.
package radarmodel

// Version is the release version. Raising it on main publishes a release; see
// RELEASING.md.
const Version = "0.2.0"

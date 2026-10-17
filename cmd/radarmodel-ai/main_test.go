// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/radarmodel/radarmodel"
	"github.com/radarmodel/radarmodel/core"
)

// openaiFake serves an OpenAI-style model list the test can change.
type openaiFake struct {
	mu     sync.Mutex
	models []string
	fail   bool
}

func (f *openaiFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	var data []map[string]string
	for _, m := range f.models {
		data = append(data, map[string]string{"id": m, "owned_by": "system"})
	}
	json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func setup(t *testing.T) (dir, cfg string, fake *openaiFake) {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("RADARMODEL_CONFIG", "")
	fake = &openaiFake{models: []string{"gpt-4o", "gpt-4o-mini", "o3"}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir = t.TempDir()
	cfg = filepath.Join(dir, "radarmodel.json")
	body := fmt.Sprintf(`{"providers":[{"type":"openai","base_url":%q}],"watch":["openai:gpt-4o"],"timeout":"5s"}`, srv.URL)
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, cfg, fake
}

func call(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestCheckExitCodes(t *testing.T) {
	dir, cfg, fake := setup(t)
	code, out, _ := call("check", "-config", cfg)
	if code != core.ExitOK || !strings.Contains(out, "baseline") {
		t.Fatalf("first check: %d\n%s", code, out)
	}
	// The state folder sits next to the config, wherever the command runs.
	if _, err := os.Stat(filepath.Join(dir, ".radarmodel", "latest", "openai.json")); err != nil {
		t.Fatal(err)
	}
	if code, _, _ = call("check", "-config", cfg); code != core.ExitOK {
		t.Fatalf("unchanged: exit %d", code)
	}

	fake.mu.Lock()
	fake.models = []string{"gpt-4o-mini", "o3"}
	fake.mu.Unlock()
	code, out, _ = call("check", "-config", cfg, "-dry-run")
	if code != core.ExitOK || !strings.Contains(out, "WARNING") || !strings.Contains(out, "gpt-4o *") {
		t.Fatalf("first miss: exit %d\n%s", code, out)
	}
	// The dry run kept the baseline, so this is a first miss again.
	if code, out, _ = call("check", "-config", cfg); code != core.ExitOK || !strings.Contains(out, "WARNING") {
		t.Fatalf("first miss: exit %d\n%s", code, out)
	}
	// The second miss is breaking.
	code, out, _ = call("check", "-config", cfg, "-json")
	if code != core.ExitBreaking {
		t.Fatalf("after dry run: exit %d", code)
	}
	var rep core.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil || rep.Results[0].Changes[0].ID != "gpt-4o" {
		t.Fatalf("json: %v\n%s", err, out)
	}

	fake.mu.Lock()
	fake.fail = true
	fake.mu.Unlock()
	code, out, _ = call("check", "-config", cfg)
	if code != core.ExitError || !strings.Contains(out, "ERROR") {
		t.Fatalf("outage: exit %d\n%s", code, out)
	}
}

func TestShow(t *testing.T) {
	_, cfg, _ := setup(t)
	if code, _, errs := call("show", "-config", cfg, "openai"); code != core.ExitError || !strings.Contains(errs, "no snapshot") {
		t.Fatalf("%d %s", code, errs)
	}
	call("check", "-config", cfg)
	code, out, _ := call("show", "-config", cfg, "openai")
	if code != core.ExitOK || !strings.Contains(out, "3 models") || !strings.Contains(out, "*  gpt-4o ") {
		t.Fatalf("%d\n%s", code, out)
	}
	if code, _, errs := call("show", "-config", cfg); code != core.ExitError || !strings.Contains(errs, "openai") {
		t.Fatalf("%d %s", code, errs)
	}
}

func TestInit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "radarmodel.json")
	if code, _, errs := call("init", "-config", p); code != core.ExitOK {
		t.Fatal(errs)
	}
	if code, _, errs := call("init", "-config", p); code != core.ExitError || !strings.Contains(errs, "exists") {
		t.Fatalf("overwrote: %d %s", code, errs)
	}
	// The starter config is valid.
	if _, _, _, err := load(p); err != nil {
		t.Fatal(err)
	}
}

func TestBadInput(t *testing.T) {
	_, cfg, _ := setup(t)
	for _, args := range [][]string{
		{},
		{"frobnicate"},
		{"check", "-config", cfg, "-min", "loud"},
		{"check", "-config", cfg, "extra"},
		{"check", "-config", filepath.Join(t.TempDir(), "missing.json")},
		{"watch", "-config", cfg, "-dry-run"},
	} {
		if code, _, _ := call(args...); code != core.ExitError {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

func TestVersion(t *testing.T) {
	code, out, _ := call("version")
	if code != 0 || out != "radarmodel-ai "+radarmodel.Version+"\n" {
		t.Fatalf("%q", out)
	}
}

func TestDropRepeats(t *testing.T) {
	sent := map[string]bool{}
	r := &core.Report{Results: []core.SourceResult{{Source: "a", Changes: []core.Change{
		{Source: "a", Kind: core.Unlisted, ID: "m", Severity: core.Warning},
		{Source: "a", Kind: core.Removed, ID: "n", Severity: core.Breaking},
	}}}}
	if got := dropRepeats(r, sent); len(got.Results[0].Changes) != 2 {
		t.Fatal("first time dropped something")
	}
	if got := dropRepeats(r, sent); len(got.Results[0].Changes) != 1 || got.Results[0].Changes[0].ID != "n" {
		t.Fatalf("second time: %+v", got.Results[0].Changes)
	}
	if len(r.Results[0].Changes) != 2 {
		t.Fatal("the terminal report lost a change")
	}
}

func TestLockedStoreRefusesASecondRun(t *testing.T) {
	dir, cfg, _ := setup(t)
	os.MkdirAll(filepath.Join(dir, ".radarmodel"), 0o755)
	os.WriteFile(filepath.Join(dir, ".radarmodel", "lock"), []byte("pid 1"), 0o644)
	code, _, errs := call("check", "-config", cfg)
	if code != core.ExitError || !strings.Contains(errs, "another run") {
		t.Fatalf("%d %s", code, errs)
	}
	os.Remove(filepath.Join(dir, ".radarmodel", "lock"))
	if code, _, errs := call("check", "-config", cfg); code != core.ExitOK {
		t.Fatalf("%d %s", code, errs)
	}
	if _, err := os.Stat(filepath.Join(dir, ".radarmodel", "lock")); err == nil {
		t.Fatal("lock left behind")
	}
}

func TestDropRepeatsForgetsAModelThatCameBack(t *testing.T) {
	sent := map[string]bool{}
	missing := &core.Report{Results: []core.SourceResult{{Source: "a", Changes: []core.Change{{Source: "a", Kind: core.Unlisted, ID: "m"}}}}}
	quiet := &core.Report{Results: []core.SourceResult{{Source: "a"}}}
	dropRepeats(missing, sent)
	dropRepeats(quiet, sent)
	if got := dropRepeats(missing, sent); len(got.Results[0].Changes) != 1 {
		t.Fatal("a model missing again after coming back wasn't alerted")
	}
}

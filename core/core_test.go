// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func snap(src string, items ...Item) *Snapshot {
	s := &Snapshot{Source: src, TakenAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Items: items}
	if err := s.Normalize(); err != nil {
		panic(err)
	}
	return s
}

func it(id string, kv ...string) Item {
	i := Item{ID: id, Attrs: map[string]string{}}
	for j := 0; j+1 < len(kv); j += 2 {
		i.Attrs[kv[j]] = kv[j+1]
	}
	return i
}

func TestDiff(t *testing.T) {
	old := snap("s", it("b", "x", "1", "y", "2"), it("a"), it("c", "z", "9"))
	new := snap("s", it("b", "x", "1", "y", "3", "w", "new"), it("d"), it("c"))
	got := Diff(old, new)
	want := []Change{
		{Source: "s", Kind: Removed, ID: "a"},
		{Source: "s", Kind: Changed, ID: "b", Field: "w", New: "new"},
		{Source: "s", Kind: Changed, ID: "b", Field: "y", Old: "2", New: "3"},
		{Source: "s", Kind: Changed, ID: "c", Field: "z", Old: "9"},
		{Source: "s", Kind: Added, ID: "d"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	if d := Diff(old, old); len(d) != 0 {
		t.Fatalf("snapshot differs from itself: %+v", d)
	}
}

func TestNormalizeRejectsDuplicatesAndEmptyIDs(t *testing.T) {
	for _, s := range []*Snapshot{
		{Source: "s", Items: []Item{{ID: "a"}, {ID: "a"}}},
		{Source: "s", Items: []Item{{ID: ""}}},
		{Items: []Item{{ID: "a"}}},
	} {
		if err := s.Normalize(); err == nil {
			t.Errorf("%+v accepted", s)
		}
	}
}

func TestStoreSaveLatestAndPrune(t *testing.T) {
	st := &Store{Dir: t.TempDir(), Keep: 3}
	if s, err := st.Latest("x/y"); s != nil || err != nil {
		t.Fatalf("empty store: %v %v", s, err)
	}
	for i := 0; i < 5; i++ {
		s := snap("x/y", it("m", "n", string(rune('0'+i))))
		s.TakenAt = s.TakenAt.Add(time.Duration(i) * time.Minute)
		if err := st.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.Latest("x/y")
	if err != nil || got.Items[0].Attrs["n"] != "4" {
		t.Fatalf("latest %+v %v", got, err)
	}
	hist, _ := os.ReadDir(filepath.Join(st.Dir, "history", "x_y"))
	if len(hist) != 3 {
		t.Fatalf("%d history files, want 3", len(hist))
	}
	// No temporary files left behind.
	filepath.Walk(st.Dir, func(p string, info os.FileInfo, err error) error {
		if strings.Contains(filepath.Base(p), ".tmp-") {
			t.Errorf("leftover %s", p)
		}
		return nil
	})
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{"openai": "openai", "a/b c": "a_b_c", "..": "_..", "": "_", "Ünï": "_n_"} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReportExitCodes(t *testing.T) {
	r := &Report{Results: []SourceResult{{Source: "a"}}}
	if r.ExitCode() != ExitOK {
		t.Fatal("empty report not OK")
	}
	r.Results[0].Changes = []Change{{Severity: Warning}}
	if r.ExitCode() != ExitOK {
		t.Fatal("warning fails the run")
	}
	r.Results = append(r.Results, SourceResult{Source: "b", Error: "down"})
	if r.ExitCode() != ExitError {
		t.Fatal("error not reported")
	}
	r.Results[0].Changes = append(r.Results[0].Changes, Change{Severity: Breaking})
	if r.ExitCode() != ExitBreaking {
		t.Fatal("breaking not reported")
	}
}

func TestReportText(t *testing.T) {
	r := &Report{Product: "P", Results: []SourceResult{
		{Source: "a", Items: 5, Changes: []Change{
			{Source: "a", Kind: Removed, ID: "m1", Severity: Breaking, Watched: true, Note: "possibly renamed to m2"},
			{Source: "a", Kind: Added, ID: "m2", Severity: Info},
		}},
		{Source: "b", Baseline: true, Items: 3},
		{Source: "c", Error: "GET x returned 500\nmore"},
		{Source: "d", Items: 2},
	}}
	var b bytes.Buffer
	r.WriteText(&b, Warning)
	out := b.String()
	for _, want := range []string{"BREAKING", "m1 *", "possibly renamed to m2", "1 more below warning",
		"baseline", "3 items recorded", "ERROR", "returned 500 more", "no changes (2 items)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "m2  new") {
		t.Errorf("info change shown at warning level:\n%s", out)
	}
	if h := Headline(r, Info); h != "P: 1 breaking, 1 info, 1 source(s) failed to scan" {
		t.Errorf("headline %q", h)
	}
}

func TestFetchRedactsKeys(t *testing.T) {
	got := Redact("https://user:pw@example.com/v1/models?key=AIzaSECRET&pageSize=5")
	if strings.Contains(got, "SECRET") || strings.Contains(got, "pw") || !strings.Contains(got, "pageSize=5") {
		t.Fatalf("%s", got)
	}
}

func TestFetchDoesNotRetryClientErrors(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer srv.Close()
	f := &Fetcher{Client: srv.Client(), Sleep: func(context.Context, time.Duration) error { return nil }}
	var v any
	err := f.GetJSON(context.Background(), srv.URL, nil, &v)
	var se *StatusError
	if err == nil || n != 1 || !asStatus(err, &se) || se.Status != 403 {
		t.Fatalf("err %v after %d calls", err, n)
	}
}

func asStatus(err error, se **StatusError) bool {
	for err != nil {
		if s, ok := err.(*StatusError); ok {
			*se = s
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestFetchRefusesHugeBodies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":"` + strings.Repeat("x", 2000) + `"}`))
	}))
	defer srv.Close()
	f := &Fetcher{Client: srv.Client(), MaxBody: 1000}
	var v any
	if err := f.GetJSON(context.Background(), srv.URL, nil, &v); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("%v", err)
	}
}

func TestFileURLsReadSavedResponses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file:// base URLs are for Linux and macOS")
	}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "v1"), 0o755)
	os.WriteFile(filepath.Join(dir, "v1", "models"), []byte(`{"ok":true}`), 0o644)
	f := &Fetcher{Client: NewHTTPClient(time.Second)}
	var v struct{ OK bool }
	u := "file://" + filepath.ToSlash(dir) + "/v1/models"
	if err := f.GetJSON(context.Background(), u, nil, &v); err != nil || !v.OK {
		t.Fatalf("%v %+v", err, v)
	}
}

func testReport() *Report {
	return &Report{Product: "RadarModel AI", Version: "0.0.0", At: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		Results: []SourceResult{{Source: "openai", Items: 2, Changes: []Change{
			{Source: "openai", Kind: Removed, ID: "gpt-4o", Severity: Breaking, Watched: true},
			{Source: "openai", Kind: Added, ID: "gpt-5", Severity: Info},
		}}}}
}

func TestWebhookAndSlack(t *testing.T) {
	var mu sync.Mutex
	got := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got[r.URL.Path] = b
		mu.Unlock()
	}))
	defer srv.Close()
	t.Setenv("HOOK", srv.URL+"/hook")
	t.Setenv("SLACK", srv.URL+"/slack")
	cfg := &AlertConfig{Webhook: &WebhookConfig{URLEnv: "HOOK"}, Slack: &SlackConfig{URLEnv: "SLACK"}}
	outs, min, err := cfg.Outputs(srv.Client())
	if err != nil || len(outs) != 2 || min != Warning {
		t.Fatalf("%v %d %v", err, len(outs), min)
	}
	if errs := Deliver(context.Background(), outs, testReport(), min); len(errs) != 0 {
		t.Fatal(errs)
	}
	var rep Report
	if err := json.Unmarshal(got["/hook"], &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Results[0].Changes) != 1 || rep.Results[0].Changes[0].ID != "gpt-4o" {
		t.Fatalf("webhook got %s", got["/hook"])
	}
	var sl struct{ Text string }
	json.Unmarshal(got["/slack"], &sl)
	if !strings.Contains(sl.Text, "1 breaking") || !strings.Contains(sl.Text, "gpt-4o") || strings.Contains(sl.Text, "gpt-5") {
		t.Fatalf("slack got %q", sl.Text)
	}
}

func TestNothingWorthSendingSendsNothing(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer srv.Close()
	outs, _, _ := (&AlertConfig{Webhook: &WebhookConfig{URL: srv.URL}}).Outputs(srv.Client())
	r := testReport()
	r.Results[0].Changes = r.Results[0].Changes[1:] // only the info change
	Deliver(context.Background(), outs, r, Warning)
	if calls != 0 {
		t.Fatalf("%d calls for an info-only report", calls)
	}
	r.Results = append(r.Results, SourceResult{Source: "x", Error: "down"})
	Deliver(context.Background(), outs, r, Warning)
	if calls != 1 {
		t.Fatalf("scan error not alerted")
	}
}

func TestFailingWebhookIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", 500)
	}))
	defer srv.Close()
	outs, _, _ := (&AlertConfig{Webhook: &WebhookConfig{URL: srv.URL}}).Outputs(srv.Client())
	if errs := Deliver(context.Background(), outs, testReport(), Warning); len(errs) != 1 || !strings.Contains(errs[0].Error(), "500") {
		t.Fatal(errs)
	}
}

func TestUnsetSecretIsAConfigError(t *testing.T) {
	t.Setenv("NOT_SET_ANYWHERE", "")
	for _, cfg := range []*AlertConfig{
		{Slack: &SlackConfig{URLEnv: "NOT_SET_ANYWHERE"}},
		{Webhook: &WebhookConfig{URLEnv: "NOT_SET_ANYWHERE"}},
		{Webhook: &WebhookConfig{}},
		{Email: &EmailConfig{Host: "h", From: "f", To: []string{"t"}, UsernameEnv: "NOT_SET_ANYWHERE"}},
		{Email: &EmailConfig{Host: "h"}},
		{MinSeverity: "loud"},
	} {
		if _, _, err := cfg.Outputs(http.DefaultClient); err == nil {
			t.Errorf("%+v accepted", cfg)
		}
	}
}

// fakeSMTP accepts one message and hands it over.
func fakeSMTP(t *testing.T) (addr string, msgs chan string) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	msgs = make(chan string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		rd := bufio.NewReader(c)
		say := func(s string) { c.Write([]byte(s + "\r\n")) }
		say("220 fake ESMTP")
		var data strings.Builder
		inData := false
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					msgs <- data.String()
					say("250 ok")
					continue
				}
				data.WriteString(line)
				continue
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				say("250 fake")
			case cmd == "DATA":
				inData = true
				say("354 go on")
			case cmd == "QUIT":
				say("221 bye")
				return
			default:
				say("250 ok")
			}
		}
	}()
	return l.Addr().String(), msgs
}

func TestEmail(t *testing.T) {
	addr, msgs := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(addr)
	var p int
	for _, ch := range port {
		p = p*10 + int(ch-'0')
	}
	cfg := &AlertConfig{Email: &EmailConfig{Host: host, Port: p, From: "radar@example.com", To: []string{"ops@example.com"}}}
	outs, min, err := cfg.Outputs(http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	r := testReport()
	r.Results[0].Changes[0].ID = ".gpt-4o" // a line starting with a dot survives
	if errs := Deliver(context.Background(), outs, r, min); len(errs) != 0 {
		t.Fatal(errs)
	}
	m := <-msgs
	for _, want := range []string{"Subject: RadarModel AI: 1 breaking", "To: ops@example.com", ".gpt-4o"} {
		if !strings.Contains(m, want) {
			t.Errorf("missing %q in\n%s", want, m)
		}
	}
}

func TestDurationJSON(t *testing.T) {
	var v struct{ D Duration }
	if err := json.Unmarshal([]byte(`{"D":"15m"}`), &v); err != nil || time.Duration(v.D) != 15*time.Minute {
		t.Fatal(err, v)
	}
	for _, bad := range []string{`{"D":15}`, `{"D":"-1m"}`, `{"D":"soon"}`} {
		if err := json.Unmarshal([]byte(bad), &v); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestLoadJSONRejectsUnknownFields(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte(`{"provders":[]}`), 0o644)
	var v struct{ Providers []string }
	if err := LoadJSON(p, &v); err == nil || !strings.Contains(err.Error(), "provders") {
		t.Fatal(err)
	}
}

func TestCrossHostRedirectIsRefused(t *testing.T) {
	var leaked string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("x-api-key")
		w.Write([]byte(`{}`))
	}))
	defer other.Close()
	// 127.0.0.1 and localhost are different hosts to the redirect check.
	target := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+"/v1/models", http.StatusFound)
	}))
	defer first.Close()
	f := &Fetcher{Client: NewHTTPClient(2 * time.Second), Attempts: 1}
	var v any
	err := f.GetJSON(context.Background(), first.URL+"/v1/models", map[string]string{"x-api-key": "SECRET"}, &v)
	if err == nil || !strings.Contains(err.Error(), "refusing a redirect") || leaked != "" {
		t.Fatalf("err %v, key seen by the other host: %q", err, leaked)
	}
}

func TestWebhookErrorsHideTheURL(t *testing.T) {
	outs, _, err := (&AlertConfig{Webhook: &WebhookConfig{URL: "http://127.0.0.1:1/services/T000/B000/SECRETTOKEN"}}).Outputs(NewAlertClient(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	errs := Deliver(context.Background(), outs, testReport(), Warning)
	if len(errs) != 1 || strings.Contains(errs[0].Error(), "SECRET") || !strings.Contains(errs[0].Error(), "127.0.0.1:1") {
		t.Fatalf("%v", errs)
	}
}

func TestAlertClientRefusesFileURLs(t *testing.T) {
	outs, _, _ := (&AlertConfig{Webhook: &WebhookConfig{URL: "file:///tmp/hook"}}).Outputs(NewAlertClient(time.Second))
	if errs := Deliver(context.Background(), outs, testReport(), Warning); len(errs) != 1 {
		t.Fatal("a file:// webhook counted as delivered")
	}
}

func TestLock(t *testing.T) {
	st := &Store{Dir: t.TempDir()}
	unlock, err := st.Lock(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Lock(time.Hour); err == nil || !strings.Contains(err.Error(), "another run") {
		t.Fatalf("second lock: %v", err)
	}
	unlock()
	unlock, err = st.Lock(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// A lock older than the stale limit is taken over.
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(filepath.Join(st.Dir, "lock"), old, old)
	unlock2, err := st.Lock(time.Hour)
	if err != nil {
		t.Fatalf("stale lock not taken over: %v", err)
	}
	unlock2()
	_ = unlock
}

func TestReservedNamesAndKeys(t *testing.T) {
	for in, want := range map[string]string{"con": "_con", "NUL.json": "_NUL.json", "com1": "_com1", "console": "console"} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
	if FileKey("OpenAI") != FileKey("openai") || FileKey("a b") != FileKey("a/b") {
		t.Fatal("FileKey doesn't fold")
	}
}

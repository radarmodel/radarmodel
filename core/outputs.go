// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

// AlertConfig says where reports go besides the terminal. Secrets are never
// written into the config file: each one names the environment variable that
// holds it.
type AlertConfig struct {
	// MinSeverity is the lowest severity that triggers an alert ("warning"
	// when empty). Scan errors always trigger one.
	MinSeverity string `json:"min_severity,omitempty"`

	Webhook *WebhookConfig `json:"webhook,omitempty"`
	Slack   *SlackConfig   `json:"slack,omitempty"`
	Email   *EmailConfig   `json:"email,omitempty"`
}

// WebhookConfig posts the JSON report to a URL.
type WebhookConfig struct {
	URL    string `json:"url,omitempty"`
	URLEnv string `json:"url_env,omitempty"`
}

// SlackConfig posts a text summary to a Slack incoming webhook.
type SlackConfig struct {
	URLEnv string `json:"url_env"`
}

// EmailConfig sends a text summary over SMTP (STARTTLS when the server
// offers it, as on port 587).
type EmailConfig struct {
	Host        string   `json:"host"`
	Port        int      `json:"port,omitempty"`
	From        string   `json:"from"`
	To          []string `json:"to"`
	UsernameEnv string   `json:"username_env,omitempty"`
	PasswordEnv string   `json:"password_env,omitempty"`
}

// Output delivers a report somewhere.
type Output interface {
	Name() string
	Send(ctx context.Context, r *Report, min Severity) error
}

// Outputs builds the configured outputs, reading secrets from the
// environment. A secret that is named but not set is an error, so a missing
// variable can't silently turn alerts off.
func (a *AlertConfig) Outputs(client *http.Client) ([]Output, Severity, error) {
	min := Warning
	if a == nil {
		return nil, min, nil
	}
	if a.MinSeverity != "" {
		s, err := ParseSeverity(a.MinSeverity)
		if err != nil {
			return nil, min, fmt.Errorf("alerts.min_severity: %v", err)
		}
		min = s
	}
	var outs []Output
	if w := a.Webhook; w != nil {
		u := w.URL
		if w.URLEnv != "" {
			v, err := need(w.URLEnv, "alerts.webhook.url_env")
			if err != nil {
				return nil, min, err
			}
			u = v
		}
		if u == "" {
			return nil, min, errors.New("alerts.webhook needs url or url_env")
		}
		outs = append(outs, &webhook{url: u, client: client})
	}
	if s := a.Slack; s != nil {
		u, err := need(s.URLEnv, "alerts.slack.url_env")
		if err != nil {
			return nil, min, err
		}
		outs = append(outs, &slack{url: u, client: client})
	}
	if e := a.Email; e != nil {
		if e.Host == "" || e.From == "" || len(e.To) == 0 {
			return nil, min, errors.New("alerts.email needs host, from and to")
		}
		m := &mailer{host: e.Host, port: e.Port, from: e.From, to: e.To}
		if m.port == 0 {
			m.port = 587
		}
		if e.UsernameEnv != "" {
			v, err := need(e.UsernameEnv, "alerts.email.username_env")
			if err != nil {
				return nil, min, err
			}
			m.user = v
			p, err := need(e.PasswordEnv, "alerts.email.password_env")
			if err != nil {
				return nil, min, err
			}
			m.pass = p
		}
		outs = append(outs, m)
	}
	return outs, min, nil
}

func need(env, field string) (string, error) {
	if env == "" {
		return "", fmt.Errorf("%s is empty", field)
	}
	v := os.Getenv(env)
	if v == "" {
		return "", fmt.Errorf("%s names %s, which is not set", field, env)
	}
	return v, nil
}

// Headline is a one-line summary of a report.
func Headline(r *Report, min Severity) string {
	counts := map[Severity]int{}
	for _, res := range r.Results {
		for _, c := range res.Changes {
			if c.Severity >= min {
				counts[c.Severity]++
			}
		}
	}
	var parts []string
	for _, s := range []Severity{Breaking, Warning, Info} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	if n := r.Errors(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d source(s) failed to scan", n))
	}
	if len(parts) == 0 {
		parts = append(parts, "no changes")
	}
	return fmt.Sprintf("%s: %s", r.Product, strings.Join(parts, ", "))
}

func textBody(r *Report, min Severity) string {
	var b bytes.Buffer
	b.WriteString(Headline(r, min) + "\n\n")
	r.Filtered(min).WriteText(&b, min)
	return b.String()
}

type webhook struct {
	url    string
	client *http.Client
}

func (w *webhook) Name() string { return "webhook" }
func (w *webhook) Send(ctx context.Context, r *Report, min Severity) error {
	var b bytes.Buffer
	if err := r.Filtered(min).WriteJSON(&b); err != nil {
		return err
	}
	return post(ctx, w.client, w.url, b.Bytes())
}

type slack struct {
	url    string
	client *http.Client
}

func (s *slack) Name() string { return "slack" }
func (s *slack) Send(ctx context.Context, r *Report, min Severity) error {
	b, _ := json.Marshal(map[string]string{"text": "```\n" + textBody(r, min) + "```"})
	return post(ctx, s.client, s.url, b)
}

func post(ctx context.Context, client *http.Client, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("POST returned %s", resp.Status)
	}
	return nil
}

type mailer struct {
	host       string
	port       int
	from       string
	to         []string
	user, pass string
}

func (m *mailer) Name() string { return "email" }
func (m *mailer) Send(ctx context.Context, r *Report, min Severity) error {
	msg := Message(m.from, m.to, Headline(r, min), textBody(r, min), r.At)
	addr := net.JoinHostPort(m.host, strconv.Itoa(m.port))
	var auth smtp.Auth
	if m.user != "" {
		auth = smtp.PlainAuth("", m.user, m.pass, m.host)
	}
	done := make(chan error, 1)
	go func() { done <- smtp.SendMail(addr, auth, m.from, m.to, msg) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Message builds a plain-text email.
func Message(from string, to []string, subject, body string, at time.Time) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", strings.NewReplacer("\r", " ", "\n", " ").Replace(subject))
	fmt.Fprintf(&b, "Date: %s\r\n", at.Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
	// net/smtp's data writer does the dot-stuffing.
	for _, line := range strings.Split(body, "\n") {
		b.WriteString(line + "\r\n")
	}
	return b.Bytes()
}

// Deliver sends the report to every output when it is worth sending,
// returning one error per output that failed.
func Deliver(ctx context.Context, outs []Output, r *Report, min Severity) []error {
	if !r.Worth(min) {
		return nil
	}
	var errs []error
	for _, o := range outs {
		if err := o.Send(ctx, r, min); err != nil {
			errs = append(errs, fmt.Errorf("%s alert: %v", o.Name(), err))
		}
	}
	return errs
}

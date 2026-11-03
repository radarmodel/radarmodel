// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AWSCredentials sign requests to AWS APIs.
type AWSCredentials struct {
	AccessKeyID, SecretAccessKey, SessionToken string
}

// SignAWSGet returns the headers that sign a GET of rawURL with AWS
// Signature Version 4 for the given region and service, at time t. Only
// what a body-less GET needs is implemented.
func SignAWSGet(rawURL string, c AWSCredentials, region, service string, t time.Time) (map[string]string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	t = t.UTC()
	amzDate := t.Format("20060102T150405Z")
	day := t.Format("20060102")
	h := map[string]string{"host": u.Host, "x-amz-date": amzDate}
	if c.SessionToken != "" {
		h["x-amz-security-token"] = c.SessionToken
	}
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		fmt.Fprintf(&canonHeaders, "%s:%s\n", k, strings.TrimSpace(h[k]))
	}
	signed := strings.Join(names, ";")
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	canon := strings.Join([]string{"GET", path, canonicalQuery(u.Query()), canonHeaders.String(), signed, sha256Hex("")}, "\n")
	scope := day + "/" + region + "/" + service + "/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex(canon)
	key := hmacSHA256([]byte("AWS4"+c.SecretAccessKey), day)
	for _, part := range []string{region, service, "aws4_request"} {
		key = hmacSHA256(key, part)
	}
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	out := map[string]string{
		"X-Amz-Date":    amzDate,
		"Authorization": fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", c.AccessKeyID, scope, signed, sig),
	}
	if c.SessionToken != "" {
		out["X-Amz-Security-Token"] = c.SessionToken
	}
	return out, nil
}

// canonicalQuery encodes the query the way SigV4 wants it: keys and values
// URI-encoded (spaces as %20), sorted by key, then value.
func canonicalQuery(q url.Values) string {
	var parts []string
	for k, vs := range q {
		for _, v := range vs {
			parts = append(parts, awsEscape(k)+"="+awsEscape(v))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "&")
}

func awsEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

// Copyright radarmodel.com 2026
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"
	"time"
)

// The expected signature was made by botocore's SigV4Auth for the same
// request, credentials and time.
func TestSignAWSGetMatchesBotocore(t *testing.T) {
	at, _ := time.Parse("20060102T150405Z", "20261103T170352Z")
	h, err := SignAWSGet("https://bedrock.us-east-1.amazonaws.com/foundation-models?byOutputModality=TEXT",
		AWSCredentials{"AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "SESSIONTOKEN"}, "us-east-1", "bedrock", at)
	if err != nil {
		t.Fatal(err)
	}
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261103/us-east-1/bedrock/aws4_request, SignedHeaders=host;x-amz-date;x-amz-security-token, Signature=09e6e4143f5813253502fbe7c3887ed0cd2fcdb698728e336c325cb8fca53532"
	if h["Authorization"] != want {
		t.Fatalf("Authorization\n got %s\nwant %s", h["Authorization"], want)
	}
	if h["X-Amz-Date"] != "20261103T170352Z" || h["X-Amz-Security-Token"] != "SESSIONTOKEN" {
		t.Fatalf("headers %v", h)
	}
}

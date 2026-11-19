package auth

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// buildSignedRequestFormat builds a valid SigV4-signed request.
// format "aws" uses ", " after commas (AWS SDK style); "exaws" uses "," (ExAws style).
func buildSignedRequestFormat(t *testing.T, accounts []Account, method, path string, query url.Values, amzDate, format string) *http.Request {
	t.Helper()

	accessKeyID := accounts[0].AccessKeyID
	secretAccessKey := accounts[0].SecretAccessKey
	dateStamp := amzDate[:8]
	region := "us-east-1"
	amzContentSha256 := "UNSIGNED-PAYLOAD"

	canonicalHeaders := "host:localhost\n"
	signedHeaders := "host"

	canonicalQueryString := query.Encode()

	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		method,
		path,
		canonicalQueryString,
		canonicalHeaders,
		signedHeaders,
		amzContentSha256,
	)

	scope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, region)
	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	)

	signingKey := deriveSigningKey(secretAccessKey, dateStamp, region, "s3")
	signature := fmt.Sprintf("%x", hmacSHA256(signingKey, []byte(stringToSign)))

	target, err := url.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	target.RawQuery = canonicalQueryString
	target.Host = "localhost"

	r := &http.Request{
		Method: method,
		Host:   "localhost",
		URL:    target,
		Header: make(http.Header),
	}
	r.Header.Set("X-Amz-Date", amzDate)
	r.Header.Set("X-Amz-Content-Sha256", amzContentSha256)

	credential := fmt.Sprintf("%s/%s/%s/s3/aws4_request", accessKeyID, dateStamp, region)
	if format == "aws" {
		r.Header.Set("Authorization", fmt.Sprintf(
			"AWS4-HMAC-SHA256 Credential=%s, SignedHeaders=%s, Signature=%s",
			credential, signedHeaders, signature))
	} else {
		r.Header.Set("Authorization", fmt.Sprintf(
			"AWS4-HMAC-SHA256 Credential=%s,SignedHeaders=%s,Signature=%s",
			credential, signedHeaders, signature))
	}

	return r
}

func TestVerifyRequestHeaderFormats(t *testing.T) {
	accounts := []Account{
		{Name: "test", AccessKeyID: "AKID", SecretAccessKey: "secret"},
	}

	tests := []struct {
		name   string
		format string
	}{
		{"exaws style, no space after comma", "exaws"},
		{"aws sdk style, space after comma", "aws"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := buildSignedRequestFormat(t, accounts, "GET", "/bucket/key", url.Values{}, "20240101T000000Z", tt.format)
			got := VerifyRequest(accounts, r)
			if got == nil {
				t.Fatal("expected auth to succeed, got nil")
			}
			if got.Name != "test" {
				t.Fatalf("expected account %q, got %q", "test", got.Name)
			}
		})
	}
}

func TestVerifyRequestInvalidSignature(t *testing.T) {
	accounts := []Account{
		{Name: "test", AccessKeyID: "AKID", SecretAccessKey: "secret"},
	}

	r := buildSignedRequestFormat(t, accounts, "GET", "/bucket/key", url.Values{}, "20240101T000000Z", "exaws")
	r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), "Signature=", "Signature=0", 1))

	if got := VerifyRequest(accounts, r); got != nil {
		t.Fatal("expected auth to fail with tampered signature")
	}
}

func TestVerifyRequestUnknownAccessKey(t *testing.T) {
	accounts := []Account{
		{Name: "test", AccessKeyID: "AKID", SecretAccessKey: "secret"},
	}

	r := buildSignedRequestFormat(t, accounts, "GET", "/bucket/key", url.Values{}, "20240101T000000Z", "exaws")
	r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), "AKID", "WRONG", 1))

	if got := VerifyRequest(accounts, r); got != nil {
		t.Fatal("expected auth to fail with unknown access key")
	}
}

func TestVerifyRequestWrongSecret(t *testing.T) {
	accounts := []Account{
		{Name: "test", AccessKeyID: "AKID", SecretAccessKey: "secret"},
	}

	other := []Account{{AccessKeyID: "AKID", SecretAccessKey: "other"}}
	r := buildSignedRequestFormat(t, other, "GET", "/bucket/key", url.Values{}, "20240101T000000Z", "exaws")

	if got := VerifyRequest(accounts, r); got != nil {
		t.Fatal("expected auth to fail with wrong secret")
	}
}

func TestVerifyRequestMissingAuthHeader(t *testing.T) {
	accounts := []Account{
		{Name: "test", AccessKeyID: "AKID", SecretAccessKey: "secret"},
	}

	r := buildSignedRequestFormat(t, accounts, "GET", "/bucket/key", url.Values{}, "20240101T000000Z", "exaws")
	r.Header.Del("Authorization")

	if got := VerifyRequest(accounts, r); got != nil {
		t.Fatal("expected auth to fail without Authorization header")
	}
}

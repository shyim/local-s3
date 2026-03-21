package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

type Account struct {
	Name            string
	AccessKeyID     string
	SecretAccessKey  string
	Buckets         []string // empty = access to all buckets
	ReadOnly        bool
}

// LoadFromEnv loads accounts from environment variables.
// Format: S3_ACCOUNT_<name>=<access_key_id>:<secret_access_key>
// Optional: S3_ACCOUNT_<name>_BUCKETS=bucket1,bucket2
// Optional: S3_ACCOUNT_<name>_READONLY=true
func LoadFromEnv() []Account {
	var accounts []Account
	prefix := "S3_ACCOUNT_"

	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, value := parts[0], parts[1]

		if !strings.HasPrefix(key, prefix) {
			continue
		}
		suffix := key[len(prefix):]

		// Skip sub-keys like _BUCKETS, _READONLY
		if strings.Contains(suffix, "_") {
			continue
		}

		name := suffix
		credParts := strings.SplitN(value, ":", 2)
		if len(credParts) != 2 {
			continue
		}

		account := Account{
			Name:           name,
			AccessKeyID:    credParts[0],
			SecretAccessKey: credParts[1],
		}

		if buckets := os.Getenv(prefix + name + "_BUCKETS"); buckets != "" {
			account.Buckets = strings.Split(buckets, ",")
		}

		if ro := os.Getenv(prefix + name + "_READONLY"); strings.EqualFold(ro, "true") {
			account.ReadOnly = true
		}

		accounts = append(accounts, account)
	}

	return accounts
}

func FindByAccessKey(accounts []Account, accessKeyID string) *Account {
	for i := range accounts {
		if accounts[i].AccessKeyID == accessKeyID {
			return &accounts[i]
		}
	}
	return nil
}

func (a *Account) CanAccessBucket(bucket string) bool {
	if len(a.Buckets) == 0 {
		return true
	}
	for _, b := range a.Buckets {
		if b == bucket {
			return true
		}
	}
	return false
}

func (a *Account) CanWrite() bool {
	return !a.ReadOnly
}

// VerifyRequest verifies an AWS Signature V4 request.
// Returns the matching account or nil if auth fails.
func VerifyRequest(accounts []Account, r *http.Request) *Account {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil
	}

	if !strings.HasPrefix(authHeader, "AWS4-HMAC-SHA256 ") {
		return nil
	}

	// Parse: AWS4-HMAC-SHA256 Credential=KEY/DATE/REGION/s3/aws4_request, SignedHeaders=..., Signature=...
	authContent := authHeader[len("AWS4-HMAC-SHA256 "):]
	parts := strings.Split(authContent, ", ")
	if len(parts) != 3 {
		return nil
	}

	var credentialStr, signedHeadersStr, signatureStr string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "Credential=") {
			credentialStr = p[len("Credential="):]
		} else if strings.HasPrefix(p, "SignedHeaders=") {
			signedHeadersStr = p[len("SignedHeaders="):]
		} else if strings.HasPrefix(p, "Signature=") {
			signatureStr = p[len("Signature="):]
		}
	}

	// Parse credential: ACCESS_KEY/DATE/REGION/s3/aws4_request
	credParts := strings.SplitN(credentialStr, "/", 5)
	if len(credParts) != 5 {
		return nil
	}
	accessKeyID := credParts[0]
	dateStamp := credParts[1]
	region := credParts[2]
	service := credParts[3]

	account := FindByAccessKey(accounts, accessKeyID)
	if account == nil {
		return nil
	}

	// Get the date from header
	amzDate := r.Header.Get("X-Amz-Date")
	if amzDate == "" {
		return nil
	}

	// Build canonical request
	signedHeaders := strings.Split(signedHeadersStr, ";")
	sort.Strings(signedHeaders)

	canonicalHeaders := ""
	for _, h := range signedHeaders {
		val := ""
		if strings.EqualFold(h, "host") {
			val = r.Host
		} else {
			val = r.Header.Get(h)
		}
		canonicalHeaders += strings.ToLower(h) + ":" + strings.TrimSpace(val) + "\n"
	}

	payloadHash := r.Header.Get("X-Amz-Content-Sha256")
	if payloadHash == "" {
		payloadHash = "UNSIGNED-PAYLOAD"
	}

	canonicalQueryString := buildCanonicalQueryString(r)
	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		r.Method,
		canonicalURI(r.URL.Path),
		canonicalQueryString,
		canonicalHeaders,
		signedHeadersStr,
		payloadHash,
	)

	canonicalRequestHash := sha256Hex([]byte(canonicalRequest))

	scope := fmt.Sprintf("%s/%s/%s/aws4_request", dateStamp, region, service)
	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s",
		amzDate,
		scope,
		canonicalRequestHash,
	)

	signingKey := deriveSigningKey(account.SecretAccessKey, dateStamp, region, service)
	expectedSig := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	if !hmac.Equal([]byte(expectedSig), []byte(signatureStr)) {
		return nil
	}

	return account
}

func deriveSigningKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	return kSigning
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func canonicalURI(path string) string {
	if path == "" {
		return "/"
	}
	return path
}

func buildCanonicalQueryString(r *http.Request) string {
	query := r.URL.Query()
	if len(query) == 0 {
		return ""
	}

	var keys []string
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var pairs []string
	for _, k := range keys {
		vals := query[k]
		sort.Strings(vals)
		for _, v := range vals {
			pairs = append(pairs, uriEncode(k)+"="+uriEncode(v))
		}
	}
	return strings.Join(pairs, "&")
}

func uriEncode(s string) string {
	var buf strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isUnreserved(c) {
			buf.WriteByte(c)
		} else {
			fmt.Fprintf(&buf, "%%%02X", c)
		}
	}
	return buf.String()
}

func isUnreserved(c byte) bool {
	return (c >= 'A' && c <= 'Z') ||
		(c >= 'a' && c <= 'z') ||
		(c >= '0' && c <= '9') ||
		c == '-' || c == '_' || c == '.' || c == '~'
}

// VerifyRequestForTime is like VerifyRequest but allows specifying a custom time
// for testing purposes. Not used in production but useful for debugging.
func VerifyRequestForTime(accounts []Account, r *http.Request, _ time.Time) *Account {
	return VerifyRequest(accounts, r)
}

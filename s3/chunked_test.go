package s3

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/crc64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCRC64NVMeKnownVector anchors the CRC-64/NVME table against the published
// check value for "123456789".
func TestCRC64NVMeKnownVector(t *testing.T) {
	h := crc64.New(crc64NVMeTable)
	_, err := h.Write([]byte("123456789"))
	require.NoError(t, err)
	assert.Equal(t, uint64(0xAE8B14860A799888), h.Sum64())
}

func awsChunkedBody(chunks []string, trailer string) string {
	var sb strings.Builder
	for _, c := range chunks {
		fmt.Fprintf(&sb, "%x\r\n%s\r\n", len(c), c)
	}
	sb.WriteString("0\r\n")
	if trailer != "" {
		sb.WriteString(trailer + "\r\n")
	}
	sb.WriteString("\r\n")
	return sb.String()
}

func newChunkedRequest(body string, decodedLen string) *http.Request {
	req := httptest.NewRequest(http.MethodPut, "/bucket/key", strings.NewReader(body))
	req.Header.Set("Content-Encoding", "aws-chunked")
	req.Header.Set("X-Amz-Content-Sha256", "STREAMING-UNSIGNED-PAYLOAD-TRAILER")
	if decodedLen != "" {
		req.Header.Set("X-Amz-Decoded-Content-Length", decodedLen)
	}
	return req
}

func crc64NVMeBase64(t *testing.T, content string) string {
	t.Helper()
	h := crc64.New(crc64NVMeTable)
	_, err := h.Write([]byte(content))
	require.NoError(t, err)
	b := make([]byte, 8)
	b[0] = byte(h.Sum64() >> 56)
	b[1] = byte(h.Sum64() >> 48)
	b[2] = byte(h.Sum64() >> 40)
	b[3] = byte(h.Sum64() >> 32)
	b[4] = byte(h.Sum64() >> 24)
	b[5] = byte(h.Sum64() >> 16)
	b[6] = byte(h.Sum64() >> 8)
	b[7] = byte(h.Sum64())
	return base64.StdEncoding.EncodeToString(b)
}

// Reproduces the exact scenario from the GitHub issue: a single-chunk upload
// of "hi\n" with a CRC64NVME checksum trailer.
func TestPayloadReaderAWSChunkedSingleChunk(t *testing.T) {
	content := "hi\n"
	body := awsChunkedBody([]string{content}, "x-amz-checksum-crc64nvme:"+crc64NVMeBase64(t, content))

	p := newPayloadReader(newChunkedRequest(body, "3"))
	got, err := io.ReadAll(p)
	require.NoError(t, err)
	assert.Equal(t, content, string(got))

	algo, sum := p.ChecksumHeader()
	assert.Equal(t, "crc64nvme", algo)
	assert.Equal(t, crc64NVMeBase64(t, content), sum)
}

func TestPayloadReaderAWSChunkedMultipleChunks(t *testing.T) {
	chunks := []string{"hello", " ", "world", "!"}
	content := strings.Join(chunks, "")
	body := awsChunkedBody(chunks, "x-amz-checksum-crc64nvme:"+crc64NVMeBase64(t, content))

	p := newPayloadReader(newChunkedRequest(body, fmt.Sprint(len(content))))
	got, err := io.ReadAll(p)
	require.NoError(t, err)
	assert.Equal(t, content, string(got))
}

// SigV4 streaming uploads carry a chunk-signature extension on every chunk
// header; it must be ignored when parsing the chunk size.
func TestPayloadReaderAWSChunkedIgnoresChunkSignature(t *testing.T) {
	body := "3;chunk-signature=deadbeef\r\nhi\n\r\n0;chunk-signature=abcdef\r\n\r\n"
	p := newPayloadReader(newChunkedRequest(body, "3"))
	got, err := io.ReadAll(p)
	require.NoError(t, err)
	assert.Equal(t, "hi\n", string(got))
}

// A chunk bigger than the reader's buffer must be reassembled across
// multiple Read calls (regression: the CRLF terminator was consumed too early).
func TestPayloadReaderAWSChunkedLargeChunk(t *testing.T) {
	content := strings.Repeat("0123456789abcdef", 8192) // 128 KiB single chunk
	body := awsChunkedBody([]string{content}, "x-amz-checksum-crc64nvme:"+crc64NVMeBase64(t, content))

	p := newPayloadReader(newChunkedRequest(body, fmt.Sprint(len(content))))
	got, err := io.ReadAll(p)
	require.NoError(t, err)
	assert.Equal(t, content, string(got))
}

func TestPayloadReaderAWSChunkedWithoutTrailer(t *testing.T) {
	body := awsChunkedBody([]string{"abc"}, "")
	p := newPayloadReader(newChunkedRequest(body, "3"))
	got, err := io.ReadAll(p)
	require.NoError(t, err)
	assert.Equal(t, "abc", string(got))
}

func TestPayloadReaderAWSChunkedEmptyBody(t *testing.T) {
	body := awsChunkedBody(nil, "")
	p := newPayloadReader(newChunkedRequest(body, "0"))
	got, err := io.ReadAll(p)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestPayloadReaderAWSChunkedBadChecksum(t *testing.T) {
	content := "hi\n"
	body := awsChunkedBody([]string{content}, "x-amz-checksum-crc64nvme:"+base64.StdEncoding.EncodeToString([]byte("bogus!")))

	p := newPayloadReader(newChunkedRequest(body, "3"))
	_, err := io.ReadAll(p)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrBadDigest), "expected ErrBadDigest, got %v", err)
}

func TestPayloadReaderAWSChunkedDecodedLengthMismatch(t *testing.T) {
	content := "hi\n"
	body := awsChunkedBody([]string{content}, "")

	// Declares 10 bytes but only sends 3.
	p := newPayloadReader(newChunkedRequest(body, "10"))
	_, err := io.ReadAll(p)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrIncompleteBody), "expected ErrIncompleteBody, got %v", err)
}

func TestPayloadReaderAWSChunkedTruncatedStream(t *testing.T) {
	// Declares a chunk of 100 bytes but the stream ends after 3.
	body := "64\r\nhi\n"
	p := newPayloadReader(newChunkedRequest(body, "100"))
	_, err := io.ReadAll(p)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrBadDigest))
}

func TestPayloadReaderPlainWithChecksumHeader(t *testing.T) {
	content := "hello world"
	sum := sha256.Sum256([]byte(content))
	req := httptest.NewRequest(http.MethodPut, "/bucket/key", strings.NewReader(content))
	req.Header.Set("X-Amz-Checksum-Sha256", base64.StdEncoding.EncodeToString(sum[:]))

	p := newPayloadReader(req)
	got, err := io.ReadAll(p)
	require.NoError(t, err)
	assert.Equal(t, content, string(got))

	algo, respSum := p.ChecksumHeader()
	assert.Equal(t, "sha256", algo)
	assert.Equal(t, base64.StdEncoding.EncodeToString(sum[:]), respSum)
}

func TestPayloadReaderPlainWithBadChecksumHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "/bucket/key", strings.NewReader("hello world"))
	req.Header.Set("X-Amz-Checksum-Sha256", base64.StdEncoding.EncodeToString([]byte("bogus!")))

	p := newPayloadReader(req)
	_, err := io.ReadAll(p)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrBadDigest), "expected ErrBadDigest, got %v", err)
}

func TestPayloadReaderPlainWithoutChecksum(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "/bucket/key", strings.NewReader("plain body"))
	p := newPayloadReader(req)
	got, err := io.ReadAll(p)
	require.NoError(t, err)
	assert.Equal(t, "plain body", string(got))
	algo, _ := p.ChecksumHeader()
	assert.Empty(t, algo)
}

func TestIsAWSChunkedRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "/b/k", nil)
	assert.False(t, isAWSChunkedRequest(req))

	req.Header.Set("Content-Encoding", "aws-chunked")
	assert.True(t, isAWSChunkedRequest(req))

	req = httptest.NewRequest(http.MethodPut, "/b/k", nil)
	req.Header.Set("Content-Encoding", "gzip, aws-chunked")
	assert.True(t, isAWSChunkedRequest(req))

	req = httptest.NewRequest(http.MethodPut, "/b/k", nil)
	req.Header.Set("X-Amz-Decoded-Content-Length", "3")
	assert.True(t, isAWSChunkedRequest(req))
}

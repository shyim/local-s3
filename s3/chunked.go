package s3

import (
	"bufio"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// ErrBadDigest is returned when a client-provided checksum (trailer or header)
// does not match the payload we received.
var ErrBadDigest = errors.New("checksum mismatch")

// ErrIncompleteBody is returned when the decoded payload size does not match
// the x-amz-decoded-content-length the client declared.
var ErrIncompleteBody = errors.New("incomplete body")

// crc64NVMeTable is the table for CRC-64/NVME (poly 0xAD93D23594C93659,
// init/xorout all-ones, reflected), which Go's crc64 implements once the
// polynomial is bit-reversed. Check value for "123456789" is
// 0xAE8B14860A799888.
var crc64NVMeTable = crc64.MakeTable(reverseBits64(0xAD93D23594C93659))

func reverseBits64(x uint64) uint64 {
	var r uint64
	for i := 0; i < 64; i++ {
		r = (r << 1) | (x & 1)
		x >>= 1
	}
	return r
}

type checksumAlgorithm struct {
	newHash func() hash.Hash
	sum     func(h hash.Hash) []byte
}

var checksumAlgorithms = map[string]checksumAlgorithm{
	"crc32": {
		newHash: func() hash.Hash { return crc32.NewIEEE() },
		sum: func(h hash.Hash) []byte {
			s := h.(hash.Hash32).Sum32()
			return []byte{byte(s >> 24), byte(s >> 16), byte(s >> 8), byte(s)}
		},
	},
	"crc32c": {
		newHash: func() hash.Hash { return crc32.New(crc32.MakeTable(crc32.Castagnoli)) },
		sum: func(h hash.Hash) []byte {
			s := h.(hash.Hash32).Sum32()
			return []byte{byte(s >> 24), byte(s >> 16), byte(s >> 8), byte(s)}
		},
	},
	"crc64nvme": {
		newHash: func() hash.Hash { return crc64.New(crc64NVMeTable) },
		sum: func(h hash.Hash) []byte {
			b := make([]byte, 8)
			binary.BigEndian.PutUint64(b, h.(hash.Hash64).Sum64())
			return b
		},
	},
	"sha1": {
		newHash: sha1.New,
		sum:     func(h hash.Hash) []byte { return h.Sum(nil) },
	},
	"sha256": {
		newHash: sha256.New,
		sum:     func(h hash.Hash) []byte { return h.Sum(nil) },
	},
}

// isAWSChunkedRequest reports whether the request body uses the
// "aws-chunked" content encoding (chunk framing + optional checksum trailer),
// which clients like the AWS CLI use for uploads with integrity checksums.
func isAWSChunkedRequest(r *http.Request) bool {
	if len(r.Header.Values("X-Amz-Decoded-Content-Length")) > 0 {
		return true
	}
	for _, enc := range r.Header.Values("Content-Encoding") {
		for _, part := range strings.Split(enc, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "aws-chunked") {
				return true
			}
		}
	}
	return false
}

// payloadReader decodes the request body of a PutObject request. For
// aws-chunked requests it strips the chunk framing and the trailing checksum
// headers, validates the declared decoded length and any checksums, and
// exposes the verified checksum so it can be echoed back in the response.
// For plain requests it optionally validates x-amz-checksum-* headers.
type payloadReader struct {
	src        io.Reader
	br         *bufio.Reader
	chunked    bool
	chunkLeft  int64
	maxDecoded int64 // declared x-amz-decoded-content-length, -1 when absent
	decoded    int64
	done       bool
	err        error
	hashes     map[string]hash.Hash
	expect     map[string]string   // algorithm -> expected base64 checksum
	required   map[string]struct{} // declared checksum trailers yet to be read
	respAlgo   string
	respSum    string
}

func newPayloadReader(r *http.Request) *payloadReader {
	p := &payloadReader{
		src:        r.Body,
		br:         bufio.NewReader(r.Body),
		maxDecoded: -1,
		hashes:     make(map[string]hash.Hash, len(checksumAlgorithms)),
		expect:     map[string]string{},
		required:   map[string]struct{}{},
	}
	for algo, def := range checksumAlgorithms {
		p.hashes[algo] = def.newHash()
	}

	// Checksums sent as regular headers apply to both plain and chunked
	// requests; chunked requests usually carry them as trailers instead.
	for name, values := range r.Header {
		algo, ok := strings.CutPrefix(strings.ToLower(name), "x-amz-checksum-")
		if !ok {
			continue
		}
		if _, known := checksumAlgorithms[algo]; !known {
			continue
		}
		for _, v := range values {
			if err := p.expectChecksum(algo, v); err != nil {
				p.err = err
				return p
			}
		}
	}

	if isAWSChunkedRequest(r) {
		p.chunked = true
		if values := r.Header.Values("X-Amz-Decoded-Content-Length"); len(values) > 0 {
			n, err := strconv.ParseUint(values[0], 10, 63)
			if err != nil || len(values) != 1 {
				p.err = fmt.Errorf("%w: invalid x-amz-decoded-content-length", ErrIncompleteBody)
				return p
			}
			p.maxDecoded = int64(n)
		}
		for _, value := range r.Header.Values("X-Amz-Trailer") {
			for name := range strings.SplitSeq(value, ",") {
				algo, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(name)), "x-amz-checksum-")
				if _, known := checksumAlgorithms[algo]; ok && known {
					p.required[algo] = struct{}{}
				}
			}
		}
	}
	return p
}

// ChecksumHeader returns the algorithm and base64 checksum that were verified
// for this payload, so the handler can echo them back like real S3 does.
func (p *payloadReader) ChecksumHeader() (string, string) {
	return p.respAlgo, p.respSum
}

func (p *payloadReader) Read(buf []byte) (int, error) {
	if p.err != nil {
		return 0, p.err
	}
	if p.done {
		return 0, io.EOF
	}

	if !p.chunked {
		n, err := p.src.Read(buf)
		if n > 0 {
			p.track(buf[:n])
		}
		if err == io.EOF {
			if verr := p.finish(); verr != nil {
				p.err = verr
				return n, verr
			}
			p.done = true
			return n, io.EOF
		}
		if err != nil {
			p.err = err
		}
		return n, err
	}

	for p.chunkLeft == 0 {
		size, err := p.readChunkHeader()
		if err != nil {
			p.err = err
			return 0, err
		}
		if size == 0 {
			if err := p.readTrailers(); err != nil {
				p.err = err
				return 0, err
			}
			if err := p.finish(); err != nil {
				p.err = err
				return 0, err
			}
			p.done = true
			return 0, io.EOF
		}
		p.chunkLeft = size
	}

	if int64(len(buf)) > p.chunkLeft {
		buf = buf[:p.chunkLeft]
	}
	n, err := io.ReadFull(p.br, buf)
	p.chunkLeft -= int64(n)
	p.track(buf[:n])
	if err != nil {
		p.err = fmt.Errorf("read chunk data: %w", err)
		return n, p.err
	}
	// The CRLF terminator only follows once the whole chunk was consumed.
	if p.chunkLeft == 0 {
		if err := p.expectCRLF(); err != nil {
			p.err = err
			return n, err
		}
	}
	if p.maxDecoded >= 0 && p.decoded > p.maxDecoded {
		p.err = fmt.Errorf("%w: received more than the declared %d bytes", ErrIncompleteBody, p.maxDecoded)
		return n, p.err
	}
	return n, nil
}

func (p *payloadReader) track(b []byte) {
	p.decoded += int64(len(b))
	for _, h := range p.hashes {
		h.Write(b)
	}
}

func (p *payloadReader) finish() error {
	if p.maxDecoded >= 0 && p.decoded != p.maxDecoded {
		return fmt.Errorf("%w: got %d bytes, client declared %d", ErrIncompleteBody, p.decoded, p.maxDecoded)
	}
	for algo := range p.required {
		return fmt.Errorf("%w: missing x-amz-checksum-%s trailer", ErrBadDigest, algo)
	}
	for algo, expected := range p.expect {
		sum := base64.StdEncoding.EncodeToString(checksumAlgorithms[algo].sum(p.hashes[algo]))
		if sum != expected {
			return fmt.Errorf("%w: x-amz-checksum-%s: client sent %s, payload hashes to %s", ErrBadDigest, algo, expected, sum)
		}
		if p.respAlgo == "" {
			p.respAlgo, p.respSum = algo, sum
		}
	}
	return nil
}

func (p *payloadReader) expectChecksum(algo, value string) error {
	value = strings.TrimSpace(value)
	if expected, ok := p.expect[algo]; ok && expected != value {
		return fmt.Errorf("%w: conflicting x-amz-checksum-%s values", ErrBadDigest, algo)
	}
	p.expect[algo] = value
	return nil
}

// readLine reads a single CRLF (or LF) terminated line.
func (p *payloadReader) readLine() (string, error) {
	var sb strings.Builder
	for {
		frag, err := p.br.ReadSlice('\n')
		sb.Write(frag)
		if err == nil {
			break
		}
		if err == bufio.ErrBufferFull {
			if sb.Len() > 64*1024 {
				return "", errors.New("chunked stream: line too long")
			}
			continue
		}
		return "", err
	}
	line := strings.TrimSuffix(sb.String(), "\n")
	line = strings.TrimSuffix(line, "\r")
	return line, nil
}

// readChunkHeader parses a chunk header line like "1a;chunk-signature=..." and
// returns the chunk size in bytes.
func (p *payloadReader) readChunkHeader() (int64, error) {
	line, err := p.readLine()
	if err != nil {
		return 0, fmt.Errorf("read chunk header: %w", err)
	}
	hexPart := line
	if i := strings.IndexByte(line, ';'); i >= 0 {
		hexPart = line[:i]
	}
	size, err := strconv.ParseInt(strings.TrimSpace(hexPart), 16, 64)
	if err != nil || size < 0 {
		return 0, fmt.Errorf("invalid chunk size %q", line)
	}
	return size, nil
}

func (p *payloadReader) expectCRLF() error {
	crlf := make([]byte, 2)
	if _, err := io.ReadFull(p.br, crlf); err != nil {
		return fmt.Errorf("read chunk terminator: %w", err)
	}
	if crlf[0] != '\r' || crlf[1] != '\n' {
		return fmt.Errorf("invalid chunk terminator %q", string(crlf))
	}
	return nil
}

// readTrailers consumes the trailer section after the terminating zero chunk,
// collecting any x-amz-checksum-* values.
func (p *payloadReader) readTrailers() error {
	for {
		line, err := p.readLine()
		if err != nil {
			return fmt.Errorf("read trailer: %w", err)
		}
		if line == "" {
			return nil
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		algo, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(name)), "x-amz-checksum-")
		if !ok {
			continue
		}
		if _, known := checksumAlgorithms[algo]; known {
			if err := p.expectChecksum(algo, value); err != nil {
				return err
			}
			delete(p.required, algo)
		}
	}
}

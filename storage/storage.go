package storage

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ObjectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
	ETag         string
	ContentType  string
}

type BucketInfo struct {
	Name         string
	CreationDate time.Time
}

type Storage struct {
	dataDir string
}

func New(dataDir string) (*Storage, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	return &Storage{dataDir: dataDir}, nil
}

func (s *Storage) BucketPath(bucket string) string {
	return filepath.Join(s.dataDir, bucket)
}

func (s *Storage) ObjectPath(bucket, key string) string {
	return filepath.Join(s.dataDir, bucket, key)
}

func (s *Storage) EnsureBucket(bucket string) error {
	return os.MkdirAll(s.BucketPath(bucket), 0o755)
}

func (s *Storage) DeleteBucket(bucket string) error {
	if !s.BucketExists(bucket) {
		return fmt.Errorf("bucket not found: %s", bucket)
	}
	return os.RemoveAll(s.BucketPath(bucket))
}

func (s *Storage) BucketExists(bucket string) bool {
	info, err := os.Stat(s.BucketPath(bucket))
	return err == nil && info.IsDir()
}

func (s *Storage) ListBuckets() ([]BucketInfo, error) {
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		return nil, err
	}

	var buckets []BucketInfo
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		buckets = append(buckets, BucketInfo{
			Name:         entry.Name(),
			CreationDate: info.ModTime(),
		})
	}
	return buckets, nil
}

func (s *Storage) ListObjects(bucket, prefix, delimiter string, maxKeys int) (objects []ObjectInfo, commonPrefixes []string, isTruncated bool, err error) {
	bucketPath := s.BucketPath(bucket)

	if !s.BucketExists(bucket) {
		return nil, nil, false, fmt.Errorf("bucket not found: %s", bucket)
	}

	prefixSet := make(map[string]bool)
	err = filepath.Walk(bucketPath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}

		relPath, _ := filepath.Rel(bucketPath, path)
		// Normalize to forward slashes for S3 compatibility
		key := filepath.ToSlash(relPath)

		if prefix != "" && !strings.HasPrefix(key, prefix) {
			return nil
		}

		if delimiter != "" {
			// Find common prefixes
			afterPrefix := key[len(prefix):]
			idx := strings.Index(afterPrefix, delimiter)
			if idx >= 0 {
				cp := prefix + afterPrefix[:idx+len(delimiter)]
				if !prefixSet[cp] {
					prefixSet[cp] = true
					commonPrefixes = append(commonPrefixes, cp)
				}
				return nil
			}
		}

		etag := computeETag(path)
		objects = append(objects, ObjectInfo{
			Key:          key,
			Size:         info.Size(),
			LastModified: info.ModTime(),
			ETag:         etag,
			ContentType:  detectContentType(key),
		})

		return nil
	})

	if err != nil {
		return nil, nil, false, err
	}

	sort.Slice(objects, func(i, j int) bool {
		return objects[i].Key < objects[j].Key
	})
	sort.Strings(commonPrefixes)

	if maxKeys > 0 && len(objects) > maxKeys {
		objects = objects[:maxKeys]
		isTruncated = true
	}

	return objects, commonPrefixes, isTruncated, nil
}

func (s *Storage) GetObject(bucket, key string) (io.ReadCloser, *ObjectInfo, error) {
	path := s.ObjectPath(bucket, key)
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}

	obj := &ObjectInfo{
		Key:          key,
		Size:         info.Size(),
		LastModified: info.ModTime(),
		ETag:         computeETag(path),
		ContentType:  detectContentType(key),
	}

	return f, obj, nil
}

func (s *Storage) HeadObject(bucket, key string) (*ObjectInfo, error) {
	path := s.ObjectPath(bucket, key)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	return &ObjectInfo{
		Key:          key,
		Size:         info.Size(),
		LastModified: info.ModTime(),
		ETag:         computeETag(path),
		ContentType:  detectContentType(key),
	}, nil
}

func (s *Storage) PutObject(bucket, key string, body io.Reader) (*ObjectInfo, error) {
	path := s.ObjectPath(bucket, key)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	h := md5.New()
	w := io.MultiWriter(f, h)
	size, err := io.Copy(w, body)
	if err != nil {
		return nil, err
	}

	etag := hex.EncodeToString(h.Sum(nil))
	info, _ := os.Stat(path)

	return &ObjectInfo{
		Key:          key,
		Size:         size,
		LastModified: info.ModTime(),
		ETag:         etag,
		ContentType:  detectContentType(key),
	}, nil
}

func (s *Storage) DeleteObject(bucket, key string) error {
	path := s.ObjectPath(bucket, key)
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// Clean up empty parent directories
	dir := filepath.Dir(path)
	bucketPath := s.BucketPath(bucket)
	for dir != bucketPath {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			break
		}
		os.Remove(dir)
		dir = filepath.Dir(dir)
	}

	return nil
}

func computeETag(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	h := md5.New()
	io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))
}

func detectContentType(key string) string {
	ext := strings.ToLower(filepath.Ext(key))
	types := map[string]string{
		".html": "text/html",
		".css":  "text/css",
		".js":   "application/javascript",
		".json": "application/json",
		".xml":  "application/xml",
		".txt":  "text/plain",
		".png":  "image/png",
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".gif":  "image/gif",
		".svg":  "image/svg+xml",
		".webp": "image/webp",
		".pdf":  "application/pdf",
		".zip":  "application/zip",
		".gz":   "application/gzip",
		".tar":  "application/x-tar",
		".mp4":  "video/mp4",
		".mp3":  "audio/mpeg",
		".wav":  "audio/wav",
		".woff": "font/woff",
		".woff2": "font/woff2",
		".ico":  "image/x-icon",
	}
	if ct, ok := types[ext]; ok {
		return ct
	}
	return "application/octet-stream"
}

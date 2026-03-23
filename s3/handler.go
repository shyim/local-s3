package s3

import (
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shyim/local-s3/auth"
	"github.com/shyim/local-s3/storage"
)

type Handler struct {
	store    *storage.Storage
	accounts []auth.Account
}

func NewHandler(store *storage.Storage, accounts []auth.Account) *Handler {
	return &Handler{store: store, accounts: accounts}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Authenticate via header or presigned URL
	account := auth.VerifyRequest(h.accounts, r)
	if account == nil && r.URL.Query().Has("X-Amz-Algorithm") {
		account = auth.VerifyPresignedRequest(h.accounts, r)
	}
	if account == nil && len(h.accounts) > 0 {
		writeErrorResponse(w, http.StatusForbidden, "AccessDenied", "Access Denied")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(path, "/", 2)

	bucket := ""
	key := ""
	if len(parts) >= 1 {
		bucket = parts[0]
	}
	if len(parts) >= 2 {
		key = parts[1]
	}

	// Root request = ListBuckets
	if bucket == "" {
		h.handleListBuckets(w, r, account)
		return
	}

	// Check bucket access
	if account != nil && !account.CanAccessBucket(bucket) {
		writeErrorResponse(w, http.StatusForbidden, "AccessDenied", "Access Denied")
		return
	}

	// Bucket-level operations
	if key == "" {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Has("list-type") {
				h.handleListObjectsV2(w, r, bucket)
			} else {
				h.handleListObjects(w, r, bucket)
			}
		case http.MethodHead:
			h.handleHeadBucket(w, r, bucket)
		case http.MethodPut:
			h.handleCreateBucket(w, r, account, bucket)
		default:
			writeErrorResponse(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "Method not allowed")
		}
		return
	}

	// Object-level operations
	switch r.Method {
	case http.MethodGet:
		h.handleGetObject(w, r, bucket, key)
	case http.MethodPut:
		h.handlePutObject(w, r, account, bucket, key)
	case http.MethodDelete:
		h.handleDeleteObject(w, r, account, bucket, key)
	case http.MethodHead:
		h.handleHeadObject(w, r, bucket, key)
	default:
		writeErrorResponse(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "Method not allowed")
	}
}

func (h *Handler) handleListBuckets(w http.ResponseWriter, _ *http.Request, account *auth.Account) {
	buckets, err := h.store.ListBuckets()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}

	// Filter by account access
	if account != nil && len(account.Buckets) > 0 {
		var filtered []storage.BucketInfo
		for _, b := range buckets {
			if account.CanAccessBucket(b.Name) {
				filtered = append(filtered, b)
			}
		}
		buckets = filtered
	}

	type xmlBucket struct {
		Name         string `xml:"Name"`
		CreationDate string `xml:"CreationDate"`
	}
	type xmlListBuckets struct {
		XMLName xml.Name    `xml:"ListAllMyBucketsResult"`
		Xmlns   string      `xml:"xmlns,attr"`
		Owner   xmlOwner    `xml:"Owner"`
		Buckets []xmlBucket `xml:"Buckets>Bucket"`
	}

	var xBuckets []xmlBucket
	for _, b := range buckets {
		xBuckets = append(xBuckets, xmlBucket{
			Name:         b.Name,
			CreationDate: b.CreationDate.UTC().Format(time.RFC3339),
		})
	}

	resp := xmlListBuckets{
		Xmlns:   "http://s3.amazonaws.com/doc/2006-03-01/",
		Owner:   xmlOwner{ID: "local-s3", DisplayName: "Local S3"},
		Buckets: xBuckets,
	}

	writeXMLResponse(w, http.StatusOK, resp)
}

func (h *Handler) handleListObjects(w http.ResponseWriter, r *http.Request, bucket string) {
	if !h.store.BucketExists(bucket) {
		writeErrorResponse(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist")
		return
	}

	prefix := r.URL.Query().Get("prefix")
	delimiter := r.URL.Query().Get("delimiter")
	maxKeysStr := r.URL.Query().Get("max-keys")
	maxKeys := 1000
	if maxKeysStr != "" {
		if mk, err := strconv.Atoi(maxKeysStr); err == nil {
			maxKeys = mk
		}
	}

	objects, commonPrefixes, isTruncated, err := h.store.ListObjects(bucket, prefix, delimiter, maxKeys)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}

	type xmlContent struct {
		Key          string   `xml:"Key"`
		LastModified string   `xml:"LastModified"`
		ETag         string   `xml:"ETag"`
		Size         int64    `xml:"Size"`
		StorageClass string   `xml:"StorageClass"`
		Owner        xmlOwner `xml:"Owner"`
	}
	type xmlCommonPrefix struct {
		Prefix string `xml:"Prefix"`
	}
	type xmlListBucket struct {
		XMLName        xml.Name          `xml:"ListBucketResult"`
		Xmlns          string            `xml:"xmlns,attr"`
		Name           string            `xml:"Name"`
		Prefix         string            `xml:"Prefix"`
		Delimiter      string            `xml:"Delimiter,omitempty"`
		MaxKeys        int               `xml:"MaxKeys"`
		IsTruncated    bool              `xml:"IsTruncated"`
		Contents       []xmlContent      `xml:"Contents"`
		CommonPrefixes []xmlCommonPrefix `xml:"CommonPrefixes,omitempty"`
	}

	var contents []xmlContent
	for _, o := range objects {
		contents = append(contents, xmlContent{
			Key:          o.Key,
			LastModified: o.LastModified.UTC().Format(time.RFC3339),
			ETag:         fmt.Sprintf("\"%s\"", o.ETag),
			Size:         o.Size,
			StorageClass: "STANDARD",
			Owner:        xmlOwner{ID: "local-s3", DisplayName: "Local S3"},
		})
	}

	var cps []xmlCommonPrefix
	for _, cp := range commonPrefixes {
		cps = append(cps, xmlCommonPrefix{Prefix: cp})
	}

	resp := xmlListBucket{
		Xmlns:          "http://s3.amazonaws.com/doc/2006-03-01/",
		Name:           bucket,
		Prefix:         prefix,
		Delimiter:      delimiter,
		MaxKeys:        maxKeys,
		IsTruncated:    isTruncated,
		Contents:       contents,
		CommonPrefixes: cps,
	}

	writeXMLResponse(w, http.StatusOK, resp)
}

func (h *Handler) handleListObjectsV2(w http.ResponseWriter, r *http.Request, bucket string) {
	if !h.store.BucketExists(bucket) {
		writeErrorResponse(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist")
		return
	}

	prefix := r.URL.Query().Get("prefix")
	delimiter := r.URL.Query().Get("delimiter")
	maxKeysStr := r.URL.Query().Get("max-keys")
	maxKeys := 1000
	if maxKeysStr != "" {
		if mk, err := strconv.Atoi(maxKeysStr); err == nil {
			maxKeys = mk
		}
	}

	objects, commonPrefixes, isTruncated, err := h.store.ListObjects(bucket, prefix, delimiter, maxKeys)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}

	type xmlContent struct {
		Key          string `xml:"Key"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
		Size         int64  `xml:"Size"`
		StorageClass string `xml:"StorageClass"`
	}
	type xmlCommonPrefix struct {
		Prefix string `xml:"Prefix"`
	}
	type xmlListBucketV2 struct {
		XMLName        xml.Name          `xml:"ListBucketResult"`
		Xmlns          string            `xml:"xmlns,attr"`
		Name           string            `xml:"Name"`
		Prefix         string            `xml:"Prefix"`
		Delimiter      string            `xml:"Delimiter,omitempty"`
		MaxKeys        int               `xml:"MaxKeys"`
		KeyCount       int               `xml:"KeyCount"`
		IsTruncated    bool              `xml:"IsTruncated"`
		Contents       []xmlContent      `xml:"Contents"`
		CommonPrefixes []xmlCommonPrefix `xml:"CommonPrefixes,omitempty"`
	}

	var contents []xmlContent
	for _, o := range objects {
		contents = append(contents, xmlContent{
			Key:          o.Key,
			LastModified: o.LastModified.UTC().Format(time.RFC3339),
			ETag:         fmt.Sprintf("\"%s\"", o.ETag),
			Size:         o.Size,
			StorageClass: "STANDARD",
		})
	}

	var cps []xmlCommonPrefix
	for _, cp := range commonPrefixes {
		cps = append(cps, xmlCommonPrefix{Prefix: cp})
	}

	resp := xmlListBucketV2{
		Xmlns:          "http://s3.amazonaws.com/doc/2006-03-01/",
		Name:           bucket,
		Prefix:         prefix,
		Delimiter:      delimiter,
		MaxKeys:        maxKeys,
		KeyCount:       len(contents),
		IsTruncated:    isTruncated,
		Contents:       contents,
		CommonPrefixes: cps,
	}

	writeXMLResponse(w, http.StatusOK, resp)
}

func (h *Handler) handleHeadBucket(w http.ResponseWriter, _ *http.Request, bucket string) {
	if !h.store.BucketExists(bucket) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("X-Amz-Bucket-Region", "us-east-1")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleCreateBucket(w http.ResponseWriter, _ *http.Request, account *auth.Account, bucket string) {
	if account != nil && !account.CanWrite() {
		writeErrorResponse(w, http.StatusForbidden, "AccessDenied", "Write access denied")
		return
	}

	if err := h.store.EnsureBucket(bucket); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	w.Header().Set("Location", "/"+bucket)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleGetObject(w http.ResponseWriter, _ *http.Request, bucket, key string) {
	if !h.store.BucketExists(bucket) {
		writeErrorResponse(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist")
		return
	}

	reader, info, err := h.store.GetObject(bucket, key)
	if err != nil {
		writeErrorResponse(w, http.StatusNotFound, "NoSuchKey", "The specified key does not exist")
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", info.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	w.Header().Set("ETag", fmt.Sprintf("\"%s\"", info.ETag))
	w.Header().Set("Last-Modified", info.LastModified.UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, reader); err != nil {
		log.Printf("Error streaming object %s/%s: %v", bucket, key, err)
	}
}

func (h *Handler) handlePutObject(w http.ResponseWriter, r *http.Request, account *auth.Account, bucket, key string) {
	if account != nil && !account.CanWrite() {
		writeErrorResponse(w, http.StatusForbidden, "AccessDenied", "Write access denied")
		return
	}

	if !h.store.BucketExists(bucket) {
		writeErrorResponse(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist")
		return
	}

	info, err := h.store.PutObject(bucket, key, r.Body)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}

	w.Header().Set("ETag", fmt.Sprintf("\"%s\"", info.ETag))
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleDeleteObject(w http.ResponseWriter, _ *http.Request, account *auth.Account, bucket, key string) {
	if account != nil && !account.CanWrite() {
		writeErrorResponse(w, http.StatusForbidden, "AccessDenied", "Write access denied")
		return
	}

	if err := h.store.DeleteObject(bucket, key); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleHeadObject(w http.ResponseWriter, _ *http.Request, bucket, key string) {
	if !h.store.BucketExists(bucket) {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	info, err := h.store.HeadObject(bucket, key)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", info.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	w.Header().Set("ETag", fmt.Sprintf("\"%s\"", info.ETag))
	w.Header().Set("Last-Modified", info.LastModified.UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)
}

type xmlOwner struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

type xmlErrorResponse struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

func writeXMLResponse(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(v)
}

func writeErrorResponse(w http.ResponseWriter, status int, code, message string) {
	writeXMLResponse(w, status, xmlErrorResponse{Code: code, Message: message})
}

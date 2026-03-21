package ui

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/shyim/local-s3/auth"
	"github.com/shyim/local-s3/static"
	"github.com/shyim/local-s3/storage"
)

func Register(mux *http.ServeMux, store *storage.Storage, accounts []auth.Account) {
	h := &uiHandler{store: store, accounts: accounts}

	mux.HandleFunc("GET /_ui", h.handleIndex)
	mux.HandleFunc("GET /_ui/bucket/{bucket}", h.handleBucket)
	mux.HandleFunc("GET /_ui/bucket/{bucket}/browse/{path...}", h.handleBrowse)
	mux.HandleFunc("POST /_ui/bucket/{bucket}/upload", h.handleUpload)
	mux.HandleFunc("POST /_ui/bucket/{bucket}/delete", h.handleDelete)
	mux.HandleFunc("POST /_ui/bucket/{bucket}/create-folder", h.handleCreateFolder)
	mux.HandleFunc("POST /_ui/create-bucket", h.handleCreateBucket)
	mux.HandleFunc("POST /_ui/bucket/{bucket}/delete-bucket", h.handleDeleteBucket)
	mux.Handle("GET /_ui/static/", http.StripPrefix("/_ui/static/", http.FileServerFS(static.FS)))
}

type uiHandler struct {
	store    *storage.Storage
	accounts []auth.Account
}

func (h *uiHandler) handleIndex(w http.ResponseWriter, r *http.Request) {
	buckets, err := h.store.ListBuckets()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	component := indexPage(buckets, h.accounts)
	component.Render(r.Context(), w)
}

func (h *uiHandler) handleBucket(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	if !h.store.BucketExists(bucket) {
		http.Error(w, "Bucket not found", http.StatusNotFound)
		return
	}

	h.renderBrowse(w, r, bucket, "")
}

func (h *uiHandler) handleBrowse(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	prefix := r.PathValue("path")

	if !h.store.BucketExists(bucket) {
		http.Error(w, "Bucket not found", http.StatusNotFound)
		return
	}

	// Ensure prefix ends with / for directory browsing
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	h.renderBrowse(w, r, bucket, prefix)
}

func (h *uiHandler) renderBrowse(w http.ResponseWriter, r *http.Request, bucket, prefix string) {
	objects, commonPrefixes, _, err := h.store.ListObjects(bucket, prefix, "/", 1000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	component := browsePage(bucket, prefix, objects, commonPrefixes)
	component.Render(r.Context(), w)
}

func (h *uiHandler) handleUpload(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	prefix := r.FormValue("prefix")
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Failed to get file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	key := prefix + header.Filename

	_, err = h.store.PutObject(bucket, key, file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	redirectURL := fmt.Sprintf("/_ui/bucket/%s", bucket)
	if prefix != "" {
		redirectURL = fmt.Sprintf("/_ui/bucket/%s/browse/%s", bucket, strings.TrimSuffix(prefix, "/"))
	}
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

func (h *uiHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	key := r.FormValue("key")

	if err := h.store.DeleteObject(bucket, key); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	prefix := ""
	if idx := strings.LastIndex(key, "/"); idx >= 0 {
		prefix = key[:idx]
	}

	redirectURL := fmt.Sprintf("/_ui/bucket/%s", bucket)
	if prefix != "" {
		redirectURL = fmt.Sprintf("/_ui/bucket/%s/browse/%s", bucket, prefix)
	}
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

func (h *uiHandler) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	prefix := r.FormValue("prefix")
	folderName := r.FormValue("folder")

	if folderName == "" {
		http.Error(w, "Folder name required", http.StatusBadRequest)
		return
	}

	key := prefix + folderName + "/.keep"
	_, err := h.store.PutObject(bucket, key, strings.NewReader(""))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	redirectURL := fmt.Sprintf("/_ui/bucket/%s", bucket)
	if prefix != "" {
		redirectURL = fmt.Sprintf("/_ui/bucket/%s/browse/%s", bucket, strings.TrimSuffix(prefix, "/"))
	}
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

func (h *uiHandler) handleCreateBucket(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "Bucket name required", http.StatusBadRequest)
		return
	}

	if err := h.store.EnsureBucket(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/_ui", http.StatusSeeOther)
}

func (h *uiHandler) handleDeleteBucket(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")

	if err := h.store.DeleteBucket(bucket); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/_ui", http.StatusSeeOther)
}

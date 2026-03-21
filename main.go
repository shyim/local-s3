package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/shyim/local-s3/auth"
	"github.com/shyim/local-s3/s3"
	"github.com/shyim/local-s3/storage"
	"github.com/shyim/local-s3/ui"
)

func main() {
	addr := flag.String("addr", ":9000", "listen address")
	dataDir := flag.String("data", "./data", "data directory for object storage")
	flag.Parse()

	if env := os.Getenv("S3_LISTEN_ADDR"); env != "" {
		*addr = env
	}
	if env := os.Getenv("S3_DATA_DIR"); env != "" {
		*dataDir = env
	}

	store, err := storage.New(*dataDir)
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}

	accounts := auth.LoadFromEnv()

	// Auto-create buckets from S3_BUCKETS env var
	if buckets := os.Getenv("S3_BUCKETS"); buckets != "" {
		for _, name := range strings.Split(buckets, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if err := store.EnsureBucket(name); err != nil {
				log.Fatalf("Failed to create bucket %q: %v", name, err)
			}
			log.Printf("Bucket created: %s", name)
		}
	}

	mux := http.NewServeMux()

	// UI routes (must be registered before the S3 catch-all)
	ui.Register(mux, store, accounts)

	// S3 API catch-all
	s3Handler := s3.NewHandler(store, accounts)
	mux.Handle("/", s3Handler)

	log.Printf("Starting S3-compatible server on %s", *addr)
	log.Printf("Data directory: %s", *dataDir)
	log.Printf("Accounts configured: %d", len(accounts))
	for _, a := range accounts {
		log.Printf("  - %s (access key: %s, buckets: %v)", a.Name, a.AccessKeyID, a.Buckets)
	}
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

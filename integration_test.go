package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/shyim/local-s3/auth"
	s3handler "github.com/shyim/local-s3/s3"
	"github.com/shyim/local-s3/storage"
	"github.com/shyim/local-s3/ui"
)

const (
	testAccessKey = "testaccesskey"
	testSecretKey = "testsecretkey"
)

func setupTestServer(t *testing.T) (*s3.Client, func()) {
	t.Helper()

	dataDir, err := os.MkdirTemp("", "s3-test-*")
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}

	accounts := []auth.Account{
		{
			Name:           "test",
			AccessKeyID:    testAccessKey,
			SecretAccessKey: testSecretKey,
		},
	}

	mux := http.NewServeMux()
	ui.Register(mux, store, accounts)
	mux.Handle("/", s3handler.NewHandler(store, accounts))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	server := &http.Server{Handler: mux}
	go server.Serve(listener)

	endpoint := "http://" + listener.Addr().String()

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(testAccessKey, testSecretKey, "")),
	)
	if err != nil {
		t.Fatal(err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})

	cleanup := func() {
		server.Close()
		listener.Close()
		os.RemoveAll(dataDir)
	}

	return client, cleanup
}

func TestCreateBucket(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.TODO()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	if err != nil {
		t.Fatalf("CreateBucket failed: %v", err)
	}

	result, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		t.Fatalf("ListBuckets failed: %v", err)
	}

	if len(result.Buckets) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(result.Buckets))
	}
	if *result.Buckets[0].Name != "test-bucket" {
		t.Fatalf("expected bucket name 'test-bucket', got '%s'", *result.Buckets[0].Name)
	}
}

func TestHeadBucket(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.TODO()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	if err != nil {
		t.Fatalf("HeadBucket failed: %v", err)
	}

	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String("nonexistent"),
	})
	if err == nil {
		t.Fatal("HeadBucket should fail for nonexistent bucket")
	}
}

func TestPutGetObject(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.TODO()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	if err != nil {
		t.Fatal(err)
	}

	content := []byte("hello world")

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("test.txt"),
		Body:   bytes.NewReader(content),
	})
	if err != nil {
		t.Fatalf("PutObject failed: %v", err)
	}

	result, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("test.txt"),
	})
	if err != nil {
		t.Fatalf("GetObject failed: %v", err)
	}
	defer result.Body.Close()

	body, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(body, content) {
		t.Fatalf("expected %q, got %q", content, body)
	}
}

func TestHeadObject(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.TODO()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	if err != nil {
		t.Fatal(err)
	}

	content := []byte("hello")
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("test.txt"),
		Body:   bytes.NewReader(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("test.txt"),
	})
	if err != nil {
		t.Fatalf("HeadObject failed: %v", err)
	}

	if *head.ContentLength != int64(len(content)) {
		t.Fatalf("expected content length %d, got %d", len(content), *head.ContentLength)
	}
}

func TestDeleteObject(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.TODO()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("test.txt"),
		Body:   bytes.NewReader([]byte("hello")),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("test.txt"),
	})
	if err != nil {
		t.Fatalf("DeleteObject failed: %v", err)
	}

	_, err = client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("test.txt"),
	})
	if err == nil {
		t.Fatal("GetObject should fail after delete")
	}
}

func TestListObjectsV2(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.TODO()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	if err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		"docs/readme.txt":  "readme",
		"docs/guide.txt":   "guide",
		"images/photo.jpg": "photo",
		"root.txt":         "root",
	}

	for key, content := range files {
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String("test-bucket"),
			Key:    aws.String(key),
			Body:   bytes.NewReader([]byte(content)),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// List all objects
	result, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String("test-bucket"),
	})
	if err != nil {
		t.Fatalf("ListObjectsV2 failed: %v", err)
	}

	if *result.KeyCount != int32(len(files)) {
		t.Fatalf("expected %d objects, got %d", len(files), *result.KeyCount)
	}

	// List with delimiter to get "folders"
	result, err = client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:    aws.String("test-bucket"),
		Delimiter: aws.String("/"),
	})
	if err != nil {
		t.Fatalf("ListObjectsV2 with delimiter failed: %v", err)
	}

	if len(result.CommonPrefixes) != 2 {
		t.Fatalf("expected 2 common prefixes, got %d", len(result.CommonPrefixes))
	}

	if len(result.Contents) != 1 {
		t.Fatalf("expected 1 root object, got %d", len(result.Contents))
	}
	if *result.Contents[0].Key != "root.txt" {
		t.Fatalf("expected root object 'root.txt', got '%s'", *result.Contents[0].Key)
	}

	// List with prefix
	result, err = client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String("test-bucket"),
		Prefix: aws.String("docs/"),
	})
	if err != nil {
		t.Fatalf("ListObjectsV2 with prefix failed: %v", err)
	}

	if *result.KeyCount != 2 {
		t.Fatalf("expected 2 objects in docs/, got %d", *result.KeyCount)
	}
}

func TestNestedObjects(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.TODO()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	if err != nil {
		t.Fatal(err)
	}

	content := []byte("deeply nested content")
	key := "a/b/c/d/deep.txt"

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String(key),
		Body:   bytes.NewReader(content),
	})
	if err != nil {
		t.Fatalf("PutObject nested failed: %v", err)
	}

	result, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatalf("GetObject nested failed: %v", err)
	}
	defer result.Body.Close()

	body, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(body, content) {
		t.Fatalf("expected %q, got %q", content, body)
	}

	// Delete should clean up empty parent dirs
	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String(key),
	})
	if err != nil {
		t.Fatal(err)
	}

	list, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String("test-bucket"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if *list.KeyCount != 0 {
		t.Fatalf("expected 0 objects after delete, got %d", *list.KeyCount)
	}
}

func TestReadOnlyAccount(t *testing.T) {
	dataDir, err := os.MkdirTemp("", "s3-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dataDir)

	store, err := storage.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	store.EnsureBucket("test-bucket")

	accounts := []auth.Account{
		{
			Name:           "readonly",
			AccessKeyID:    "rokey",
			SecretAccessKey: "rosecret",
			ReadOnly:       true,
		},
	}

	mux := http.NewServeMux()
	ui.Register(mux, store, accounts)
	mux.Handle("/", s3handler.NewHandler(store, accounts))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("rokey", "rosecret", "")),
	)
	if err != nil {
		t.Fatal(err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String("http://" + listener.Addr().String())
		o.UsePathStyle = true
	})

	ctx := context.TODO()

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("test.txt"),
		Body:   bytes.NewReader([]byte("hello")),
	})
	if err == nil {
		t.Fatal("PutObject should fail for read-only account")
	}
}

func TestBucketRestriction(t *testing.T) {
	dataDir, err := os.MkdirTemp("", "s3-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dataDir)

	store, err := storage.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	store.EnsureBucket("allowed-bucket")
	store.EnsureBucket("forbidden-bucket")

	accounts := []auth.Account{
		{
			Name:           "restricted",
			AccessKeyID:    "rkey",
			SecretAccessKey: "rsecret",
			Buckets:        []string{"allowed-bucket"},
		},
	}

	mux := http.NewServeMux()
	ui.Register(mux, store, accounts)
	mux.Handle("/", s3handler.NewHandler(store, accounts))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("rkey", "rsecret", "")),
	)
	if err != nil {
		t.Fatal(err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String("http://" + listener.Addr().String())
		o.UsePathStyle = true
	})

	ctx := context.TODO()

	// Allowed bucket should work
	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String("allowed-bucket"),
	})
	if err != nil {
		t.Fatalf("HeadBucket on allowed bucket failed: %v", err)
	}

	// Forbidden bucket should fail
	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String("forbidden-bucket"),
	})
	if err == nil {
		t.Fatal("HeadBucket should fail for forbidden bucket")
	}

	// ListBuckets should only return allowed bucket
	result, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		t.Fatalf("ListBuckets failed: %v", err)
	}

	if len(result.Buckets) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(result.Buckets))
	}
	if *result.Buckets[0].Name != "allowed-bucket" {
		t.Fatalf("expected 'allowed-bucket', got '%s'", *result.Buckets[0].Name)
	}
}

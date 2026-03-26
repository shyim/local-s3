package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/shyim/local-s3/auth"
	s3handler "github.com/shyim/local-s3/s3"
	"github.com/shyim/local-s3/storage"
	"github.com/shyim/local-s3/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAccessKey = "testaccesskey"
	testSecretKey = "testsecretkey"
)

func setupTestServer(t *testing.T) (*s3.Client, func()) {
	t.Helper()

	dataDir, err := os.MkdirTemp("", "s3-test-*")
	require.NoError(t, err)

	store, err := storage.New(dataDir)
	require.NoError(t, err)

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
	require.NoError(t, err)

	server := &http.Server{Handler: mux}
	go server.Serve(listener)

	endpoint := "http://" + listener.Addr().String()

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(testAccessKey, testSecretKey, "")),
	)
	require.NoError(t, err)

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

func assertAPIErrorCode(t *testing.T, err error, code string) {
	t.Helper()

	require.Error(t, err)

	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr), "expected smithy API error, got %T", err)
	assert.Equal(t, code, apiErr.ErrorCode())
}

func TestCreateBucket(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.TODO()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	require.NoError(t, err)

	result, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
	require.NoError(t, err)

	require.Len(t, result.Buckets, 1)
	assert.Equal(t, "test-bucket", *result.Buckets[0].Name)
}

func TestHeadBucket(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.TODO()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	require.NoError(t, err)

	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	require.NoError(t, err)

	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String("nonexistent"),
	})
	assert.Error(t, err)
}

func TestPutGetObject(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.TODO()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	require.NoError(t, err)

	content := []byte("hello world")

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("test.txt"),
		Body:   bytes.NewReader(content),
	})
	require.NoError(t, err)

	result, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("test.txt"),
	})
	require.NoError(t, err)
	defer result.Body.Close()

	body, err := io.ReadAll(result.Body)
	require.NoError(t, err)

	assert.Equal(t, content, body)
}

func TestGetObjectMissingKey(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.TODO()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	require.NoError(t, err)

	_, err = client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("missing.txt"),
	})
	assertAPIErrorCode(t, err, "NoSuchKey")
}

func TestGetObjectMissingBucket(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.TODO()

	_, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("missing-bucket"),
		Key:    aws.String("test.txt"),
	})
	assertAPIErrorCode(t, err, "NoSuchBucket")
}

func TestHeadObject(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.TODO()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	require.NoError(t, err)

	content := []byte("hello")
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("test.txt"),
		Body:   bytes.NewReader(content),
	})
	require.NoError(t, err)

	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("test.txt"),
	})
	require.NoError(t, err)

	assert.Equal(t, int64(len(content)), *head.ContentLength)
}

func TestDeleteObject(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.TODO()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	require.NoError(t, err)

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("test.txt"),
		Body:   bytes.NewReader([]byte("hello")),
	})
	require.NoError(t, err)

	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("test.txt"),
	})
	require.NoError(t, err)

	_, err = client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("test.txt"),
	})
	assert.Error(t, err)
}

func TestListObjectsV2(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.TODO()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	require.NoError(t, err)

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
		require.NoError(t, err)
	}

	// List all objects
	result, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String("test-bucket"),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(len(files)), *result.KeyCount)

	// List with delimiter to get "folders"
	result, err = client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:    aws.String("test-bucket"),
		Delimiter: aws.String("/"),
	})
	require.NoError(t, err)
	assert.Len(t, result.CommonPrefixes, 2)
	require.Len(t, result.Contents, 1)
	assert.Equal(t, "root.txt", *result.Contents[0].Key)

	// List with prefix
	result, err = client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String("test-bucket"),
		Prefix: aws.String("docs/"),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), *result.KeyCount)
}

func TestNestedObjects(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.TODO()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	require.NoError(t, err)

	content := []byte("deeply nested content")
	key := "a/b/c/d/deep.txt"

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String(key),
		Body:   bytes.NewReader(content),
	})
	require.NoError(t, err)

	result, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String(key),
	})
	require.NoError(t, err)
	defer result.Body.Close()

	body, err := io.ReadAll(result.Body)
	require.NoError(t, err)
	assert.Equal(t, content, body)

	// Delete should clean up empty parent dirs
	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String(key),
	})
	require.NoError(t, err)

	list, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String("test-bucket"),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(0), *list.KeyCount)
}

func TestReadOnlyAccount(t *testing.T) {
	dataDir, err := os.MkdirTemp("", "s3-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(dataDir)

	store, err := storage.New(dataDir)
	require.NoError(t, err)
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
	require.NoError(t, err)
	defer listener.Close()

	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("rokey", "rosecret", "")),
	)
	require.NoError(t, err)

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
	assert.Error(t, err)
}

func TestPresignedGetObject(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.TODO()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	require.NoError(t, err)

	content := []byte("presigned download content")
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("presigned-test.txt"),
		Body:   bytes.NewReader(content),
	})
	require.NoError(t, err)

	presignClient := s3.NewPresignClient(client)
	presignResult, err := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("presigned-test.txt"),
	})
	require.NoError(t, err)

	resp, err := http.Get(presignResult.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, content, body)
}

func TestPresignedPutObject(t *testing.T) {
	client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.TODO()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	require.NoError(t, err)

	presignClient := s3.NewPresignClient(client)
	presignResult, err := presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("presigned-upload.txt"),
	})
	require.NoError(t, err)

	content := []byte("presigned upload content")
	req, err := http.NewRequest(http.MethodPut, presignResult.URL, bytes.NewReader(content))
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	result, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String("presigned-upload.txt"),
	})
	require.NoError(t, err)
	defer result.Body.Close()

	body, err := io.ReadAll(result.Body)
	require.NoError(t, err)
	assert.Equal(t, content, body)
}

func TestBucketRestriction(t *testing.T) {
	dataDir, err := os.MkdirTemp("", "s3-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(dataDir)

	store, err := storage.New(dataDir)
	require.NoError(t, err)
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
	require.NoError(t, err)
	defer listener.Close()

	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("rkey", "rsecret", "")),
	)
	require.NoError(t, err)

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String("http://" + listener.Addr().String())
		o.UsePathStyle = true
	})

	ctx := context.TODO()

	// Allowed bucket should work
	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String("allowed-bucket"),
	})
	require.NoError(t, err)

	// Forbidden bucket should fail
	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String("forbidden-bucket"),
	})
	assert.Error(t, err)

	// ListBuckets should only return allowed bucket
	result, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
	require.NoError(t, err)

	require.Len(t, result.Buckets, 1)
	assert.Equal(t, "allowed-bucket", *result.Buckets[0].Name)
}

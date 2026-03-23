# local-s3

A lightweight, single-binary S3-compatible server for local development and testing. Objects are stored on the local filesystem, buckets and permissions are configured via environment variables, and a built-in web UI lets you browse and manage files.

## Why not MinIO, RustFS, or Garage?

Tools like [MinIO](https://min.io/), [RustFS](https://rustfs.com/), and [Garage](https://garagehq.deuxfleurs.fr/) are production-grade, distributed storage systems. They're great for running in staging or production, but come with significant overhead for local development:

- **Complex setup** -- cluster configuration, distributed metadata, erasure coding, IAM policies
- **Slow bootstrapping** -- getting a working setup with buckets and credentials takes time and configuration

**local-s3** is purpose-built for the opposite use case: spin up a throwaway S3-compatible server in seconds for local development and testing. One binary, one environment variable, done. No cluster, no database, no configuration files. Pre-create buckets and accounts with env vars and tear it all down when you're finished.

If you need production durability, replication, or scalability, use one of the above. If you need a quick S3 endpoint for your `docker-compose.yml` or CI pipeline, use this.

## Features

- **S3-compatible API** with AWS Signature V4 authentication
- **Path-style routing** (`http://localhost:9000/bucket/key`)
- **Filesystem-backed storage** -- no database required
- **Environment-based configuration** for accounts, bucket access, and permissions
- **Web UI** at `/_ui` for browsing buckets and objects, uploading files, and managing folders

## Quick Start

### Prerequisites

- Go 1.22+
- [templ](https://templ.guide/) (`go install github.com/a-h/templ/cmd/templ@latest`)

### Run locally

```bash
# Start with a single admin account
S3_ACCOUNT_ADMIN=mykey:mysecret go run .

# Or use make
S3_ACCOUNT_ADMIN=mykey:mysecret make run
```

The server starts on `:9000` by default. Open [http://localhost:9000/_ui](http://localhost:9000/_ui) for the web UI.

### Docker

```bash
docker run -p 9000:9000 \
  -e S3_ACCOUNT_ADMIN=mykey:mysecret \
  -v s3data:/data \
  ghcr.io/shyim/local-s3:latest
```

Images are published for `linux/amd64` and `linux/arm64` to `ghcr.io/shyim/local-s3`. Available tags:

- `latest` - latest semver release
- `v1.0.0`, `v1.0`, `v1` - semver tags from releases

## Configuration

All configuration is done through environment variables or CLI flags.

### Server Settings

| Variable | Flag | Default | Description |
|---|---|---|---|
| `S3_LISTEN_ADDR` | `-addr` | `:9000` | Listen address |
| `S3_DATA_DIR` | `-data` | `./data` | Directory for stored objects |

### Accounts

Accounts are configured with environment variables following this pattern:

```bash
# Create an account (required)
S3_ACCOUNT_<NAME>=<access_key_id>:<secret_access_key>

# Restrict account to specific buckets (optional, default: all)
S3_ACCOUNT_<NAME>_BUCKETS=bucket1,bucket2

# Make account read-only (optional, default: false)
S3_ACCOUNT_<NAME>_READONLY=true
```

#### Examples

```bash
# Full-access admin
S3_ACCOUNT_ADMIN=AKIAIOSFODNN7EXAMPLE:wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY

# Read-only account restricted to one bucket
S3_ACCOUNT_READER=readerkey:readersecret
S3_ACCOUNT_READER_BUCKETS=public-assets
S3_ACCOUNT_READER_READONLY=true

# Account with access to specific buckets
S3_ACCOUNT_APP=appkey:appsecret
S3_ACCOUNT_APP_BUCKETS=uploads,media
```

If no accounts are configured, the server allows unauthenticated access.

## Supported S3 Operations

| Operation | Method | Path |
|---|---|---|
| ListBuckets | `GET /` | |
| CreateBucket | `PUT /<bucket>` | |
| HeadBucket | `HEAD /<bucket>` | |
| ListObjects | `GET /<bucket>` | |
| ListObjectsV2 | `GET /<bucket>?list-type=2` | |
| PutObject | `PUT /<bucket>/<key>` | |
| GetObject | `GET /<bucket>/<key>` | |
| HeadObject | `HEAD /<bucket>/<key>` | |
| DeleteObject | `DELETE /<bucket>/<key>` | |
| Presigned GetObject | `GET /<bucket>/<key>?X-Amz-Algorithm=...` | |
| Presigned PutObject | `PUT /<bucket>/<key>?X-Amz-Algorithm=...` | |

## Usage with AWS CLI

```bash
export AWS_ACCESS_KEY_ID=mykey
export AWS_SECRET_ACCESS_KEY=mysecret
export AWS_DEFAULT_REGION=us-east-1

# Create a bucket
aws --endpoint-url http://localhost:9000 s3 mb s3://my-bucket

# Upload a file
aws --endpoint-url http://localhost:9000 s3 cp myfile.txt s3://my-bucket/myfile.txt

# List objects
aws --endpoint-url http://localhost:9000 s3 ls s3://my-bucket/

# Download a file
aws --endpoint-url http://localhost:9000 s3 cp s3://my-bucket/myfile.txt downloaded.txt

# Delete a file
aws --endpoint-url http://localhost:9000 s3 rm s3://my-bucket/myfile.txt
```

## Usage with AWS SDKs

Point any S3 SDK at `http://localhost:9000` with path-style access and your configured credentials. Example with the Go SDK:

```go
cfg, _ := config.LoadDefaultConfig(context.TODO(),
    config.WithRegion("us-east-1"),
    config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("mykey", "mysecret", "")),
)

client := s3.NewFromConfig(cfg, func(o *s3.Options) {
    o.BaseEndpoint = aws.String("http://localhost:9000")
    o.UsePathStyle = true
})
```

## Web UI

The built-in web UI is available at `/_ui` and provides:

- Bucket overview with configured accounts
- Object browser with folder navigation and breadcrumbs
- File upload
- Object deletion
- Folder creation

## Project Structure

```
.
├── main.go           # Entry point
├── auth/             # AWS SigV4 authentication and account configuration
├── s3/               # S3 API request handler
├── storage/          # Filesystem-backed object storage
├── ui/               # Web UI (templ templates + handlers)
├── Dockerfile
└── Makefile
```

## Development

```bash
# Generate templ files
make generate

# Build binary
make build

# Run
make run
```

## License

MIT

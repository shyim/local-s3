FROM node:24-alpine AS tailwind

WORKDIR /app
RUN npm install tailwindcss @tailwindcss/cli
COPY static/input.css static/input.css
COPY ui/templates.templ ui/templates.templ
RUN npx @tailwindcss/cli -i static/input.css -o static/output.css --minify

FROM golang:1.26-alpine AS builder

RUN go install github.com/a-h/templ/cmd/templ@latest

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=tailwind /app/static/output.css static/output.css
RUN templ generate
RUN CGO_ENABLED=0 go build -o /local-s3 .

FROM alpine
RUN apk add --no-cache ca-certificates
COPY --from=builder /local-s3 /usr/local/bin/local-s3
EXPOSE 9000
VOLUME /data
ENV S3_DATA_DIR=/data
ENTRYPOINT ["local-s3"]

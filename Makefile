.PHONY: build run generate clean

build: generate
	go build -o local-s3 .

run: generate
	go run .

generate: tailwind
	templ generate

tailwind:
	npx @tailwindcss/cli -i static/input.css -o static/output.css --minify

clean:
	rm -f local-s3

.PHONY: build run test

build:
	CGO_ENABLED=1 go build -tags sqlite_omit_load_extension -o bin/ghal ./cmd/ghal

run:
	go run ./cmd/ghal

test:
	go test -race ./...

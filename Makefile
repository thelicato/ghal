.PHONY: build run test

build:
	CGO_ENABLED=1 go build -tags sqlite_omit_load_extension -o bin/dbaui ./cmd/dbaui

run:
	go run ./cmd/dbaui

test:
	go test -race ./...

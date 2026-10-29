.PHONY: build run test vulncheck

build:
	CGO_ENABLED=1 go build -tags sqlite_omit_load_extension -o bin/ghal ./cmd/ghal

run:
	go run ./cmd/ghal

test:
	go test -race ./...

vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 -C cmd/ghal -scan module

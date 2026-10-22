.PHONY: build run test

build:
	pnpm build
	go build -o bin/dbaui ./cmd/dbaui

run:
	go run ./cmd/dbaui

test:
	go test -race ./...
	pnpm check

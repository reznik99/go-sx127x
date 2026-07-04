.PHONY: build test lint tidy

build:
	go build ./...

test:
	go test -race -coverprofile=coverage.out ./...

lint:
	golangci-lint run ./...

tidy:
	go mod tidy

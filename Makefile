.PHONY: build test clean

build:
	go build -o bin/controller ./cmd/controller
	go build -o bin/agent ./cmd/agent
	go build -o bin/cli ./cmd/cli

test:
	go test -v ./...

clean:
	rm -rf bin/

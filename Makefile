.PHONY: build test clean

build:
	go build -o bin/angarium-controller ./cmd/controller
	go build -o bin/angarium-agent ./cmd/agent
	go build -o bin/angarium ./cmd/cli

test:
	go test -v ./...

clean:
	rm -rf bin/

certs:
	mkdir -p config/certs
	openssl req -x509 -newkey rsa:4096 -keyout config/certs/key.pem -out config/certs/cert.pem -sha256 -days 365 -nodes -subj "/C=US/ST=State/L=City/O=Angarium/OU=Dev/CN=localhost"

release:
	GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o bin/angarium-controller ./cmd/controller
	GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o bin/angarium-agent ./cmd/agent
	GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o bin/angarium ./cmd/cli
	tar -czf angarium-linux-amd64.tar.gz -C bin angarium-controller angarium-agent angarium
	@echo "Release package created: angarium-linux-amd64.tar.gz"

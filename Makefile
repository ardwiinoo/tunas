.PHONY: test lint fmt build build-agent

test:
	go test ./...

lint:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "gofmt needed" && exit 1)
	go vet ./...

fmt:
	gofmt -w .

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/tunas-server ./cmd/server

build-agent:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H=windowsgui" -o bin/tunas-agent.exe ./cmd/agent

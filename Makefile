.PHONY: build test dist
VERSION ?= dev
LDFLAGS := -X main.version=$(VERSION)
build:
	go build -ldflags "$(LDFLAGS)" -o dlg .
test:
	go test ./...
# Static binaries for other platforms (no CGO, no runtime needed).
dist:
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/dlg-darwin-arm64 .
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/dlg-darwin-amd64 .
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/dlg-linux-amd64 .
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/dlg-linux-arm64 .
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/dlg-windows-amd64.exe .

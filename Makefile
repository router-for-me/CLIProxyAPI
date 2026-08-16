.PHONY: build build-amd build-windows test clean

build:
	go build -trimpath -ldflags '-s -w' -o cc-proxy ./cmd/server/main.go

build-amd:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o cc-proxy ./cmd/server/main.go

build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o cc-proxy.exe ./cmd/server/main.go

test:
	go test ./...

clean:
	rm -f cc-proxy cc-proxy.exe
export GO111MODULE=on

build:
		go build -i

vendor:
		go mod vendor

test:
		go test -race ./...

.PHONY: all build vendor utils test clean

.PHONY: build test vet clean

build:
	go build -o bin/gh-inspect ./cmd/gh-inspect

test:
	go test -race ./...

vet:
	go vet ./...

clean:
	rm -rf bin

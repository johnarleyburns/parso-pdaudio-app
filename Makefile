.PHONY: build clean test vet fmt

build:
	go build -o bin/parso-pdaudio .

clean:
	rm -rf bin/

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

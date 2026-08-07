default: build

build:
	go build -o nixmox ./cmd/nixmox

test:
	go test ./...

lint:
	golangci-lint run

vet:
	go vet ./...

fmt:
	gofmt -s -w .

tidy:
	go mod tidy

clean:
	rm -f nixmox

run: build
	./nixmox serve

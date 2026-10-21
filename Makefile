.PHONY: build test race vet lint clean

build:
	go build -trimpath -o perch ./cmd/perch

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

lint:
	golangci-lint run

clean:
	rm -f perch

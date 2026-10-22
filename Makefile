.PHONY: dev doctor test vet check fmt build

dev:
	go tool mygo dev

doctor:
	go tool mygo doctor

test:
	go test ./...

vet:
	go vet ./...

check: test vet

fmt:
	gofmt -w main.go internal cmd

build:
	go tool mygo build

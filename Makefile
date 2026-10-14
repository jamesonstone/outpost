.DEFAULT_GOAL := help
.PHONY: help build test check fmt integration clean
help:
	@printf '%s\n' 'Outpost developer workflow' 'make build       Compile the service' 'make test        Native tests with race detection' 'make integration Require synthetic PostgreSQL and run all tests' 'make check       Formatting, vet, tests, and Kit contract' 'make fmt         Format Go sources' 'make clean       Remove the generated binary'
build:
	go build -o tmp/outpost ./cmd/outpost
test:
	go test -race ./...
integration:
	@test -n "$$OUTPOST_TEST_DSN" || (echo 'Set OUTPOST_TEST_DSN to an isolated loopback PostgreSQL database'; exit 1)
	go test -race -count=1 ./...
check:
	@test -z "$$(gofmt -l cmd internal tests)" || (echo 'Run make fmt'; exit 1)
	go vet ./...
	go test -race ./...
	kit check --project
fmt:
	gofmt -w cmd internal tests
clean:
	$(RM) tmp/outpost

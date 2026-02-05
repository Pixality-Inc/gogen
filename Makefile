.PHONY: all
all: dep gen lint test

.PHONY: gen
gen:
	go generate ./...
	go fmt ./...

.PHONY: dep
dep:
	go mod tidy
	go mod download
	go mod vendor

.PHONY: test
test:
	go test ./...

.PHONY: lint
lint:
	golangci-lint run --tests

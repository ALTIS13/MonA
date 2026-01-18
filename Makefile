.PHONY: build test fmt tidy

build:
\tgo build ./...

test:
\tgo test ./...

fmt:
\tgofmt -w .

tidy:
\tgo mod tidy


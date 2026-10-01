.PHONY: build test
SERVICES = users tweets social gateway admin bot

build:
	@mkdir -p build
	@set -e; for service in $(SERVICES); do go build -trimpath -o build/$$service ./cmd/$$service; done

test:
	go test ./...

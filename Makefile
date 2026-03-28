BINARY := meshsat
BUILD_DIR := build
GO := CGO_ENABLED=0 go

.PHONY: build build-arm64 build-x86_64 run test fmt lint tidy clean docker web build-with-web

build:
	$(GO) build -o $(BUILD_DIR)/$(BINARY) ./cmd/meshsat

build-arm64:
	GOOS=linux GOARCH=arm64 $(GO) build -o $(BUILD_DIR)/$(BINARY)-arm64 ./cmd/meshsat

build-x86_64:
	GOOS=linux GOARCH=amd64 $(GO) build -o $(BUILD_DIR)/$(BINARY)-amd64 ./cmd/meshsat

run:
	$(GO) run ./cmd/meshsat

test:
	$(GO) test -v ./...

fmt:
	gofmt -w .

lint:
	golangci-lint run ./...

tidy:
	go mod tidy

clean:
	rm -rf $(BUILD_DIR)

docker:
	docker build -t localhost:5000/cubeos-app/meshsat:latest .

web:
	cd web && npm ci --no-audit && npm run build
	rm -rf cmd/meshsat/web/dist
	cp -r web/dist cmd/meshsat/web/dist

jspr-helper:
	gcc -O2 -Wall -static -o $(BUILD_DIR)/jspr-helper cmd/jspr-helper/main.c

jspr-helper-arm64:
	aarch64-linux-gnu-gcc -O2 -Wall -static -o $(BUILD_DIR)/jspr-helper-arm64 cmd/jspr-helper/main.c

build-with-web: web build

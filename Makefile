GO_PROJECT_NAME := northstar-dns

# GO commands
go_build:
	@echo "\n....Building $(GO_PROJECT_NAME)"
	go build -ldflags "-s -w" -o ./bin/ ./cmd/northstar

go_dep_install:
	@echo "\n....Installing dependencies for $(GO_PROJECT_NAME)...."
	go get .

go_run:
	@echo "\n....Running $(GO_PROJECT_NAME)...."
	$(GOPATH)/bin/$(GO_PROJECT_NAME)

test:
	@echo "\n....Running tests for $(GO_PROJECT_NAME)...."
	NORTHSTAR_LOG_IGNORE=1 go test -v ./pkg/backend
	NORTHSTAR_LOG_IGNORE=1 go test -v ./pkg/config

# Project rules
build:
	$(MAKE) go_build

bench:
	NORTHSTAR_LOG_IGNORE=1 go test -bench=. ./pkg/backend -count 5 -benchmem | tee benchmark.out
	benchstat benchmark.out

prof:
	NORTHSTAR_LOG_IGNORE=1 go test -cpuprofile cpu.prof -memprofile mem.prof -bench=. ./pkg/backend

race:
	NORTHSTAR_LOG_IGNORE=1 go test -race ./pkg/backend

run:
ifeq ($(ENV), dev)
	$(MAKE) build
	$(GOPATH)/bin/gin
else
	$(MAKE) go_build
	$(MAKE) go_run
endif

clean:
	rm -rf ./bin/*

# E2E tests using Docker (predastore + northstar)
e2e:
	@echo "\n....Running E2E tests...."
	cd e2e && docker compose up -d --build
	@echo "Waiting for services to start..."
	sleep 5
	cd e2e && NORTHSTAR_E2E=1 go test -v -timeout 120s ./...
	cd e2e && docker compose down -v

e2e-down:
	cd e2e && docker compose down -v

# Full test suite
test-all:
	$(MAKE) test
	$(MAKE) race

docker:
	@echo "\n....Building latest docker image and uploading ...."
	$(MAKE) test
	docker buildx build --push --platform linux/arm/v7,linux/arm64/v8,linux/amd64 --tag calacode/$(GO_PROJECT_NAME):latest .

.PHONY: docker go_build go_dep_install go_run build run clean test test-all e2e e2e-down bench prof race

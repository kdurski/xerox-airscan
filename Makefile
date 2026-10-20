GO ?= go
CAPTURE_DIR ?= .

.PHONY: build test test-captures check run clean service-install service-status service-restart service-stop service-start service-uninstall

build:
	$(GO) build -trimpath -o bin/xerox-airscan ./cmd/xerox-airscan

test:
	$(GO) test -race ./...

test-captures:
	XEROX_TEST_CAPTURE_DIR="$(abspath $(CAPTURE_DIR))" $(GO) test -race ./internal/xerox -run 'TestReplayReal' -count=1

check:
	$(GO) vet ./...

run: build
	./bin/xerox-airscan serve

clean:
	rm -rf bin

service-install service-status service-restart service-stop service-start service-uninstall:
	/bin/zsh tools/service.sh $(patsubst service-%,%,$@)

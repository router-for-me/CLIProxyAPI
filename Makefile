APP := CLIProxyAPI
CONFIG := ./config.yaml
CMD := ./cmd/server
GO := $(HOME)/.local/sdk/go1.26.1/bin/go

.PHONY: build run run-local clean

build:
	env -u GOROOT $(GO) build -o ./$(APP) $(CMD)

run: build
	./$(APP) -config $(CONFIG)

run-local: build
	./$(APP) -config $(CONFIG) -local-model

clean:
	rm -f ./$(APP)

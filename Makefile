.PHONY: test lint build integration

test:
	go test ./...

lint:
	uvx pre-commit run --all-files

build:
	go build ./...

integration:
	go test -tags integration ./internal/infra/minikube -run TestClusterLifecycle -v -timeout 30m

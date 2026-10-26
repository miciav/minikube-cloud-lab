.PHONY: test lint build integration images

test:
	go test ./...

lint:
	uvx pre-commit run --all-files

build:
	go build ./...

integration:
	go test -tags integration ./internal/infra/minikube -run TestClusterLifecycle -v -timeout 30m

images:
	docker build --target cloud -t minikube-cloud-lab/cloud:dev .
	docker build --target lab -t minikube-cloud-lab/lab:dev .

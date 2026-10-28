SHELL := /bin/bash
.SHELLFLAGS := -euo pipefail -c

.PHONY: test lint build integration images verify tidy-check vuln cover race

test:
	go test ./...

lint:
	uvx pre-commit run --all-files

build:
	go build ./...

integration:
	go test -count=1 -tags integration ./internal/infra/minikube -run TestClusterLifecycle -v -timeout 30m

images:
	docker build --target nfs -t minikube-cloud-lab/nfs:dev .
	docker build --target cloud -t minikube-cloud-lab/cloud:dev .
	docker build --target lab -t minikube-cloud-lab/lab:dev .

verify: lint tidy-check vuln cover race

tidy-check:
	go mod tidy -diff
	go mod verify

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -json ./... > vuln-report.json
	go run ./tools/vulncheck < vuln-report.json

race:
	go test -race ./...

cover:
	# Generated protocol bindings follow the reference project coverage scope.
	go test -coverprofile=coverage.out $$(go list ./... | grep -v -E '/internal/infra/(minikube|nfs)$$|/proto$$')
	go tool cover -func=coverage.out | awk '/^total:/ { sub(/%/, "", $$3); print "Go coverage " $$3 "% (minimum 90%)"; exit ($$3 + 0 < 90) }'

# Mount/loop operations run only inside an isolated Linux container.
storage-integration:
	bash tests/storage-backend.sh

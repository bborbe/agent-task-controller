include ../../Makefile.variables
include tools.env
include ../../Makefile.docker
include ../../Makefile.env
include ../../common.env

SERVICE = agent-task-controller

export ROOTDIR ?= $(shell git rev-parse --show-toplevel)

default: precommit

.PHONY: precommit ensure format generate check lint test vet errcheck gosec addlicense vulncheck osv-scanner trivy

precommit: ensure format generate test check addlicense
	@echo "ready to commit"

ensure:
	go mod tidy
	go mod verify
	rm -rf vendor

format:
	find . -type f -name 'go.mod' -not -path './vendor/*' -exec go run github.com/shoenig/go-modtool@$(GO_MODTOOL_VERSION) -w fmt "{}" \;
	find . -type f -name '*.go' -not -path './vendor/*' -exec gofmt -w "{}" +
	go run github.com/incu6us/goimports-reviser/v3@$(GOIMPORTS_REVISER_VERSION) -project-name github.com/bborbe/agent -format -excludes vendor ./...
	find . -type d -name vendor -prune -o -type f -name '*.go' -print0 | xargs -0 -n 10 go run github.com/segmentio/golines@$(GOLINES_VERSION) --max-len=100 -w

generate:
	rm -rf mocks avro
	mkdir -p mocks
	echo "package mocks" > mocks/mocks.go
	go generate -mod=mod ./...

test:
	go test -mod=mod -p=$${GO_TEST_PARALLEL:-1} -cover -race $(shell go list -mod=mod ./... | grep -v /vendor/)

# errcheck removed — embedded in golangci-lint (see .golangci.yml).
# gosec removed — embedded in golangci-lint (see .golangci.yml).
# Standalone errcheck/gosec fatal under Go 1.26+ (NeedDeps issue in package loader).
check: lint vet vulncheck osv-scanner trivy

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run --config $(ROOTDIR)/.golangci.yml ./...

vet:
	go vet -mod=mod $(shell go list -mod=mod ./... | grep -v /vendor/)

vulncheck:
	@go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) -format json $(shell go list -mod=mod ./... | grep -v /vendor/) 2>&1 | \
		jq -e 'select(.finding != null and .finding.osv != "GO-2026-4923" and .finding.osv != "GO-2026-4514" and .finding.osv != "GO-2022-0470" and .finding.osv != "GO-2026-4772" and .finding.osv != "GO-2026-4771")' > /dev/null 2>&1 && \
		{ echo "Unexpected vulnerabilities found"; go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) $(shell go list -mod=mod ./... | grep -v /vendor/); exit 1; } || \
		echo "No unignored vulnerabilities found"

osv-scanner:
	@if [ -f .osv-scanner.toml ]; then \
		echo "Using .osv-scanner.toml"; \
		go run github.com/google/osv-scanner/v2/cmd/osv-scanner@$(OSV_SCANNER_VERSION) --config .osv-scanner.toml --recursive .; \
	elif [ -f $(ROOTDIR)/.osv-scanner.toml ]; then \
		echo "Using $(ROOTDIR)/.osv-scanner.toml"; \
		go run github.com/google/osv-scanner/v2/cmd/osv-scanner@$(OSV_SCANNER_VERSION) --config $(ROOTDIR)/.osv-scanner.toml --recursive .; \
	else \
		echo "No config found, running default scan"; \
		go run github.com/google/osv-scanner/v2/cmd/osv-scanner@$(OSV_SCANNER_VERSION) --recursive .; \
	fi

trivy:
	trivy fs \
	--db-repository ghcr.io/aquasecurity/trivy-db \
	$(if $(wildcard .trivyignore),--ignorefile .trivyignore,$(if $(wildcard $(ROOTDIR)/.trivyignore),--ignorefile $(ROOTDIR)/.trivyignore,)) \
	--scanners vuln,secret \
	--skip-dirs vendor \
	--quiet \
	--no-progress \
	--disable-telemetry \
	--exit-code 1 .

addlicense:
	go run github.com/google/addlicense@$(ADDLICENSE_VERSION) -c "Benjamin Borbe" -y $$(date +'%Y') -l bsd $$(find . -name "*.go" -not -path './vendor/*')

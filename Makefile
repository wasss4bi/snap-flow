MONITORING := docker compose -f compose.test.yaml -f compose.observability.yaml
LINT_IMAGE ?= golangci/golangci-lint:v2.1.6
K6_UPLOAD_VUS ?= 1
# Git Bash otherwise rewrites container paths such as /app on Windows.
export MSYS_NO_PATHCONV := 1

.PHONY: help lint test test-integration smoke run stop logs ps load load-upload k6 k6-upload
.DEFAULT_GOAL := help

help:
	@echo "make lint              Run golangci-lint in Docker"
	@echo "make test              Run unit, integration, and smoke tests"
	@echo "make test-integration  Run unit and integration tests"
	@echo "make smoke             Run end-to-end smoke tests"
	@echo "make run               Build and start the local project"
	@echo "make stop              Stop the local project, keeping its volumes"
	@echo "make logs              Follow application logs"
	@echo "make ps                Show local service status"
	@echo "make load              Run the k6 read load test"
	@echo "make load-upload       Run a writing k6 test with a dedicated account"
	@echo "make k6                Alias for the k6 read load test"
	@echo "make k6-upload         Alias for the k6 upload load test"

lint:
	docker run --rm -v "$(CURDIR):/app" -w /app $(LINT_IMAGE) golangci-lint run ./...

test:
	$(MONITORING) --profile test run --build --rm tests
	$(MONITORING) --profile test run --build --rm smoke

test-integration:
	$(MONITORING) --profile test run --build --rm tests

smoke:
	$(MONITORING) --profile test run --build --rm smoke

run:
	$(MONITORING) up --build -d --wait

stop:
	$(MONITORING) down

logs:
	$(MONITORING) logs -f server worker outbox

ps:
	$(MONITORING) ps

load:
	$(MONITORING) --profile loadtest run --rm --no-deps k6

load-upload:
	$(MONITORING) --profile loadtest run --rm --no-deps -e K6_ALLOW_WRITES=1 -e K6_VUS=$(K6_UPLOAD_VUS) k6 run -o experimental-prometheus-rw /scripts/upload.js

k6: load

k6-upload: load-upload

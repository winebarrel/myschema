export MYSQL_HOST  := 127.0.0.1
export MYSQL_PORT  := 3306
export MYSQL_USER  := root
export MYSQL_PWD   :=
export MYSQL_DB    := myschema_test

# Default DSN used by tests / scenario scripts. Override with MYSCHEMA_TEST_DSN
# if your local MySQL is reachable on a different host / port / user.
export MYSCHEMA_TEST_DSN ?= $(MYSQL_USER)@tcp($(MYSQL_HOST):$(MYSQL_PORT))/

MYSQL := mysql -h $(MYSQL_HOST) -P $(MYSQL_PORT) -u $(MYSQL_USER) --skip-ssl

.PHONY: all
all: vet test build

.PHONY: build
build:
	go build ./cmd/myschema

.PHONY: vet
vet:
	go vet ./...

# `-p 1` because integration tests share a single MySQL instance.
.PHONY: test
test:
	go test -p 1 -v ./... $(TEST_OPTS)

.PHONY: test-unit
test-unit:
	go test -v ./parser/... ./diff/... ./model/...

.PHONY: lint
lint:
	golangci-lint run

.PHONY: fix
fix:
	golangci-lint run --fix

.PHONY: test-scenario
test-scenario:
	bash test/scenario/run.sh

.PHONY: clean-schema
clean-schema:
	$(MYSQL) -e 'DROP DATABASE IF EXISTS $(MYSQL_DB); CREATE DATABASE $(MYSQL_DB)'

.PHONY: schema
schema: clean-schema
	@echo "(no sample-DB targets defined yet — see TODO.md)"

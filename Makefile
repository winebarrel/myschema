# `?=` so the matrix CI (and any caller) can override these via the
# environment. `:=` would silently win over the env value because Make
# assignments override env unless `-e` is passed.
MYSQL_HOST ?= 127.0.0.1
MYSQL_PORT ?= 3306
MYSQL_USER ?= root
MYSQL_PWD  ?=
MYSQL_DB   ?= myschema_test
export MYSQL_HOST MYSQL_PORT MYSQL_USER MYSQL_PWD MYSQL_DB

# Default DSN used by tests / scenario scripts. The trailing slash makes it
# easy for tests to append the test DB name; the production CLI requires the
# database to be in the DSN (testutil + test_helper take care of that).
# Override with MYSCHEMA_TEST_DSN if MySQL lives elsewhere.
export MYSCHEMA_TEST_DSN ?= $(MYSQL_USER)@tcp($(MYSQL_HOST):$(MYSQL_PORT))/

MYSQL := mysql -h $(MYSQL_HOST) -P $(MYSQL_PORT) -u $(MYSQL_USER)

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

# test-mysql9 runs the full integration suite against the MySQL 9.x
# compose service on port 3307. Both MYSQL_PORT and MYSCHEMA_TEST_DSN
# are overridden explicitly so a caller with MYSCHEMA_TEST_DSN already
# set in the environment doesn't accidentally hit the 8.0 instance.
.PHONY: test-mysql9
test-mysql9:
	$(MAKE) test MYSQL_PORT=3307 \
	  MYSCHEMA_TEST_DSN='$(MYSQL_USER)@tcp($(MYSQL_HOST):3307)/'

.PHONY: clean-schema-mysql9
clean-schema-mysql9:
	$(MAKE) clean-schema MYSQL_PORT=3307

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

# `schema` loads three permissively-licensed sample DBs (chinook, employees,
# sakila) into separate databases on the local MySQL so you can poke at
# realistic schemas with `myschema dump` / `plan`. Each loader fetches the
# upstream artefact, pipes it through the mysql client, and cleans up.
#
# Once loaded, point myschema at any of them:
#   MYSCHEMA_DSN='root@tcp(127.0.0.1:3306)/Chinook'    ./myschema dump
#   MYSCHEMA_DSN='root@tcp(127.0.0.1:3306)/employees'  ./myschema dump
#   MYSCHEMA_DSN='root@tcp(127.0.0.1:3306)/sakila'     ./myschema dump
.PHONY: schema
schema: load-chinook load-employees load-sakila

.PHONY: load-chinook
load-chinook:
	@tmp=$$(mktemp -d) && trap "rm -rf $$tmp" EXIT && \
	curl -sSfL -o $$tmp/chinook.sql \
	  https://raw.githubusercontent.com/lerocha/chinook-database/master/ChinookDatabase/DataSources/Chinook_MySql.sql && \
	$(MYSQL) < $$tmp/chinook.sql

.PHONY: load-employees
load-employees:
	@tmp=$$(mktemp -d) && trap "rm -rf $$tmp" EXIT && \
	git clone --depth 1 --quiet https://github.com/datacharmer/test_db.git $$tmp/test_db && \
	cd $$tmp/test_db && \
	grep -v '^source ' employees.sql | $(MYSQL)
# `source load_X.dump;` lines load data; we only want schema, so drop them.
# (mysql 8 also rejects `source` outside interactive mode, so this is doubly
# necessary.)

.PHONY: load-sakila
load-sakila:
	@tmp=$$(mktemp -d) && trap "rm -rf $$tmp" EXIT && \
	curl -sSfL -o $$tmp/sakila-db.tar.gz https://downloads.mysql.com/docs/sakila-db.tar.gz && \
	tar -xzf $$tmp/sakila-db.tar.gz -C $$tmp && \
	$(MYSQL) < $$tmp/sakila-db/sakila-schema.sql && \
	$(MYSQL) < $$tmp/sakila-db/sakila-data.sql

.PHONY: schema-drop
schema-drop:
	$(MYSQL) -e 'DROP DATABASE IF EXISTS Chinook; DROP DATABASE IF EXISTS employees; DROP DATABASE IF EXISTS sakila'

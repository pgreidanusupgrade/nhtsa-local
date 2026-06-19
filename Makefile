.PHONY: build-db convert build run test test-unit test-gob test-sqlite test-integration clean

# Step 1: build the postgres image — auto-downloads current month's NHTSA VPIC release
# To pin a specific release: make build-db VPIC_URL=https://vpic.nhtsa.dot.gov/downloads/vPICList_full_2026_06.plain.zip
build-db:
	podman build $(if $(VPIC_URL),--build-arg VPIC_URL="$(VPIC_URL)",) -t vpic-db ./db

# Step 2: start postgres, run the converter, stop postgres
# Produces api-gob/vpic.gob.gz (gob format) and api-sqlite/vpic.sqlite (SQLite)
convert: build-db
	podman rm --force --ignore vpic-db-tmp
	podman run -d --name vpic-db-tmp -e POSTGRES_DB=vpic -e POSTGRES_USER=vpic -e POSTGRES_PASSWORD=vpic -p 5432:5432 vpic-db
	until podman exec vpic-db-tmp pg_isready -U vpic -d vpic; do sleep 1; done
	podman build -t vpic-converter ./converter
	podman run --rm -e DATABASE_URL="postgres://vpic:vpic@host.containers.internal:5432/vpic?sslmode=disable" -e OUTPUT_PATH=/out/vpic.sqlite -v "$(PWD)/api-sqlite:/out" vpic-converter
	cp api-sqlite/vpic.sqlite api-gob/vpic.sqlite
	cd /tmp/sqlite_to_gob && go run . $(PWD)/api-gob/vpic.sqlite $(PWD)/api-gob/vpic.gob.gz
	gzip -k -9 api-sqlite/vpic.sqlite -c > api-sqlite/vpic.sqlite.gz
	podman stop vpic-db-tmp
	podman rm vpic-db-tmp

# Unit tests: run inside each module (require vpic data files; skip gracefully if absent)
test-gob:
	cd api-gob && go test -v ./...

test-sqlite:
	cd api-sqlite && go test -v ./...

test-unit: test-gob test-sqlite

# Integration tests: spin up both containers, verify parity and NHTSA correctness,
# then tear them down. GOB_URL / SQLITE_URL override the default localhost ports.
#
# Flags:
#   INTEG_SHORT=1   only test the 6 curated probe VINs (fast smoke test)
#   INTEG_TIMEOUT   go test -timeout value (default: 5m)
INTEG_FLAGS :=
ifdef INTEG_SHORT
INTEG_FLAGS += -short
endif
INTEG_TIMEOUT ?= 5m

test-integration:
	podman compose up -d --build
	scripts/wait-healthy.sh
	cd integration && go test -v -timeout $(INTEG_TIMEOUT) $(INTEG_FLAGS) ./...
	podman compose down

# Full test suite: unit tests for both modules + integration tests
test: test-unit test-integration

# Step 3: build both API images
build:
	podman compose build

# Step 4: run api-gob on :8080, api-sqlite on :8081
run:
	podman compose up

# All in one
all: convert test build run

clean:
	podman compose down
	rm -f api-gob/vpic.sqlite api-gob/vpic.gob.gz
	rm -f api-sqlite/vpic.sqlite api-sqlite/vpic.sqlite.gz

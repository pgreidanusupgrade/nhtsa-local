.PHONY: build-db convert build run clean

# Step 1: build the postgres image with NHTSA data loaded
build-db:
	docker build -t vpic-db -f Dockerfile.db .

# Step 2: start postgres, run the converter, stop postgres
# Produces api/vpic.sqlite
convert: build-db
	docker run -d --name vpic-db-tmp -e POSTGRES_DB=vpic -e POSTGRES_USER=vpic -e POSTGRES_PASSWORD=vpic -p 5432:5432 vpic-db
	until docker exec vpic-db-tmp pg_isready -U vpic -d vpic; do sleep 1; done
	docker build -t vpic-converter ./converter
	docker run --rm --network host -e DATABASE_URL="postgres://vpic:vpic@localhost:5432/vpic?sslmode=disable" -e OUTPUT_PATH=/out/vpic.sqlite -v "$(PWD)/api:/out" vpic-converter
	docker stop vpic-db-tmp
	docker rm vpic-db-tmp

# Step 3: build the final API image (embeds api/vpic.sqlite)
build:
	docker compose build

# Step 4: run the API on :8080
run:
	docker compose up

# All in one
all: convert build run

clean:
	docker compose down
	rm -f api/vpic.sqlite

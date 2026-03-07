# Simple Makefile for a Go project

MIGRATIONS_DIR=./internal/database/migrations
DB_URL=./data/tellarr.db

migrate-up:
	goose -dir $(MIGRATIONS_DIR) sqlite3 $(DB_URL) up

migrate-down:
	goose -dir $(MIGRATIONS_DIR) sqlite3 $(DB_URL) down

migrate-status:
	goose -dir $(MIGRATIONS_DIR) sqlite3 $(DB_URL) status

migrate-create:
	@read -p "Migration name: " name; \
	goose -dir $(MIGRATIONS_DIR) create $$name sql
	
# Build the application
all: build test

build:
	@echo "Building..."
	
	
	@go build -o main cmd/api/main.go

# Run the application
run:
	@go run cmd/api/main.go

# Test the application
test:
	@echo "Testing..."
	@go test ./... -v

# Clean the binary
clean:
	@echo "Cleaning..."
	@rm -f main

# Live Reload
watch:
	@if command -v air > /dev/null; then \
            air; \
            echo "Watching...";\
        else \
            read -p "Go's 'air' is not installed on your machine. Do you want to install it? [Y/n] " choice; \
            if [ "$$choice" != "n" ] && [ "$$choice" != "N" ]; then \
                go install github.com/air-verse/air@latest; \
                air; \
                echo "Watching...";\
            else \
                echo "You chose not to install air. Exiting..."; \
                exit 1; \
            fi; \
        fi

.PHONY: all build run test clean watch migrate-up migrate-down migrate-status migrate-create


.PHONY: build install uninstall test test-short test-verbose fmt lint clean help

# Build variables
BINARY_NAME=greek-ls
BUILD_DIR=bin
CMD_DIR=./cmd/greek-ls
DB_NAME=lemma_greek_en.db

# Install locations. greek-ls looks for the dictionary in
# $XDG_DATA_HOME/greek-ls (default ~/.local/share/greek-ls); if you change
# DATADIR, point the server at it with init_options.db or $GREEK_LS_DB.
BINDIR ?= $(HOME)/.local/bin
DATADIR ?= $(or $(XDG_DATA_HOME),$(HOME)/.local/share)/greek-ls

# Build the server into bin/
build:
	@echo "Building $(BINARY_NAME)..."
	@go build -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

# Install the server and its dictionary
install: build $(DATADIR)/$(DB_NAME)
	@install -d $(BINDIR)
	@install -m 755 $(BUILD_DIR)/$(BINARY_NAME) $(BINDIR)/$(BINARY_NAME)
	@echo "Installed $(BINDIR)/$(BINARY_NAME)"
	@case ":$$PATH:" in *":$(BINDIR):"*) ;; *) echo "Note: $(BINDIR) is not on your PATH";; esac

# Copied only when the checkout's dictionary is newer
$(DATADIR)/$(DB_NAME): $(DB_NAME)
	@install -d $(DATADIR)
	@install -m 644 $(DB_NAME) $@
	@echo "Installed $@"

# Remove what install put in place
uninstall:
	@rm -f $(BINDIR)/$(BINARY_NAME) $(DATADIR)/$(DB_NAME)
	@rmdir $(DATADIR) 2>/dev/null || true
	@echo "Removed $(BINDIR)/$(BINARY_NAME) and $(DATADIR)/$(DB_NAME)"

# Run all tests, including the check of every stored lookup key
test:
	@echo "Running tests..."
	@go test ./...

# Run tests without the full key scan
test-short:
	@go test -short ./...

# Run tests with verbose output
test-verbose:
	@go test -v ./...

# Format code
fmt:
	@go fmt ./...

# Lint code
lint:
	@go vet ./...

# Clean build artifacts
clean:
	@rm -rf $(BUILD_DIR)

# Show help
help:
	@echo "Available targets:"
	@echo "  build          - Build $(BUILD_DIR)/$(BINARY_NAME)"
	@echo "  install        - Install to $(BINDIR), dictionary to $(DATADIR)"
	@echo "  uninstall      - Remove what install put in place"
	@echo "  test           - Run all tests"
	@echo "  test-short     - Run tests, skipping the full dictionary key scan"
	@echo "  test-verbose   - Run tests with verbose output"
	@echo "  fmt            - Format code"
	@echo "  lint           - Run go vet"
	@echo "  clean          - Remove build artifacts"
	@echo "  help           - Show this help message"

.DEFAULT_GOAL := help

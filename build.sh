#!/usr/bin/env bash
# Build the provider and install it into ~/go/bin (used by dev_overrides in examples/basic/dev.tfrc).
set -euo pipefail
cd "$(dirname "$0")"

VERSION="${VERSION:-0.1.0}"

go mod tidy
go vet ./...
go test ./...
go build -ldflags "-X main.version=${VERSION}" -o "$(go env GOPATH)/bin/terraform-provider-databricks-metricview" .

echo "Installed $(go env GOPATH)/bin/terraform-provider-databricks-metricview"

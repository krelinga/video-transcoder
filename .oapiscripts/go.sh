#!/bin/bash
set -e

# Script to generate Go client, server, and types from OpenAPI specifications
# This script should be run from the repository root

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

echo "==> Creating directory structure..."
mkdir -p vtrest

echo "==> Generating Go API..."
cd vtrest
oapi-codegen -config ../.oapiconfig/go.yml ../openapi.yml
echo "Generated API code:"
wc -l api.gen.go

echo "==> Updating go.mod..."
go mod tidy
cd ../

echo "==> Done! Generated code is in vtrest/"
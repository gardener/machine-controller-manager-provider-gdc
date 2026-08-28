# machine-controller-manager-provider-gdc

## Introduction

This repository implements the out-of-tree machine-controller-manager provider for Google Distributed Cloud air-gapped.
It enables the Gardener Machine Controller Manager to manage VMs (machines) in GDC.

## Components

This repository contains the following components, located in `cmd/`:

*   **Machine Controller (`machine-controller`)**: The GDC Machine Controller is responsible for provisioning and managing the lifecycle of VMs in GDC.

## Development & Build Workflow

This repository uses a standard Go toolchain and `Makefile` matching upstream Gardener build standards.

### Prerequisites
- Go 1.25 or higher
- Docker (for building container images)

### Common Make Targets

| Target | Description |
| :--- | :--- |
| `make format` | Formats all Go source files with `goimports` |
| `make check` | Runs code linters (`golangci-lint`, `go vet`) |
| `make test` | Runs unit test suite across all packages |
| `make unittests` | Alias for test |
| `make build-local` | Builds binaries locally in current environment |
| `make release` | Builds cross-compiled release binaries |
| `make docker-images` | Builds multi-stage Docker images for machine controller |
| `make clean` | Cleans built binaries and test tools cache |

### Managing Dependencies

- **Add a new dependency**:
  ```bash
  go get <package-name>
  go mod tidy
  ```
- **Verify and download dependencies**:
  ```bash
  go mod download
  go mod verify
  ```
- **Format and check code before submitting**:
  ```bash
  make format
  make check
  make unittests
  ```

## Contributing

Contributions are welcome! Please ensure that your changes pass all linters and tests before submitting a Pull Request:

```bash
make format
make check
make test
```

## License

`machine-controller-manager-provider-gdc` is licensed under the Apache 2.0 license.

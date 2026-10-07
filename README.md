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
| `make test-integration` | Runs presubmit integration tests against GDC |
| `make build-local` | Builds binaries locally in current environment |
| `make release` | Builds cross-compiled release binaries |
| `make docker-images` | Builds multi-stage Docker images for machine controller |
| `make clean` | Cleans built binaries and test tools cache |

### Presubmit Integration Tests

The `Presubmit Integration Test` GitHub Actions workflow (`.github/workflows/integration-test.yaml`) runs automatically on same-repository Pull Requests targeting `main` authored by repository maintainers (`OWNERS_ALIASES` / `CODEOWNERS`).

For automated bot PRs (such as Renovate), forked PRs, or manual re-runs, an authorized maintainer can trigger the presubmit integration test using any of the following methods:

1. **PR Comment (Recommended):**
   Leave a comment on the Pull Request:
   ```text
   /test-integration
   ```
2. **GitHub Actions UI (`workflow_dispatch`):**
   Navigate to **Actions → Presubmit Integration Test → Run workflow**, keep **Use workflow from: `Branch: main`**, enter the target Pull Request number in **`pr_number`**, and click **Run workflow**.
3. **GitHub CLI (`gh`):**
   ```bash
   gh workflow run integration-test.yaml \
     --repo gardener/machine-controller-manager-provider-gdc \
     --ref main \
     -f pr_number=<PR_NUMBER>
   ```

When triggered manually on a PR, the workflow merges `origin/main` into the PR branch under test and posts the `MCM Integration Test (GDC Staging)` check-run result directly onto the PR's head commit.

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

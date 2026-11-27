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
| `make test-ci-scripts` | Runs the tests of the CI helper scripts (`scripts/ci-common.sh`) |
| `make build-local` | Builds binaries locally in current environment |
| `make release` | Builds cross-compiled release binaries |
| `make docker-images` | Builds multi-stage Docker images for machine controller |
| `make clean` | Cleans built binaries and test tools cache |

### Presubmit Integration Tests

The `Presubmit Integration Test` GitHub Actions workflow (`.github/workflows/integration-test.yaml`) runs automatically on same-repository Pull Requests targeting `main` whose author is listed in `OWNERS_ALIASES` / `CODEOWNERS` on `main`. Repository or organization membership alone does not start the test.

For Pull Requests from other authors, unlisted automated bots, forked PRs, or manual re-runs, a maintainer listed in `OWNERS_ALIASES` / `CODEOWNERS` can trigger the presubmit integration test using any of the following methods.

On same-repository Pull Requests authored by a login listed in `OWNERS_ALIASES` / `CODEOWNERS` on `main` (which already run automatically on every push, including Renovate PRs), `<HEAD_SHA>` is optional and `/test-integration` works as before. On forked Pull Requests and Pull Requests from authors outside `OWNERS_ALIASES` / `CODEOWNERS`, a manual trigger must name the reviewed head commit by its full 40-character SHA (`<HEAD_SHA>` below; abbreviated SHAs are rejected). When the automatic check skips such a Pull Request, it prints the ready-to-copy command with the current commit SHA (also available on the **Commits** tab or via `gh pr view <PR_NUMBER> --json headRefOid --jq .headRefOid`). If the head of the Pull Request changes before the job starts, the job stops without checking out the Pull Request: review the new head and trigger the test again with its SHA.

1. **PR Comment (Recommended):**
   Leave a comment on the Pull Request (omit `<HEAD_SHA>` on same-repository `OWNERS` PRs):
   ```text
   /test-integration [<HEAD_SHA>]
   ```
2. **GitHub Actions UI (`workflow_dispatch`):**
   Navigate to **Actions → Presubmit Integration Test → Run workflow**, keep **Use workflow from: `Branch: main`**, enter the target Pull Request number in **`pr_number`** and (for fork or non-`OWNERS` PRs) the reviewed head commit SHA in **`head_sha`**, and click **Run workflow**.
3. **GitHub CLI (`gh`):**
   ```bash
   gh workflow run integration-test.yaml \
     --repo gardener/machine-controller-manager-provider-gdc \
     --ref main \
     -f pr_number=<PR_NUMBER> \
     -f head_sha=<HEAD_SHA>
   ```

When triggered manually on a PR, the workflow checks out the named commit, merges `origin/main` into it and posts the `MCM Integration Test (GDC Staging)` check-run result directly onto that commit.

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

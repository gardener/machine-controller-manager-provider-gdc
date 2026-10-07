#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Google LLC
#
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${REPO_ROOT}"

# shellcheck source=scripts/ci-common.sh
source "${REPO_ROOT}/scripts/ci-common.sh"

check_ci_preconditions

COMMIT_HASH="${COMMIT_HASH:-$(git rev-parse --short HEAD)}"
REGION="${GDC_REGION:-us-west6}"
ZONE="${GDC_ZONE:-us-west6-a}"
GDCH_PROJECT="${GDC_PROJECT:-sapbtp}"
VUC="${GDC_VUC:-gardener-github-ci}"
ORG="${GDC_ORG:-gdc1}"
LAB_URL="${GDC_LAB_URL:-staging.gpcdemolabs.com}"
GDCLOUD_VERSION="${GDCLOUD_VERSION:-1.16.2}"
IMAGE_REPOSITORY="${IMAGE_REPOSITORY:-ghcr.io/gardener/machine-controller-manager-provider-gdc/machine-controller-manager-provider-gdch}"
IMAGE_TAG="${IMAGE_TAG:-pr-${COMMIT_HASH}}"
IMAGE_WITHTAG="${IMAGE_REPOSITORY}:${IMAGE_TAG}"
MCM_IMAGE="${MCM_IMAGE:-europe-docker.pkg.dev/gardener-project/releases/gardener/machine-controller-manager:v0.58.0}"
MACHINE_IMAGE="${MACHINE_IMAGE:-gardenlinux-gdch}"
MACHINE_TYPE="${MACHINE_TYPE:-n3-standard-2-gdc}"

WORK_DIR="$(mktemp -d)"
CA_FILE="${WORK_DIR}/cafile"
SA_FILE="${MCM_SERVICE_ACCOUNT_FILE:-${WORK_DIR}/mcm_service_account.json}"
MGMT_URL="https://management-kube.apiserver.${ORG}.${ZONE}.${LAB_URL}"
IMAGE_PUSHED=false

cleanup() {
  local exit_code=$?
  if [[ "${IMAGE_PUSHED}" == "true" ]]; then
    delete_ghcr_image_tag "${IMAGE_REPOSITORY}" "${IMAGE_TAG}"
  fi
  rm -rf "${WORK_DIR}"
  complete_pr_check_run "${exit_code}"
}
trap cleanup EXIT INT TERM

setup_gdc_credentials "${SA_FILE}" "${CA_FILE}" "${ORG}" "${ZONE}" "${LAB_URL}" "${GDC_MCM_SERVICE_ACCOUNT_KEY:-}"
unset GDC_MCM_SERVICE_ACCOUNT_KEY
install_gdcloud_cli "${SA_FILE}" "${CA_FILE}" "${MGMT_URL}" "${GDCLOUD_VERSION}" "./integration/cmd/token-helper"

echo "Building and pushing provider image ${IMAGE_WITHTAG}..."
make docker-images IMAGE_REPOSITORY="${IMAGE_REPOSITORY}" IMAGE_TAG="${IMAGE_TAG}"
docker push "${IMAGE_WITHTAG}"
IMAGE_PUSHED=true

echo "Running MCM presubmit integration test..."
go test -v -timeout=20m ./integration/presubmit/machine-controller-manager \
  -args \
  --commit_hash="${COMMIT_HASH}" \
  --zone="${ZONE}" \
  --region="${REGION}" \
  --project="${GDCH_PROJECT}" \
  --vuc="${VUC}" \
  --org="${ORG}" \
  --lab_url="${LAB_URL}" \
  --cafile="${CA_FILE}" \
  --service_account="${SA_FILE}" \
  --gdc_mcm_image_tag="${IMAGE_WITHTAG}" \
  --mcm_image_tag="${MCM_IMAGE}" \
  --machine_image="${MACHINE_IMAGE}" \
  --machine_type="${MACHINE_TYPE}"

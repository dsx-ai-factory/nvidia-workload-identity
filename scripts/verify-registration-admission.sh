#!/bin/bash
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

fail() {
  log_error "$1"
  exit 1
}

if [[ $# -ne 1 ]]; then
  fail "usage: $0 <kube-context>"
fi

context=$1
writer=system:serviceaccount:dsx-identity:e2e-registration-writer
manifest=$(<"$(dirname "${BASH_SOURCE[0]}")/admission/registration-probe.yaml.tmpl")

expect_denial() {
  local operation=$1
  local output

  if output=$(kubectl --context="${context}" --as="${writer}" "${operation}" --dry-run=server -f - <<<"${manifest}" 2>&1); then
    fail "unauthorized registration ${operation} unexpectedly succeeded"
  fi
  if [[ "${output}" != *"Only configured deployment subjects"* ]]; then
    fail "registration ${operation} did not reach the DSX admission policy: ${output}"
  fi
}

expect_policy_denial() {
  local name=$1
  local expected=$2
  local candidate=$3
  local output

  if output=$(kubectl --context="${context}" apply --dry-run=server -f - <<<"${candidate}" 2>&1); then
    fail "${name} unexpectedly passed admission"
  fi
  if [[ "${output}" != *"${expected}"* ]]; then
    fail "${name} did not reach the expected DSX validation rule: ${output}"
  fi
}

expect_denial create

if [[ "${context}" == "kind-k2" ]]; then
  controller=system:serviceaccount:dsx-identity:dsx-identity-dsx-identity-controller
  controller_entry=$(cat <<'EOF'
apiVersion: spire.spiffe.io/v1alpha1
kind: ClusterStaticEntry
metadata:
  name: e2e-controller-admission-probe
  labels:
    app.kubernetes.io/managed-by: dsx-identity-controller
    identity.dsx.nvidia.com/source-uid: ""
  annotations:
    identity.dsx.nvidia.com/source-group: provisioning.dpu.nvidia.com
    identity.dsx.nvidia.com/source-kind: DPU
    identity.dsx.nvidia.com/source-namespace: dsx-identity-admission-e2e
    identity.dsx.nvidia.com/source-name: e2e-controller-admission-probe
spec:
  spiffeID: spiffe://k2.sjc-1.nvidia.com/tenant/operator-1/service/dsx/dpu/e2e-controller-admission-probe
  parentID: spiffe://k2.sjc-1.nvidia.com/spire/agent/dsx_dpu/e2e-controller-admission-probe
  selectors:
    - unix:uid:1000
    - unix:path:/usr/local/bin/dsx-token-fetcher
  hint: dsx-dpu
  className: dsx-identity-dsx-identity
  x509SVIDTTL: 0s
  jwtSVIDTTL: 0s
EOF
)
  if ! output=$(kubectl --context="${context}" --as="${controller}" create \
      --dry-run=server -f - -o name <<<"${controller_entry}" 2>&1); then
    fail "valid controller registration was rejected: ${output}"
  fi
fi

if kubectl --context="${context}" get clusterpolicy inject-dsx-token-fetcher >/dev/null 2>&1; then
  invalid_role=$(cat <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: invalid-role
  namespace: dsx-identity-admission-e2e
  labels:
    spiffe.nvidia.com/service: dsx
    spiffe.nvidia.com/role: INVALID
spec:
  containers:
    - name: application
      image: registry.k8s.io/pause:3.10.1
EOF
)
  expect_policy_denial "invalid operator Pod role" \
    "The optional operator Pod role" \
    "${invalid_role}"

fi

if kubectl --context="${context}" get clusterpolicy inject-dsx-imds-sidecars >/dev/null 2>&1; then
  vm_pod=$(cat <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: valid-vm-injection
  namespace: dsx-identity-admission-e2e
  labels:
    kubevirt.io: virt-launcher
  annotations:
    spiffe.nvidia.com/inject-dsx-imds: "true"
spec:
  initContainers:
    - name: application-init
      image: registry.k8s.io/pause:3.10.1
  containers:
    - name: compute
      image: registry.k8s.io/pause:3.10.1
EOF
)
  if ! output=$(kubectl --context="${context}" \
      --as=system:serviceaccount:kubevirt:kubevirt-controller create \
      --dry-run=server -f - -o json <<<"${vm_pod}" 2>&1); then
    fail "valid VM identity injection was rejected: ${output}"
  fi
  if ! jq -e '
      [.spec.containers[].name] | index("metadata-service") != null and
      index("dsx-imds-network-setup") != null
    ' <<<"${output}" >/dev/null; then
    fail "valid VM Pod did not receive both identity sidecars"
  fi
fi

kubectl --context="${context}" apply --server-side -f - <<<"${manifest}" >/dev/null
cleanup() {
  kubectl --context="${context}" delete --ignore-not-found -f - <<<"${manifest}" >/dev/null
}
trap cleanup EXIT

expect_denial delete
cleanup
trap - EXIT
log_success "registration and identity injection admission checks passed"

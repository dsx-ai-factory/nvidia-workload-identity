#!/bin/bash

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Verify k2 DPU, host, and VM identity delivery.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
CLUSTER_NAME="k2"
CONTEXT="kind-${CLUSTER_NAME}"
# bridge uses KubeVirt DHCP. l2bridge/managedTap uses the cloud-init hook.
# Both must return their own signed VM identity and pass the same OpenBao checks.
VM_NAMES=(e2e-debian-vm e2e-debian-vm-l2bridge)
if [[ -n "${VM_NAME:-}" ]]; then
    VM_NAMES=("${VM_NAME}")
fi
VM_NAME="${VM_NAMES[0]}"
VM_TENANT="${VM_TENANT:-tenant1}"
NAMESPACE="tenant-${VM_TENANT}"
TEST_AUDIENCE="spiffe://ds9.dsx.nvid.id/openbao"
HOST_AUDIENCE="spiffe://k2.sjc-1.nvidia.com/spire/server"
EXPECTED_ISSUER="https://ds9.dsx.nvid.id"
EXPECTED_SPIFFE_ID="spiffe://tenant-2.ds9.dsx.nvid.id/vm/${VM_NAME}"
IMDS_IP="169.254.169.254"
GATEWAY_HOST="gateway.sjc-1.nvidia.com"
OPENBAO_URL="http://${GATEWAY_HOST}:8200"
EXTERNAL_CLIENT_NAME="e2e-external-client"
EXTERNAL_CLIENT_CONTAINER="external-client"
OPERATOR_HOST_ID="k2-worker"
VM_TOKEN=""
K2_IDENTITY_KEYS_KID=""
GATEWAY_ADDRESS=""
E2E_PROBE_IMAGE=""

source "${PROJECT_ROOT}/local-e2e-tests/common.sh"

external_curl() {
    docker run --rm --network kind \
        --add-host "${GATEWAY_HOST}:${GATEWAY_ADDRESS}" \
        --entrypoint curl "${E2E_PROBE_IMAGE}" "$@"
}

initialize_external_test_process() {
    GATEWAY_ADDRESS=$(get_gateway_address kind-k1 envoy-gateway-system k1-gateway)
    kubectl wait --for=condition=Ready "pod/${EXTERNAL_CLIENT_NAME}" \
        --namespace "${NAMESPACE}" --context "${CONTEXT}" --timeout=120s >/dev/null
    E2E_PROBE_IMAGE=$(kubectl get pod e2e-spire-client \
        --namespace dsx-identity-spire-e2e --context "${CONTEXT}" \
        -o jsonpath='{.spec.containers[?(@.name=="identity-probe")].image}')
    docker image inspect "${E2E_PROBE_IMAGE}" >/dev/null
}

verify_dpu_controller_registration() {
    log_test "Checking the Identity Controller DPU registration..."

    local spiffe_id parent_id entries="" deadline=$((SECONDS + 60))
    spiffe_id="spiffe://k2.sjc-1.nvidia.com/tenant/operator-1/service/dsx/bm/e2e-machine-001"
    parent_id="spiffe://k2.sjc-1.nvidia.com/spire/agent/dpu_hw/dpf-serial-001"
    while ((SECONDS < deadline)); do
        entries=$(kubectl exec statefulset/dsx-identity-server -n dsx-identity \
            --context "${CONTEXT}" -c spire-server -- \
            /opt/spire/bin/spire-server entry show -spiffeID "${spiffe_id}" \
            -output json 2>/dev/null) || continue
        jq -e --arg parent "${parent_id}" '
            .entries | length == 1 and
            ("spiffe://" + .[0].parent_id.trust_domain + .[0].parent_id.path) == $parent and
            .[0].hint == "dsx-imds" and
            ([.[0].selectors[] | .type + ":" + .value] | sort) == [
                "k8s:container-name:e2e-metadata-service",
                "k8s:ns:dsx-identity",
                "k8s:sa:e2e-dsx-imds"
            ]
        ' <<<"${entries}" >/dev/null && {
            log_success "Identity Controller created the DPU IMDS entry under its dpu_hw agent"
            return 0
        }
    done

    log_error "Identity Controller did not create the expected DPU SPIRE entry"
    [[ -z "${entries}" ]] || jq . <<<"${entries}" >&2
    return 1
}

# Check the exchanged JWT contract.
check_token_contract() {
    local token="$1"
    local expected_subject="${2:-${EXPECTED_SPIFFE_ID}}"
    local expected_audience="${3:-${TEST_AUDIENCE}}"
    local encoded padding header payload now

    encoded=$(cut -d. -f1 <<<"${token}")
    padding=$(( (4 - ${#encoded} % 4) % 4 ))
    encoded="${encoded}$(printf '%*s' "${padding}" '' | tr ' ' '=')"
    if ! header=$(tr '_-' '/+' <<<"${encoded}" | base64 -d 2>/dev/null); then
        log_error "Could not decode JWT header"
        return 1
    fi
    if ! jq -e --arg kid "${K2_IDENTITY_KEYS_KID}" '
        .alg == "ES256" and .kid == $kid
    ' <<<"${header}" >/dev/null; then
        log_error "DSX token was not signed by the k2 DSX Identity Keys active key"
        jq . <<<"${header}" 2>/dev/null || true
        return 1
    fi

    encoded=$(cut -d. -f2 <<<"${token}")
    padding=$(( (4 - ${#encoded} % 4) % 4 ))
    encoded="${encoded}$(printf '%*s' "${padding}" '' | tr ' ' '=')"
    if ! payload=$(tr '_-' '/+' <<<"${encoded}" | base64 -d 2>/dev/null); then
        log_error "Could not decode JWT payload"
        return 1
    fi

    now=$(date +%s)
    if ! jq -e --arg issuer "${EXPECTED_ISSUER}" \
        --arg audience "${expected_audience}" \
        --arg subject "${expected_subject}" \
        --argjson now "${now}" '
          .iss == $issuer and
          .aud == [$audience] and
          .sub == $subject and
          (.iat | type == "number") and
          (.exp | type == "number") and
          (.exp > .iat) and
          (.exp > $now) and
          ((.exp - .iat) <= 300)
        ' <<<"${payload}" >/dev/null; then
        log_error "JWT issuer, audience, subject, or expiry is invalid"
        jq . <<<"${payload}" 2>/dev/null || true
        return 1
    fi

    log_success "JWT has the expected identity and a current TTL of at most 300s"
}

# Parse the JWT response.
parse_jwt_response() {
    local response="$1"
    local token

    if ! jq -e --arg subject "${EXPECTED_SPIFFE_ID}" '
        .spiffe_id == $subject and
        (.expires_in | type == "number" and . > 0 and . <= 300) and
        (.token | type == "string" and length > 0)
      ' <<<"${response}" >/dev/null; then
        log_error "IMDS response does not contain the expected VM identity, lifetime, and token"
        jq . <<<"${response}" 2>/dev/null || true
        return 1
    fi

    token=$(jq -r '.token' <<<"${response}")
    check_token_contract "${token}" || return 1
    VM_TOKEN="${token}"

    log_success "Successfully retrieved JWT response"
}

openbao_curl() {
    external_curl --connect-timeout 5 --max-time 15 "$@"
}

ssh_vm() {
    local vm_ip="$1"
    local command="$2"
    local deadline=$((SECONDS + 180)) remaining result
    # Cloud-init can restart sshd during authentication. Retry SSH failures,
    # but preserve remote command failures and the overall startup deadline.
    while ((SECONDS < deadline)); do
        remaining=$((deadline - SECONDS))
        if kubectl exec "${EXTERNAL_CLIENT_NAME}" -c "${EXTERNAL_CLIENT_CONTAINER}" -n "${NAMESPACE}" \
            --context "${CONTEXT}" -- timeout "${remaining}s" ssh \
            -i /run/dsx-identity-ssh/id_ed25519 \
            -o BatchMode=yes \
            -o StrictHostKeyChecking=no \
            -o UserKnownHostsFile=/dev/null \
            -o GlobalKnownHostsFile=/dev/null \
            -o LogLevel=ERROR \
            -o ConnectionAttempts=180 \
            -o ConnectTimeout="${remaining}" \
            "debian@${vm_ip}" "${command}"; then
            return 0
        else
            result=$?
        fi
        [[ "${result}" == 255 ]] || return "${result}"
    done
    return 124
}

verify_openbao_access() {
    log_test "Authenticating the real VM IMDS token to OpenBao..."

    local request response client_token path expected status
    request=$(jq -cn --arg role basic-spiffe --arg jwt "${VM_TOKEN}" '{role: $role, jwt: $jwt}')
    response=$(openbao_curl -fsS --retry 30 --retry-all-errors --retry-connrefused \
        --retry-delay 1 --retry-max-time 30 \
        -X POST -H 'Content-Type: application/json' \
        -d "${request}" "${OPENBAO_URL}/v1/auth/jwt/cel/login") || return 1
    if ! jq -e '
        ((.auth.policies - ["default"]) | sort) ==
        (["spiffe-authenticated", "region-ds9", "tenant-tenant-2", "service-dsx", "resource-type-vm"] | sort)
    ' <<<"${response}" >/dev/null; then
        log_error "OpenBao did not assign the exact VM policies"
        jq . <<<"${response}" >&2
        return 1
    fi
    client_token=$(jq -er '.auth.client_token' <<<"${response}") || return 1

    while read -r path expected; do
        response=$(openbao_curl -fsS -H "X-Vault-Token: ${client_token}" \
            "${OPENBAO_URL}/v1/secret/data/${path}") || return 1
        jq -e --arg expected "${expected}" '.data.data.value == $expected' \
            <<<"${response}" >/dev/null || return 1
    done <<'EOF'
region/ds9/e2e region-ds9
tenant/tenant-2/e2e tenant-2
service/dsx/e2e service-dsx
resource-type/vm/e2e resource-vm
EOF

    for path in resource-type/bm/e2e resource-type/dpu/e2e forbidden/admin/e2e; do
        status=$(openbao_curl -sS -o /dev/null -w '%{http_code}' \
            -H "X-Vault-Token: ${client_token}" "${OPENBAO_URL}/v1/secret/data/${path}") || return 1
        if [[ "${status}" != "403" ]]; then
            log_error "VM token received HTTP ${status} for forbidden secret ${path}"
            return 1
        fi
    done

    log_success "OpenBao verified the VM signature and enforced its exact policies"
}

verify_imds_identity() {
    local pod="$1" expected_subject="$2" endpoint="${3:-http://127.0.0.1:18080}" response="" token deadline=$((SECONDS + 180))

    while ((SECONDS < deadline)); do
        response=$(kubectl exec "${pod}" -n dsx-identity --context "${CONTEXT}" \
            -c imds-probe -- curl -fsS --max-time 10 -G \
            -H 'Metadata: true' -H 'Accept: application/json' \
            "${endpoint}/latest/meta-data/identity" \
            --data-urlencode "aud=${HOST_AUDIENCE}" 2>/dev/null) || continue
        jq -e --arg subject "${expected_subject}" '.spiffe_id == $subject' \
            <<<"${response}" >/dev/null && break
        response=""
    done
    if [[ -z "${response}" ]]; then
        log_error "${pod} did not return ${expected_subject}"
        return 1
    fi
    jq -e --arg subject "${expected_subject}" '.spiffe_id == $subject' \
        <<<"${response}" >/dev/null || return 1
    token=$(jq -er '.token' <<<"${response}") || return 1
    check_token_contract "${token}" "${expected_subject}" "${HOST_AUDIENCE}" || return 1
}

verify_operator_host_imds() {
    log_test "Requesting the operator worker-host identity from bootstrap IMDS..."

    verify_imds_identity e2e-nico-mds \
        "spiffe://operator-1.ds9.dsx.nvid.id/bm/${OPERATOR_HOST_ID}" || return 1

    log_success "Operator worker-host IMDS returned the configured tenant and node ID"
}

verify_dpu_imds() {
    log_test "Requesting the DPU identity from metadata-service..."

    verify_imds_identity e2e-dpu-imds-probe \
        spiffe://operator-1.ds9.dsx.nvid.id/dpu/e2e-dpu-imds \
        http://dsx-imds || return 1

    log_success "DPU IMDS exchanged its SPIFFE identity"
}

verify_metadata_access() {
    log_test "Requesting DSX IMDS v1 from guest ${VM_NAME} through the tenant network..."

    local vm_ip response command
    local ssh_started
    # VMI and SSH readiness can precede guest IMDS readiness, so retry the IMDS request itself.
    command="curl -fsS --max-time 10 "
    command+="--retry 180 --retry-all-errors --retry-connrefused --retry-delay 1 --retry-max-time 180 "
    command+="-G -H 'Metadata: true' -H 'Accept: application/json' "
    command+="'http://${IMDS_IP}/latest/meta-data/identity' --data-urlencode 'aud=${TEST_AUDIENCE}'"
    run_timed "VM VMI readiness" kubectl wait \
        --for=condition=Ready \
        "vmi/${VM_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}" \
        --timeout=300s || return 1
    # managedTap guests need not report their address through the guest agent.
    # Use the CNI allocation to reach the real guest, then assert its IMDS response.
    vm_ip=$(kubectl get pods -l "kubevirt.io/domain=${VM_NAME}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" -o json | jq -er '
          [.items[] |
           .metadata.annotations["k8s.ovn.org/pod-networks"] | fromjson |
           .[] | select(.role == "primary") | .ip_addresses[] |
           split("/")[0] | select(test("^[0-9]+(\\.[0-9]+){3}$"))][0]
        ') || return 1
    ssh_started=${SECONDS}
    if [[ -n "${vm_ip}" ]] && response=$(ssh_vm "${vm_ip}" "${command}"); then
        log_timing "VM SSH and metadata" "${ssh_started}"
        response="${response//$'\r'/}"
        parse_jwt_response "${response}" || return 1
        run_timed "VM OpenBao authentication" verify_openbao_access || return 1
        return 0
    fi
    log_timing "VM SSH and metadata" "${ssh_started}"

    log_error "External client could not retrieve a DSX IMDS response from the VM"
    if [[ -n "${vm_ip}" ]]; then
        ssh_vm "${vm_ip}" "${command}" || true
    else
        log_error "VM has no reported primary-network IPv4 address"
    fi
    return 1
}

verify_identity_keys_proxy_restart() {
    log_test "Checking VM identity after an abrupt identity keys proxy stop..."

    local node pod container_id pid current_id deadline=$((SECONDS + 120))
    node=$(kubectl get pods -l "kubevirt.io/domain=${VM_NAME}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" -o json | jq -er '.items[0].spec.nodeName') || return 1
    pod=$(kubectl get pods -n dsx-identity --context "${CONTEXT}" \
        -l app.kubernetes.io/name=dsx-identity-keys-proxy-csi \
        --field-selector "spec.nodeName=${node}" -o json | jq -er '.items[0].metadata.name') || return 1
    container_id=$(kubectl get pod "${pod}" -n dsx-identity --context "${CONTEXT}" \
        -o json | jq -er '.status.containerStatuses[] | select(.name == "identity-keys-proxy") | .containerID') || return 1
    pid=$(docker exec "${node}" crictl inspect "${container_id#containerd://}" | \
        jq -er '.info.pid | select(. > 0)') || return 1
    # Kill from the parent PID namespace so socat cannot unlink its hostPath socket.
    docker exec "${node}" kill -KILL "${pid}" || return 1

    # Wait for a replacement before checking the existing VM's mounted socket.
    while ((SECONDS < deadline)); do
        current_id=$(kubectl get pod "${pod}" -n dsx-identity --context "${CONTEXT}" \
            -o json | jq -er '.status.containerStatuses[] |
                select(.name == "identity-keys-proxy" and .state.running != null) | .containerID') || continue
        if [[ "${current_id}" != "${container_id}" ]]; then
            verify_metadata_access
            return $?
        fi
    done
    log_error "Identity keys proxy did not restart within 120s"
    return 1
}

main() {
    local script_started=${SECONDS} setup_started=${SECONDS}
    local failed=0

    log_info "Verifying k2 identity registration and delivery..."
    echo

    initialize_external_test_process

    K2_IDENTITY_KEYS_KID=$(kubectl get secret dsx-identity-keys-active-kid -n dsx-identity \
        --context "${CONTEXT}" -o json | jq -er '.data["active-kid.json"]' | base64 -d | jq -er '.active_kid')
    log_timing "setup" "${setup_started}"

    run_timed "DPU controller registration" verify_dpu_controller_registration || failed=$((failed + 1))
    run_timed "operator host IMDS" verify_operator_host_imds || failed=$((failed + 1))
    run_timed "DPU IMDS" verify_dpu_imds || failed=$((failed + 1))
    for VM_NAME in "${VM_NAMES[@]}"; do
        EXPECTED_SPIFFE_ID="spiffe://tenant-2.ds9.dsx.nvid.id/vm/${VM_NAME}"
        run_timed "${VM_NAME} identity and OpenBao" verify_metadata_access || failed=$((failed + 1))
        run_timed "${VM_NAME} proxy restart recovery" verify_identity_keys_proxy_restart || failed=$((failed + 1))
    done
    echo
    if [[ $failed -eq 0 ]]; then
        log_timing "k2 identity verification total" "${script_started}"
        log_success "All k2 identity checks passed"
    else
        log_error "${failed} test(s) failed"
        exit 1
    fi
}

trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"

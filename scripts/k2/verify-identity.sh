#!/bin/bash
# Verify k2 Identity Controller registrations and VM identity delivery.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
CLUSTER_NAME="k2"
CONTEXT="kind-${CLUSTER_NAME}"
VM_NAME="${VM_NAME:-e2e-ubuntu-vm}"
VM_TENANT="${VM_TENANT:-tenant1}"
NAMESPACE="tenant-${VM_TENANT}"
TEST_AUDIENCE="spiffe://ds9.dsx.nvid.id/openbao"
HOST_AUDIENCE="spiffe://k2.sjc-1.nvidia.com/spire/server"
EXPECTED_ISSUER="spiffe://ds9.dsx.nvid.id"
IDENTITY_ROLE="database"
EXPECTED_SPIFFE_ID="spiffe://tenant-2.ds9.dsx.nvid.id/vm/${VM_NAME}/role/${IDENTITY_ROLE}"
IMDS_IP="169.254.169.254"
GATEWAY_HOST="gateway.sjc-1.nvidia.com"
OPENBAO_URL="http://${GATEWAY_HOST}:8200"
EXTERNAL_CLIENT_NAME="e2e-external-client"
EXTERNAL_CLIENT_CONTAINER="external-client"
OPERATOR_HOST_ID="k2-worker"
DPU_TOKEN_FETCHER_POD="e2e-dpu-token-fetcher"
VM_TOKEN=""
K2_EXCHANGE_KID=""
GATEWAY_ADDRESS=""
E2E_PROBE_IMAGE=""

source "${PROJECT_ROOT}/scripts/common.sh"

external_curl() {
    docker run --rm --network kind \
        --add-host "${GATEWAY_HOST}:${GATEWAY_ADDRESS}" \
        --entrypoint curl "${E2E_PROBE_IMAGE}" "$@"
}

initialize_external_test_process() {
    GATEWAY_ADDRESS=$(get_gateway_address kind-k1 envoy-gateway-system k1-gateway)
    E2E_PROBE_IMAGE=$(kubectl get pod e2e-spire-client \
        --namespace dsx-identity-spire-e2e --context "${CONTEXT}" \
        -o jsonpath='{.spec.containers[?(@.name=="identity-probe")].image}')
    docker image inspect "${E2E_PROBE_IMAGE}" >/dev/null
}

spire_server_entry() {
    kubectl exec statefulset/dsx-identity-server -n dsx-identity \
        --context "${CONTEXT}" -c spire-server -- \
        /opt/spire/bin/spire-server entry "$@"
}

wait_for_spire_entry_count() {
    local spiffe_id="$1"
    local expected="$2"
    local result_name="${3:-}"
    local current_entries count deadline=$((SECONDS + 60))
    while ((SECONDS < deadline)); do
        current_entries=$(spire_server_entry show -spiffeID "${spiffe_id}" -output json) || continue
        count=$(jq -er '.entries | length' <<<"${current_entries}") || continue
        if [[ "${count}" -eq "${expected}" ]]; then
            [[ -z "${result_name}" ]] || printf -v "${result_name}" '%s' "${current_entries}"
            return 0
        fi
    done

    log_error "SPIRE entry ${spiffe_id} did not reach count ${expected}"
    return 1
}

assert_spire_entry() {
    local spiffe_id="$1"
    local parent_id="$2"
    local hint="$3"
    local holder_namespace="$4"
    local holder_service_account="$5"
    local holder_pod_uid="${6:-}"
    local entries

    wait_for_spire_entry_count "${spiffe_id}" 1 entries || return 1
    if ! jq -e --arg parent "${parent_id}" --arg hint "${hint}" \
        --arg namespace "${holder_namespace}" \
        --arg service_account "${holder_service_account}" \
        --arg pod_uid "${holder_pod_uid}" '
        .entries | length == 1 and
        ("spiffe://" + .[0].parent_id.trust_domain + .[0].parent_id.path) == $parent and
        .[0].hint == $hint and
        ([.[0].selectors[] | .type + ":" + .value] | sort) ==
        (["k8s:ns:" + $namespace, "k8s:sa:" + $service_account] +
          (if $pod_uid == "" then [] else ["k8s:pod-uid:" + $pod_uid] end) | sort)
    ' <<<"${entries}" >/dev/null; then
        log_error "SPIRE entry ${spiffe_id} has the wrong parent, hint, or selectors"
        jq . <<<"${entries}" >&2
        return 1
    fi
}

assert_operator_host_boundary_entry() {
    local spiffe_id="$1"
    local entries

    wait_for_spire_entry_count "${spiffe_id}" 1 entries || return 1
    if ! jq -e '
        def parent_id: "spiffe://" + .parent_id.trust_domain + .parent_id.path;
        .entries | length == 1 and
        (.[0] | parent_id | startswith("spiffe://k2.sjc-1.nvidia.com/spire/agent/k8s_psat/k2/")) and
        .[0].hint == "dsx-imds"
    ' <<<"${entries}" >/dev/null; then
        log_error "SPIRE host entry ${spiffe_id} has the wrong local boundary parent or hint"
        jq . <<<"${entries}" >&2
        return 1
    fi
}

assert_dpu_fetcher_entry() {
    local spiffe_id="$1"
    local parent_id="$2"
    local entries

    wait_for_spire_entry_count "${spiffe_id}" 1 entries || return 1
    if ! jq -e --arg parent "${parent_id}" '
        .entries | length == 1 and
        ("spiffe://" + .[0].parent_id.trust_domain + .[0].parent_id.path) == $parent and
        .[0].hint == "dsx-dpu" and
        ([.[0].selectors[] | .type + ":" + .value] | sort) ==
          (["unix:uid:1000", "unix:path:/usr/local/bin/dsx-token-fetcher"] | sort)
    ' <<<"${entries}" >/dev/null; then
        log_error "SPIRE DPU token fetcher entry ${spiffe_id} has the wrong parent, hint, or selectors"
        jq . <<<"${entries}" >&2
        return 1
    fi
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
    if ! jq -e --arg kid "${K2_EXCHANGE_KID}" '
        .alg == "ES256" and .kid == $kid
    ' <<<"${header}" >/dev/null; then
        log_error "DSX token was not signed by the k2 Exchange active key"
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
    # VMI readiness can precede both the SSH listener and its banner. Bound the
    # complete attempt while SSH retries refusals and waits for the banner.
    kubectl exec "${EXTERNAL_CLIENT_NAME}" -c "${EXTERNAL_CLIENT_CONTAINER}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" -- timeout 180s ssh \
        -i /run/dsx-identity-ssh/id_ed25519 \
        -o BatchMode=yes \
        -o StrictHostKeyChecking=no \
        -o UserKnownHostsFile=/dev/null \
        -o GlobalKnownHostsFile=/dev/null \
        -o ConnectionAttempts=180 \
        -o ConnectTimeout=180 \
        "ubuntu@${vm_ip}" "${command}"
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

    for path in resource-type/host/e2e resource-type/dpu/e2e forbidden/admin/e2e; do
        status=$(openbao_curl -sS -o /dev/null -w '%{http_code}' \
            -H "X-Vault-Token: ${client_token}" "${OPENBAO_URL}/v1/secret/data/${path}") || return 1
        if [[ "${status}" != "403" ]]; then
            log_error "VM token received HTTP ${status} for forbidden secret ${path}"
            return 1
        fi
    done

    log_success "OpenBao verified the VM signature and enforced its exact policies"
}

verify_controller_registrations() {
    log_test "Checking the deployed Identity Controller registrations..."

    local dpu_name=e2e-controller-dpu host_name=e2e-controller-host-source
    local dpu_id dpu_parent host_id
    dpu_id="spiffe://k2.sjc-1.nvidia.com/tenant/operator-1/service/dsx/dpu/${dpu_name}"
    dpu_parent="spiffe://k2.sjc-1.nvidia.com/spire/agent/dsx_dpu/${dpu_name}"
    host_id="spiffe://k2.sjc-1.nvidia.com/tenant/tenant-2/service/dsx/host/${host_name}"

    assert_dpu_fetcher_entry "${dpu_id}" "${dpu_parent}" || return 1
    assert_spire_entry "${host_id}" "${dpu_parent}" "dsx-imds" \
        dsx-identity e2e-nico-mds || return 1
    log_success "Identity Controller created the required DPU and host registrations"
}

verify_dpu_token_file() {
    log_test "Exercising DPU token-file delivery through the external-agent fixture..."

    local token="" deadline=$((SECONDS + 180))
    local expected_subject="spiffe://operator-1.ds9.dsx.nvid.id/dpu/e2e-dpu-token-fetcher"
    while ((SECONDS < deadline)); do
        token=$(kubectl exec "${DPU_TOKEN_FETCHER_POD}" -n dsx-identity --context "${CONTEXT}" \
            -c dpu-consumer -- cat /run/secrets/nvidia.com/dsx/token 2>/dev/null) || continue
        [[ -n "${token}" ]] && break
    done
    if [[ -z "${token}" ]]; then
        log_error "DPU fixture did not receive a DSX token file"
        return 1
    fi
    check_token_contract "${token}" "${expected_subject}" "${TEST_AUDIENCE}" || return 1
    kubectl exec "${DPU_TOKEN_FETCHER_POD}" -n dsx-identity --context "${CONTEXT}" \
        -c dpu-consumer -- test ! -e /var/run/secrets/spire.io/spire-agent.sock || {
        log_error "DPU consumer unexpectedly received the Workload API socket"
        return 1
    }

    log_success "The real DPU fetcher wrote a token consumed by its fixture process"
}

verify_operator_host_imds() {
    log_test "Requesting the operator worker-host identity from bootstrap IMDS..."

    local pod expected_subject internal_id response="" token deadline=$((SECONDS + 180))
    pod=$(kubectl get pod e2e-nico-mds -n dsx-identity --context "${CONTEXT}" \
        -o jsonpath='{.metadata.name}') || return 1
    expected_subject="spiffe://operator-1.ds9.dsx.nvid.id/host/${OPERATOR_HOST_ID}"
    internal_id="spiffe://k2.sjc-1.nvidia.com/tenant/operator-1/service/dsx/host/${OPERATOR_HOST_ID}"
    assert_operator_host_boundary_entry "${internal_id}" || return 1
    while ((SECONDS < deadline)); do
        response=$(kubectl exec "${pod}" -n dsx-identity --context "${CONTEXT}" \
            -c host-imds-probe -- curl -fsS --max-time 10 -G \
            -H 'Metadata: true' -H 'Accept: application/json' \
            'http://127.0.0.1:18080/latest/meta-data/identity' \
            --data-urlencode "aud=${HOST_AUDIENCE}" 2>/dev/null) || continue
        jq -e --arg subject "${expected_subject}" '.spiffe_id == $subject' \
            <<<"${response}" >/dev/null && break
        response=""
    done
    if [[ -z "${response}" ]]; then
        log_error "Operator worker-host IMDS did not return the expected identity"
        return 1
    fi
    jq -e --arg subject "${expected_subject}" '.spiffe_id == $subject' \
        <<<"${response}" >/dev/null || return 1
    token=$(jq -er '.token' <<<"${response}") || return 1
    check_token_contract "${token}" "${expected_subject}" "${HOST_AUDIENCE}" || return 1

    log_success "Operator worker-host IMDS returned the configured tenant and node ID"
}

verify_virt_launcher_associations() {
    log_test "Checking the operator Pod and dynamic VM associations..."

    local pod node uid pod_id vm_id pod_entries vm_entries parent_id
    pod=$(kubectl get pods -n "${NAMESPACE}" --context "${CONTEXT}" \
        -l "kubevirt.io=virt-launcher,kubevirt.io/domain=${VM_NAME}" \
        -o jsonpath='{.items[0].metadata.name}') || return 1
    node=$(kubectl get pod "${pod}" -n "${NAMESPACE}" --context "${CONTEXT}" \
        -o jsonpath='{.spec.nodeName}') || return 1
    uid=$(kubectl get pod "${pod}" -n "${NAMESPACE}" --context "${CONTEXT}" \
        -o jsonpath='{.metadata.uid}') || return 1
    pod_id="spiffe://k2.sjc-1.nvidia.com/tenant/operator-1/service/dsx/host/${node}/pod/${uid}"
    vm_id="spiffe://k2.sjc-1.nvidia.com/tenant/tenant-2/service/dsx/vm/${VM_NAME}/role/${IDENTITY_ROLE}"
    parent_id="spiffe://k2.sjc-1.nvidia.com/spire/agent/dsx_imds/tenant/operator-1/host/${node}"
    wait_for_spire_entry_count "${pod_id}" 1 pod_entries || return 1
    if ! jq -e --arg parent "${parent_id}" --arg uid "${uid}" '
        .entries | length == 1 and
        ("spiffe://" + .[0].parent_id.trust_domain + .[0].parent_id.path) == $parent and
        .[0].hint == "dsx-pod" and
        ([.[0].selectors[] | .type + ":" + .value] | index("k8s:pod-uid:" + $uid)) != null
    ' <<<"${pod_entries}" >/dev/null; then
        log_error "virt-launcher Pod entry has the wrong operator-host parent, hint, or Pod UID selector"
        jq . <<<"${pod_entries}" >&2
        return 1
    fi
    wait_for_spire_entry_count "${vm_id}" 1 vm_entries || return 1
    if ! jq -e --arg parent "${parent_id}" '
        .entries | length == 1 and
        ("spiffe://" + .[0].parent_id.trust_domain + .[0].parent_id.path) == $parent and
        .[0].hint == "dsx-imds-vm" and
        ([.[0].selectors[] | .type + ":" + .value] | sort) ==
          (["k8s:ns:tenant-tenant1", "k8s:sa:e2e-ubuntu-vm"] | sort)
    ' <<<"${vm_entries}" >/dev/null; then
        log_error "VM entry has the wrong operator-host parent, hint, or holder selectors"
        jq . <<<"${vm_entries}" >&2
        return 1
    fi

    log_success "The virt-launcher Pod and VM have the exact shared host-agent parent"
}

verify_metadata_access() {
    log_test "Requesting DSX IMDS v1 from an external tenant-network client..."

    local vm_ip response command
    command="curl -fsS --max-time 10 -G -H 'Metadata: true' -H 'Accept: application/json' 'http://${IMDS_IP}/latest/meta-data/identity' --data-urlencode 'aud=${TEST_AUDIENCE}'"
    kubectl wait --for=jsonpath='{.status.interfaces[?(@.name=="default")].ipAddress}' \
        "vmi/${VM_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}" --timeout=300s >/dev/null || return 1
    verify_virt_launcher_associations || return 1
    vm_ip=$(kubectl get vmi "${VM_NAME}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" -o json | jq -r '
          [.status.interfaces[]? |
           select(.name == "default") |
           ((.ipAddresses // [])[]?, .ipAddress?) |
           select(type == "string" and test("^[0-9]+(\\.[0-9]+){3}$"))][0] // empty
        ') || return 1
    if [[ -n "${vm_ip}" ]] && response=$(ssh_vm "${vm_ip}" "${command}" 2>/dev/null); then
        response="${response//$'\r'/}"
        parse_jwt_response "${response}" || return 1
        verify_openbao_access || return 1
        return 0
    fi

    log_error "External client could not retrieve a DSX IMDS response from the VM"
    if [[ -n "${vm_ip}" ]]; then
        ssh_vm "${vm_ip}" "${command}" || true
    else
        log_error "VM has no reported primary-network IPv4 address"
    fi
    return 1
}

main() {
    local script_started=${SECONDS} setup_started=${SECONDS}
    local failed=0

    log_info "Verifying k2 identity registration and delivery..."
    echo

    initialize_external_test_process

    K2_EXCHANGE_KID=$(kubectl get secret spiffe-exchange-signing-keyset -n dsx-identity \
        --context "${CONTEXT}" -o json | jq -er '.data["active-kid"]' | base64 -d)
    log_timing "setup" "${setup_started}"

    run_timed "operator host IMDS" verify_operator_host_imds || failed=$((failed + 1))
    run_timed "DPU token file" verify_dpu_token_file || failed=$((failed + 1))
    run_timed "VM identity and OpenBao" verify_metadata_access || failed=$((failed + 1))
    run_timed "controller registrations" verify_controller_registrations || failed=$((failed + 1))

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

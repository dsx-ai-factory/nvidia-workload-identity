#!/bin/bash
# verify-vm.sh - Verifies VM access through the DSX IMDS v1 metadata-service

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k2"
CONTEXT="kind-${CLUSTER_NAME}"
K1_CONTEXT="kind-k1"
VM_NAME="${VM_NAME:-e2e-ubuntu-vm}"
VM_TENANT="${VM_TENANT:-tenant1}"
NAMESPACE="tenant-${VM_TENANT}"
TEST_AUDIENCE="spiffe://ds9.dsx.nvid.id/openbao"
EXPECTED_ISSUER="spiffe://ds9.dsx.nvid.id"
IDENTITY_ROLE="database"
EXPECTED_SPIFFE_ID="spiffe://nca-0000000000000001.ds9.dsx.nvid.id/vm/${VM_NAME}/role/${IDENTITY_ROLE}"
IMDS_IP="169.254.169.254"
OPENBAO_URL="http://127.0.0.1:18200"
SSH_CLIENT_IMAGE="nicolaka/netshoot:v0.16"
CLIENT_NAME="${VM_NAME}-imds-client"
LAUNCH_LAYER_SOURCE_NAME="e2e-vm-source-${VM_NAME}"
LIFECYCLE_DPU_NAME="e2e-lifecycle-dpu"
LIFECYCLE_HOST_SOURCE_NAME="e2e-lifecycle-host-source"
LIFECYCLE_VM_SOURCE_NAME="e2e-lifecycle-vm-source"
LIFECYCLE_VM_NAME="e2e-lifecycle-vm"
VM_TOKEN=""
K1_EXCHANGE_KID=""
K2_EXCHANGE_KID=""
BAO_PF_PID=""
LIFECYCLE_VM_SOURCE=""
LIFECYCLE_SOURCES_CREATED=false
SPIRE_ENTRY_FOLLOWER_PID=""

source "${PROJECT_ROOT}/scripts/common.sh"

cleanup_spire_entry_follower() {
    local follower_pid="${SPIRE_ENTRY_FOLLOWER_PID:-${SPIRE_ENTRY_EVENTS_PID:-}}"
    SPIRE_ENTRY_FOLLOWER_PID=""
    [[ -n "${follower_pid}" ]] || return 0

    kill "${follower_pid}" 2>/dev/null || true
    wait "${follower_pid}" 2>/dev/null || true
}

cleanup() {
    set +e
    cleanup_spire_entry_follower
    if [[ "${LIFECYCLE_SOURCES_CREATED}" == "true" ]]; then
        kubectl delete launchlayersourcefixture \
            "${LIFECYCLE_HOST_SOURCE_NAME}" "${LIFECYCLE_VM_SOURCE_NAME}" \
            -n "${NAMESPACE}" --context "${CONTEXT}" --ignore-not-found=true --wait=false >/dev/null
        kubectl delete dpus.provisioning.dpu.nvidia.com "${LIFECYCLE_DPU_NAME}" \
            -n "${NAMESPACE}" --context "${CONTEXT}" --ignore-not-found=true --wait=false >/dev/null
    fi
    if [[ -n "${BAO_PF_PID}" ]]; then
        kill "${BAO_PF_PID}" 2>/dev/null || true
        wait "${BAO_PF_PID}" 2>/dev/null || true
    fi
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
    local current_entries count follower_pid output_fd line matched=false started_at

    started_at=$(date -u +'%Y-%m-%dT%H:%M:%S.%NZ')
    current_entries=$(spire_server_entry show -spiffeID "${spiffe_id}" -output json) || return 1
    count=$(jq -er '.entries | length' <<<"${current_entries}") || return 1
    if [[ "${count}" -eq "${expected}" ]]; then
        [[ -z "${result_name}" ]] || printf -v "${result_name}" '%s' "${current_entries}"
        return 0
    fi

    coproc SPIRE_ENTRY_EVENTS {
        exec timeout 60s kubectl logs statefulset/dsx-identity-server -n dsx-identity \
            --context "${CONTEXT}" -c spire-controller-manager \
            --since-time="${started_at}" -f 2>/dev/null
    }
    follower_pid="${SPIRE_ENTRY_EVENTS_PID:-}"
    SPIRE_ENTRY_FOLLOWER_PID="${follower_pid}"
    output_fd="${SPIRE_ENTRY_EVENTS[0]}"
    while IFS= read -r -u "${output_fd}" line; do
        [[ "${line}" == *"${spiffe_id}"* ]] || continue
        if [[ "${expected}" -eq 1 ]]; then
            [[ "${line}" == *"Created entry"* || "${line}" == *"Updated entry"* ]] || continue
        else
            [[ "${line}" == *"Deleted entry"* ]] || continue
        fi

        current_entries=$(spire_server_entry show -spiffeID "${spiffe_id}" -output json) || break
        count=$(jq -er '.entries | length' <<<"${current_entries}") || break
        if [[ "${count}" -eq "${expected}" ]]; then
            matched=true
            break
        fi
    done
    exec {output_fd}<&-
    cleanup_spire_entry_follower
    if [[ "${matched}" == "true" ]]; then
        [[ -z "${result_name}" ]] || printf -v "${result_name}" '%s' "${current_entries}"
        return 0
    fi

    log_error "SPIRE entry ${spiffe_id} has count ${count}, want ${expected}"
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

restore_lifecycle_vm_source() {
    local current
    current=$(kubectl get launchlayersourcefixture "${LIFECYCLE_VM_SOURCE_NAME}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" --ignore-not-found=true -o json) || return 1
    if [[ -z "${current}" ]]; then
        kubectl apply -f - --context "${CONTEXT}" <<<"${LIFECYCLE_VM_SOURCE}" >/dev/null
    elif jq -e '.metadata.deletionTimestamp != null' <<<"${current}" >/dev/null; then
        kubectl wait --for=delete "launchlayersourcefixture/${LIFECYCLE_VM_SOURCE_NAME}" -n "${NAMESPACE}" \
            --context "${CONTEXT}" --timeout=120s >/dev/null || return 1
        kubectl apply -f - --context "${CONTEXT}" <<<"${LIFECYCLE_VM_SOURCE}" >/dev/null
    else
        kubectl patch launchlayersourcefixture "${LIFECYCLE_VM_SOURCE_NAME}" -n "${NAMESPACE}" \
            --context "${CONTEXT}" --type=merge \
            -p "$(jq -c '{spec: .spec}' <<<"${LIFECYCLE_VM_SOURCE}")" >/dev/null
    fi
}

create_lifecycle_sources() {
    local base_source controller_sources
    controller_sources=$(<"${SCRIPT_DIR}/e2e-controller-sources.yaml.tmpl")
    controller_sources="${controller_sources//DPU_NAME_PLACEHOLDER/$LIFECYCLE_DPU_NAME}"
    controller_sources="${controller_sources//HOST_SOURCE_NAME_PLACEHOLDER/$LIFECYCLE_HOST_SOURCE_NAME}"
    controller_sources="${controller_sources//TENANT_NAMESPACE_PLACEHOLDER/$NAMESPACE}"
    LIFECYCLE_SOURCES_CREATED=true
    kubectl apply -f - --context "${CONTEXT}" <<<"${controller_sources}" >/dev/null || return 1

    base_source=$(kubectl get launchlayersourcefixture "${LAUNCH_LAYER_SOURCE_NAME}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" -o json) || return 1
    LIFECYCLE_VM_SOURCE=$(jq -c \
        --arg name "${LIFECYCLE_VM_SOURCE_NAME}" --arg resource "${LIFECYCLE_VM_NAME}" '
        {
          apiVersion,
          kind,
          metadata: {name: $name, namespace: .metadata.namespace},
          spec: (.spec | .resourceID = $resource)
        }
    ' <<<"${base_source}") || return 1
    kubectl apply -f - --context "${CONTEXT}" <<<"${LIFECYCLE_VM_SOURCE}" >/dev/null
}

# Check the exchanged JWT contract.
check_token_contract() {
    local token="$1"
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
        log_error "VM token was not signed by the k2 Exchange active key"
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
        --arg audience "${TEST_AUDIENCE}" \
        --arg subject "${EXPECTED_SPIFFE_ID}" \
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

    log_success "JWT has the expected VM identity and a current TTL of at most 300s"
}

# Parse and display JWT response
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
    echo
    echo "Response:"
    jq . <<<"${response}"
}

start_openbao_port_forward() {
    kubectl port-forward -n openbao svc/openbao 18200:8200 \
        --context "${K1_CONTEXT}" >/dev/null 2>&1 &
    BAO_PF_PID=$!

    if curl -fsS --retry 30 --retry-all-errors --retry-connrefused \
        --retry-delay 1 --retry-max-time 30 --connect-timeout 1 --max-time 5 \
        "${OPENBAO_URL}/v1/sys/health" >/dev/null 2>&1; then
        return 0
    fi
    if ! kill -0 "${BAO_PF_PID}" 2>/dev/null; then
        log_error "OpenBao port-forward exited"
        return 1
    fi
    log_error "Timed out waiting for the OpenBao port-forward"
    return 1
}

openbao_curl() {
    curl --connect-timeout 5 --max-time 15 "$@"
}

ssh_vm() {
    local vm_ip="$1"
    local command="$2"
    # VMI readiness can precede both the SSH listener and its banner. Bound the
    # complete attempt while SSH retries refusals and waits for the banner.
    kubectl exec "${CLIENT_NAME}" -c ssh-client -n "${NAMESPACE}" \
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
    start_openbao_port_forward || return 1
    request=$(jq -cn --arg role basic-spiffe --arg jwt "${VM_TOKEN}" '{role: $role, jwt: $jwt}')
    response=$(openbao_curl -fsS -X POST -H 'Content-Type: application/json' \
        -d "${request}" "${OPENBAO_URL}/v1/auth/jwt/cel/login") || return 1
    if ! jq -e '
        ((.auth.policies - ["default"]) | sort) ==
        (["spiffe-authenticated", "region-ds9", "tenant-nca-0000000000000001", "service-dsx", "resource-type-vm"] | sort)
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
tenant/nca-0000000000000001/e2e tenant-1
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

verify_imds_http_contract() {
    local vm_ip="$1"
    local token status

    token=$(ssh_vm "${vm_ip}" "curl -fsS --max-time 10 -G -H 'Metadata: true' -H 'Accept: text/plain' 'http://${IMDS_IP}/2009-04-04/meta-data/identity' --data-urlencode 'aud=${TEST_AUDIENCE}'") || return 1
    token="${token//$'\r'/}"
    check_token_contract "${token}" || return 1

    status=$(ssh_vm "${vm_ip}" "curl -sS --max-time 10 -o /dev/null -w '%{http_code}' -G 'http://${IMDS_IP}/latest/meta-data/identity' --data-urlencode 'aud=${TEST_AUDIENCE}'") || return 1
    [[ "${status//$'\r'/}" == "400" ]] || {
        log_error "IMDS accepted a request without Metadata: true (HTTP ${status})"
        return 1
    }
    status=$(ssh_vm "${vm_ip}" "curl -sS --max-time 10 -o /dev/null -w '%{http_code}' -G -H 'Metadata: true' -H 'X-Forwarded-For: 192.0.2.1' 'http://${IMDS_IP}/latest/meta-data/identity' --data-urlencode 'aud=${TEST_AUDIENCE}'") || return 1
    [[ "${status//$'\r'/}" == "403" ]] || {
        log_error "IMDS accepted a forwarded request (HTTP ${status})"
        return 1
    }
    status=$(ssh_vm "${vm_ip}" "curl -sS --max-time 10 -o /dev/null -w '%{http_code}' -X PUT 'http://${IMDS_IP}/latest/api/token'") || return 1
    [[ "${status//$'\r'/}" == "404" ]] || {
        log_error "IMDS exposes a session-token endpoint (HTTP ${status})"
        return 1
    }

    log_success "Both v1 paths, direct-request validation, and tokenless behavior passed"
}

verify_identity_controller_lifecycle() {
    log_test "Exercising the deployed Identity Controller lifecycle through SPIRE..."

    local launch_layer_source dpu_id dpu_parent host_id updated_host_id
    local original_id updated_id parent_id holder_namespace holder_service_account holder_pod_uid

    create_lifecycle_sources || return 1
    dpu_id="spiffe://k2.sjc-1.nvidia.com/tenant/nca-0000000000000001/service/dsx/dpu/${LIFECYCLE_DPU_NAME}"
    dpu_parent="spiffe://k2.sjc-1.nvidia.com/spire/agent/dsx_dpu/${LIFECYCLE_DPU_NAME}"
    host_id="spiffe://k2.sjc-1.nvidia.com/tenant/nca-0000000000000001/service/dsx/host/${LIFECYCLE_HOST_SOURCE_NAME}"
    updated_host_id="spiffe://k2.sjc-1.nvidia.com/tenant/nca-0000000000000002/service/dsx/host/${LIFECYCLE_HOST_SOURCE_NAME}-updated"
    assert_spire_entry "${dpu_id}" "${dpu_parent}" "dsx-dpu" \
        dsx-identity dpu-token-fetcher || return 1
    assert_spire_entry "${host_id}" "${dpu_parent}" "dsx-imds" \
        dsx-identity dsx-identity-dsx-imds-agent || return 1

    kubectl patch launchlayersourcefixture "${LIFECYCLE_HOST_SOURCE_NAME}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" --type=merge \
        -p "$(jq -cn --arg resource "${LIFECYCLE_HOST_SOURCE_NAME}-updated" \
            '{spec: {tenantID: "nca-0000000000000002", resourceID: $resource}}')" >/dev/null || return 1
    assert_spire_entry "${updated_host_id}" "${dpu_parent}" "dsx-imds" \
        dsx-identity dsx-identity-dsx-imds-agent || return 1
    wait_for_spire_entry_count "${host_id}" 0 >/dev/null || return 1

    kubectl delete launchlayersourcefixture "${LIFECYCLE_HOST_SOURCE_NAME}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" --wait=true --timeout=120s >/dev/null || return 1
    wait_for_spire_entry_count "${updated_host_id}" 0 >/dev/null || return 1
    kubectl delete dpus.provisioning.dpu.nvidia.com "${LIFECYCLE_DPU_NAME}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" --wait=true --timeout=120s >/dev/null || return 1
    wait_for_spire_entry_count "${dpu_id}" 0 >/dev/null || return 1
    launch_layer_source=$(kubectl get launchlayersourcefixture "${LIFECYCLE_VM_SOURCE_NAME}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" -o json) || return 1

    original_id="spiffe://k2.sjc-1.nvidia.com/tenant/nca-0000000000000001/service/dsx/vm/${LIFECYCLE_VM_NAME}/role/${IDENTITY_ROLE}"
    updated_id="spiffe://k2.sjc-1.nvidia.com/tenant/nca-0000000000000002/service/dsx/vm/${LIFECYCLE_VM_NAME}-retenanted/role/${IDENTITY_ROLE}"
    parent_id="spiffe://k2.sjc-1.nvidia.com/spire/agent/dsx_imds/tenant/nca-0000000000000001/host/$(jq -r '.spec.parentID' <<<"${launch_layer_source}")"
    holder_namespace=$(jq -er '.spec.holder.namespace' <<<"${launch_layer_source}") || return 1
    holder_service_account=$(jq -er '.spec.holder.serviceAccount' <<<"${launch_layer_source}") || return 1
    holder_pod_uid=$(jq -r '.spec.holder.podUID // ""' <<<"${launch_layer_source}") || return 1

    assert_spire_entry "${original_id}" "${parent_id}" "dsx-imds-vm" \
        "${holder_namespace}" "${holder_service_account}" "${holder_pod_uid}" || return 1
    kubectl patch launchlayersourcefixture "${LIFECYCLE_VM_SOURCE_NAME}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" --type=merge \
        -p "$(jq -cn --arg tenant nca-0000000000000002 --arg resource "${LIFECYCLE_VM_NAME}-retenanted" \
            '{spec: {tenantID: $tenant, resourceID: $resource}}')" >/dev/null || return 1
    assert_spire_entry "${updated_id}" "${parent_id}" "dsx-imds-vm" \
        "${holder_namespace}" "${holder_service_account}" "${holder_pod_uid}" || return 1
    wait_for_spire_entry_count "${original_id}" 0 >/dev/null || return 1

    kubectl patch launchlayersourcefixture "${LIFECYCLE_VM_SOURCE_NAME}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" --type=merge \
        -p '{"spec":{"holder":{"namespace":"INVALID_NAMESPACE"}}}' >/dev/null || return 1
    wait_for_spire_entry_count "${updated_id}" 0 >/dev/null || return 1

    restore_lifecycle_vm_source || return 1
    wait_for_spire_entry_count "${original_id}" 1 >/dev/null || return 1
    kubectl delete launchlayersourcefixture "${LIFECYCLE_VM_SOURCE_NAME}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" --wait=true --timeout=120s >/dev/null || return 1
    wait_for_spire_entry_count "${original_id}" 0 >/dev/null || return 1
    restore_lifecycle_vm_source || return 1
    wait_for_spire_entry_count "${original_id}" 1 >/dev/null || return 1
    kubectl delete launchlayersourcefixture "${LIFECYCLE_VM_SOURCE_NAME}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" --wait=true --timeout=120s >/dev/null || return 1
    wait_for_spire_entry_count "${original_id}" 0 >/dev/null || return 1

    LIFECYCLE_SOURCES_CREATED=false
    log_success "Identity Controller DPU, host, and VM registration lifecycles passed"
}

verify_metadata_access() {
    log_test "Requesting DSX IMDS v1 from an external tenant-network client..."

    local vm_ip response command pod_name
    kubectl wait --for=condition=Ready "pod/${CLIENT_NAME}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" --timeout=120s >/dev/null || return 1
    vm_ip=""
    command="curl -fsS --max-time 10 -G -H 'Metadata: true' -H 'Accept: application/json' 'http://${IMDS_IP}/latest/meta-data/identity' --data-urlencode 'aud=${TEST_AUDIENCE}'"
    kubectl wait --for=jsonpath='{.status.interfaces[?(@.name=="default")].ipAddress}' \
        "vmi/${VM_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}" --timeout=120s >/dev/null || return 1
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
        verify_imds_http_contract "${vm_ip}" || return 1
        verify_openbao_access || return 1
        return 0
    fi

    log_error "External client could not retrieve a DSX IMDS response from the VM"
    if [[ -n "${vm_ip}" ]]; then
        ssh_vm "${vm_ip}" "${command}" || true
    else
        log_error "VM has no reported primary-network IPv4 address"
    fi
    pod_name=$(kubectl get pod -n "${NAMESPACE}" -l kubevirt.io/domain="${VM_NAME}" \
        -o jsonpath='{.items[0].metadata.name}' --context "${CONTEXT}" 2>/dev/null || true)
    [[ -n "${pod_name}" ]] || return 1
    kubectl logs "${pod_name}" -c guest-console-log -n "${NAMESPACE}" \
        --tail=100 --context "${CONTEXT}" || true
    kubectl logs "${pod_name}" -c dsx-imds-network-setup -n "${NAMESPACE}" \
        --tail=100 --context "${CONTEXT}" || true
    kubectl logs "${pod_name}" -c metadata-service -n "${NAMESPACE}" \
        --tail=100 --context "${CONTEXT}" || true
    return 1
}

verify_vm_running() {
    log_test "Checking if VM is running..."
    
    if kubectl wait --for=jsonpath='{.status.phase}'=Running "vmi/${VM_NAME}" \
        -n "${NAMESPACE}" --timeout=60s --context "${CONTEXT}" > /dev/null 2>&1; then
        log_success "VM is running"
        return 0
    else
        log_error "VM is not running"
        return 1
    fi
}

display_vm_info() {
    echo
    echo "VM Information:"
    echo "==============="
    
    echo "VMI:"
    kubectl get vmi "${VM_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}" 2>/dev/null || echo "  Not found"
    
    echo
    echo "Launcher Pod:"
    kubectl get pod -n "${NAMESPACE}" -l kubevirt.io/domain="${VM_NAME}" --context "${CONTEXT}" 2>/dev/null || echo "  Not found"
}

main() {
    log_info "Verifying VM through DSX IMDS v1 metadata-service..."
    echo

    K1_EXCHANGE_KID=$(kubectl get secret spiffe-exchange-signing-keyset -n dsx-identity \
        --context "${K1_CONTEXT}" -o json | jq -er '.data["active-kid"]' | base64 -d)
    K2_EXCHANGE_KID=$(kubectl get secret spiffe-exchange-signing-keyset -n dsx-identity \
        --context "${CONTEXT}" -o json | jq -er '.data["active-kid"]' | base64 -d)
    if [[ "${K1_EXCHANGE_KID}" == "${K2_EXCHANGE_KID}" ]]; then
        log_error "k1 and k2 Exchange active keys reuse kid ${K1_EXCHANGE_KID}"
        exit 1
    fi

    local failed=0
    
    if verify_vm_running; then
        verify_metadata_access || failed=$((failed + 1))
        verify_identity_controller_lifecycle || failed=$((failed + 1))
    else
        failed=$((failed + 1))
    fi
    
    display_vm_info
    
    echo
    if [[ $failed -eq 0 ]]; then
        log_success "All VM verification tests passed"
    else
        log_error "${failed} test(s) failed"
        exit 1
    fi
}

trap cleanup EXIT
trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"

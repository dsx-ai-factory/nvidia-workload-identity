#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
K1_CONTEXT="kind-k1"
K2_CONTEXT="kind-k2"
K1_TRUST_DOMAIN="sjc-1.nvidia.com"
K2_TRUST_DOMAIN="k2.sjc-1.nvidia.com"
BAO_IMAGE="quay.io/openbao/openbao:2.4.0"
BAO_ADDR="http://127.0.0.1:8200"
VAULT_ADDR="${BAO_ADDR}"
BAO_TOKEN=""
AUDIENCE="spiffe://ds9.dsx.nvid.id/openbao"
ISSUER="spiffe://ds9.dsx.nvid.id"
SIGNING_SECRET="spiffe-exchange-signing-keyset"
IDENTITY_NAMESPACE="dsx-identity"
K1_POD="e2e-nginx"
K2_POD="e2e-nginx"
FIXTURE_NAMESPACE="dsx-identity-e2e"
K1_EXCHANGE_URL="http://127.0.0.1:18080"
K2_EXCHANGE_URL="http://127.0.0.1:28080"
BAO_PF_PID=""
K1_EXCHANGE_PF_PID=""
K2_EXCHANGE_PF_PID=""
MQTT_PF_PID=""
KEYCLOAK_PF_PID=""
K1_ORIGINAL_JWKS=""
K1_ORIGINAL_KID=""
K2_ORIGINAL_JWKS=""
K2_ORIGINAL_KID=""
K2_SIGNING_SECRET_CHANGED=false
PROBE_DIR=""
SPIRE_SVID_PROBE=""

export BAO_ADDR VAULT_ADDR

source "${PROJECT_ROOT}/scripts/common.sh"

bao_cli() {
    run_bao_cli "${BAO_IMAGE}" "$@"
}

stop_port_forward() {
    local pid="$1"
    [[ -n "${pid}" ]] || return 0
    kill "${pid}" 2>/dev/null || true
    wait "${pid}" 2>/dev/null || true
}

update_signing_secret() {
    local context="$1"
    local jwks="$2"
    local kid="$3"
    kubectl create secret generic "${SIGNING_SECRET}" \
        --namespace "${IDENTITY_NAMESPACE}" \
        --context "${context}" \
        --from-literal="signing-jwks.json=${jwks}" \
        --from-literal="active-kid=${kid}" \
        --dry-run=client -o yaml | kubectl apply -f - --context "${context}" >/dev/null
}

start_exchange_port_forward() {
    local context="$1"
    local port="$2"
    local pid_variable="$3"
    local old_pid="${!pid_variable}"
    stop_port_forward "${old_pid}"
    kubectl port-forward -n "${IDENTITY_NAMESPACE}" svc/spiffe-exchange "${port}:8080" \
        --context "${context}" >/dev/null 2>&1 &
    printf -v "${pid_variable}" '%s' "$!"

    local pid="${!pid_variable}"
    if curl -fsS --retry 60 --retry-all-errors --retry-connrefused \
        --retry-delay 1 --retry-max-time 60 --connect-timeout 1 --max-time 5 \
        "http://127.0.0.1:${port}/ready" >/dev/null 2>&1; then
        return 0
    fi
    if ! kill -0 "${pid}" 2>/dev/null; then
        log_error "${context} Exchange port-forward exited"
        return 1
    fi
    log_error "Timed out waiting for ${context} Exchange"
    return 1
}

restart_exchange() {
    local context="$1"
    local port="$2"
    local pid_variable="$3"
    kubectl rollout restart deployment/spiffe-exchange -n "${IDENTITY_NAMESPACE}" \
        --context "${context}" >/dev/null
    kubectl rollout status deployment/spiffe-exchange -n "${IDENTITY_NAMESPACE}" \
        --context "${context}" --timeout=180s >/dev/null
    start_exchange_port_forward "${context}" "${port}" "${pid_variable}"
}

assert_exchange_service_survives_pod_loss() {
    local name="$1"
    local context="$2"
    local fixture_pod="$3"
    local pod
    pod=$(kubectl get pods -n "${IDENTITY_NAMESPACE}" --context "${context}" \
        -l app=spiffe-exchange -o json | jq -er '[.items[] | select(.status.phase == "Running") | .metadata.name][0]')
    kubectl delete pod "${pod}" -n "${IDENTITY_NAMESPACE}" --context "${context}" --wait=false >/dev/null
    kubectl wait --for=delete "pod/${pod}" -n "${IDENTITY_NAMESPACE}" --context "${context}" --timeout=60s >/dev/null
    kubectl exec "${fixture_pod}" -c nginx -n "${FIXTURE_NAMESPACE}" --context "${context}" -- \
        curl -fsS --max-time 5 http://spiffe-exchange.dsx-identity.svc.cluster.local:8080/ready >/dev/null
    kubectl rollout status deployment/spiffe-exchange -n "${IDENTITY_NAMESPACE}" \
        --context "${context}" --timeout=180s >/dev/null
    log_success "${name} Exchange served through a Pod loss"
}

cleanup() {
    set +e
    cleanup_command_output_follower
    if [[ "${K2_SIGNING_SECRET_CHANGED}" == "true" && -n "${K2_ORIGINAL_JWKS}" && -n "${K2_ORIGINAL_KID}" ]]; then
        log_info "Restoring the original k2 signing keyset after a failed rotation"
        update_signing_secret "${K2_CONTEXT}" "${K2_ORIGINAL_JWKS}" "${K2_ORIGINAL_KID}"
        kubectl rollout restart deployment/spiffe-exchange -n "${IDENTITY_NAMESPACE}" \
            --context "${K2_CONTEXT}" >/dev/null
        kubectl rollout status deployment/spiffe-exchange -n "${IDENTITY_NAMESPACE}" \
            --context "${K2_CONTEXT}" --timeout=180s >/dev/null
    fi
    stop_port_forward "${BAO_PF_PID}"
    stop_port_forward "${K1_EXCHANGE_PF_PID}"
    stop_port_forward "${K2_EXCHANGE_PF_PID}"
    stop_port_forward "${MQTT_PF_PID}"
    stop_port_forward "${KEYCLOAK_PF_PID}"
    if [[ -n "${PROBE_DIR}" ]]; then
        rm -rf "${PROBE_DIR}"
    fi
}

start_port_forwards() {
    kubectl port-forward -n openbao svc/openbao 8200:8200 --context "${K1_CONTEXT}" >/dev/null 2>&1 &
    BAO_PF_PID=$!
    kubectl port-forward -n "${IDENTITY_NAMESPACE}" svc/nats 11884:1883 \
        --context "${K1_CONTEXT}" >/dev/null 2>&1 &
    MQTT_PF_PID=$!
    kubectl port-forward -n "${IDENTITY_NAMESPACE}" svc/keycloak-service 18081:8080 \
        --context "${K1_CONTEXT}" >/dev/null 2>&1 &
    KEYCLOAK_PF_PID=$!
    start_exchange_port_forward "${K1_CONTEXT}" 18080 K1_EXCHANGE_PF_PID
    start_exchange_port_forward "${K2_CONTEXT}" 28080 K2_EXCHANGE_PF_PID

    curl -fsS --retry 30 --retry-all-errors --retry-connrefused \
        --retry-delay 1 --retry-max-time 30 --connect-timeout 1 --max-time 5 \
        http://127.0.0.1:8200/v1/sys/health >/dev/null || return 1
    curl -fsS --retry 30 --retry-all-errors --retry-connrefused \
        --retry-delay 1 --retry-max-time 30 --connect-timeout 1 --max-time 5 \
        http://127.0.0.1:18081/realms/event-bus/.well-known/openid-configuration >/dev/null || return 1
    if ! kill -0 "${BAO_PF_PID}" 2>/dev/null ||
        ! kill -0 "${MQTT_PF_PID}" 2>/dev/null ||
        ! kill -0 "${KEYCLOAK_PF_PID}" 2>/dev/null; then
        log_error "OpenBao or Event Bus port-forward exited"
        return 1
    fi
}

event_bus_access_token() {
    local client_id client_secret
    client_id=$(kubectl get secret spiffe-exchange-mqtt-oauth -n "${IDENTITY_NAMESPACE}" \
        --context "${K1_CONTEXT}" -o json | jq -er '.data["client-id"]' | base64 -d)
    client_secret=$(kubectl get secret spiffe-exchange-mqtt-oauth -n "${IDENTITY_NAMESPACE}" \
        --context "${K1_CONTEXT}" -o json | jq -er '.data["client-secret"]' | base64 -d)
    curl -fsS --max-time 10 \
        --data-urlencode grant_type=client_credentials \
        --data-urlencode scope=mqtt \
        --data-urlencode "client_id=${client_id}" \
        --data-urlencode "client_secret=${client_secret}" \
        http://127.0.0.1:18081/realms/event-bus/protocol/openid-connect/token | jq -er '.access_token'
}

assert_retained_keys() {
    local shard_id="$1"
    local jwks="$2"
    local access_token expected_jwks started_at
    access_token=$(event_bus_access_token)
    expected_jwks=$(jq -c '{keys: [.keys[] | del(.d)]}' <<<"${jwks}")
    started_at=$(date -u +'%Y-%m-%dT%H:%M:%S.%NZ')
    DSX_E2E_MQTT_BROKER=tcp://127.0.0.1:11884 \
        DSX_E2E_MQTT_TOKEN="${access_token}" \
        DSX_E2E_MQTT_SHARD_ID="${shard_id}" \
        DSX_E2E_MQTT_EXPECTED_JWKS="${expected_jwks}" \
        go test -count=1 -run '^TestLiveMQTTAuthorization$' ./cmd/spiffe-exchange-service

    if ! wait_for_command_output 30s \
        'Publish Violation - Subject "outside.dsx-identity-e2e"' '' \
        kubectl logs -f statefulset/nats -n "${IDENTITY_NAMESPACE}" \
        --all-containers=true --since-time="${started_at}" --context "${K1_CONTEXT}"; then
        log_error "Event Bus did not reject the out-of-prefix MQTT publish"
        return 1
    fi
}

read_pod_token() {
    local context="$1"
    local pod="$2"
    local container="$3"
    kubectl exec "${pod}" -c "${container}" --namespace "${FIXTURE_NAMESPACE}" \
        --context "${context}" -- cat /var/run/secrets/nvidia.com/dsx/token
}

jwt_json_part() {
    local token="$1"
    local part="$2"
    local encoded padding
    encoded=$(cut -d. -f"${part}" <<<"${token}")
    padding=$(( (4 - ${#encoded} % 4) % 4 ))
    encoded="${encoded}$(printf '%*s' "${padding}" '' | tr ' ' '=')"
    tr '_-' '/+' <<<"${encoded}" | base64 -d
}

assert_token_contract() {
    local name="$1"
    local token="$2"
    local subject="$3"
    local audience="${4:-${AUDIENCE}}"
    local header payload now
    header=$(jwt_json_part "${token}" 1)
    payload=$(jwt_json_part "${token}" 2)
    now=$(date +%s)
    jq -e --arg issuer "${ISSUER}" --arg audience "${audience}" --arg subject "${subject}" --argjson now "${now}" '
        .iss == $issuer and
        .aud == [$audience] and
        .sub == $subject and
        (.exp > .iat and .exp > $now and (.exp - .iat) <= 300)
    ' <<<"${payload}" >/dev/null
    jq -e '.alg == "ES256" and (.kid | type == "string" and length > 0)' <<<"${header}" >/dev/null
    log_success "${name} token has the canonical issuer, audience, subject, and ES256 header"
}

token_kid() {
    jwt_json_part "$1" 1 | jq -er '.kid'
}

pod_subject() {
    local context="$1"
    local pod="$2"
    local resource_type="$3"
    local resource_id="$4"
    local uid
    uid=$(kubectl get pod "${pod}" --namespace "${FIXTURE_NAMESPACE}" \
        --context "${context}" -o jsonpath='{.metadata.uid}')
    printf 'spiffe://nca-0000000000000001.ds9.dsx.nvid.id/%s/%s/pod/%s' \
        "${resource_type}" "${resource_id}" "${uid}"
}

internal_pod_subject() {
    local context="$1"
    local trust_domain="$2"
    local pod="$3"
    local uid
    uid=$(kubectl get pod "${pod}" --namespace "${FIXTURE_NAMESPACE}" \
        --context "${context}" -o jsonpath='{.metadata.uid}')
    printf 'spiffe://%s/tenant/nca-0000000000000001/service/dsx/host/e2e-nginx/pod/%s' \
        "${trust_domain}" "${uid}"
}

prepare_spire_svid_probe() {
    PROBE_DIR=$(mktemp -d)
    SPIRE_SVID_PROBE="${PROBE_DIR}/spire-svid-probe"
    CGO_ENABLED=0 go test -c -o "${SPIRE_SVID_PROBE}" ./cmd/dsx-token-fetcher
}

fetch_spire_jwt_svid() {
    local context="$1"
    local pod="$2"
    local expected_id="$3"
    local audience="$4"
    local output token
    kubectl cp "${SPIRE_SVID_PROBE}" \
        "${FIXTURE_NAMESPACE}/${pod}:/tmp/spire-svid-probe" \
        -c dsx-token-fetcher --context "${context}" >/dev/null
    kubectl exec "${pod}" -c dsx-token-fetcher --namespace "${FIXTURE_NAMESPACE}" \
        --context "${context}" -- chmod 0700 /tmp/spire-svid-probe
    output=$(kubectl exec "${pod}" -c dsx-token-fetcher --namespace "${FIXTURE_NAMESPACE}" \
        --context "${context}" -- env \
        DSX_E2E_SPIFFE_ENDPOINT_SOCKET=unix:///var/run/secrets/spire.io/spire-agent.sock \
        DSX_E2E_SPIRE_JWT_AUDIENCE="${audience}" \
        DSX_E2E_SPIRE_JWT_ID="${expected_id}" \
        /tmp/spire-svid-probe -test.v -test.run '^TestLiveFetchSPIREJWTSVID$')
    token=$(awk -F= '/^SPIRE_JWT=/{sub(/^SPIRE_JWT=/, ""); print; exit}' <<<"${output}")
    if [[ "${token}" != *.*.* ]]; then
        log_error "SPIRE Workload API did not return a JWT-SVID for ${expected_id}"
        return 1
    fi
    printf '%s' "${token}"
}

exchange_spire_svid() {
    local endpoint="$1"
    local token="$2"
    curl -fsS --max-time 10 -X POST \
        -H 'Content-Type: application/x-www-form-urlencoded' \
        --data-urlencode 'grant_type=urn:ietf:params:oauth:grant-type:token-exchange' \
        --data-urlencode "subject_token=${token}" \
        --data-urlencode 'subject_token_type=urn:ietf:params:oauth:token-type:jwt' \
        "${endpoint}/v1/exchange" | jq -er '.access_token'
}

assert_exchange_rejects() {
    local name="$1"
    local endpoint="$2"
    local token="$3"
    local output status body
    output=$(curl -sS --max-time 10 -w $'\n%{http_code}' -X POST \
        -H 'Content-Type: application/x-www-form-urlencoded' \
        --data-urlencode 'grant_type=urn:ietf:params:oauth:grant-type:token-exchange' \
        --data-urlencode "subject_token=${token}" \
        --data-urlencode 'subject_token_type=urn:ietf:params:oauth:token-type:jwt' \
        "${endpoint}/v1/exchange") || return 1
    status=${output##*$'\n'}
    body=${output%$'\n'*}
    if [[ "${status}" != "401" ]] || ! jq -e '.error == "invalid_grant"' <<<"${body}" >/dev/null; then
        log_error "${name} was not rejected as invalid_grant (HTTP ${status})"
        return 1
    fi
    log_success "${name} was rejected"
}

public_keyset() {
    jq -c '{keys: [.keys[] | del(.d)]}' <<<"$1"
}

aggregate_keysets() {
    local first second
    first=$(public_keyset "$1")
    second=$(public_keyset "$2")
    jq -cn --argjson first "${first}" --argjson second "${second}" \
        '{keys: (($first.keys + $second.keys) | sort_by(.kid))}'
}

wait_for_pod_log() {
    local context="$1"
    local pod="$2"
    local since="$3"
    local first="$4"
    local second="$5"
    wait_for_command_output 60s "${first}" "${second}" \
        kubectl logs -f "${pod}" -c spiffe-exchange -n "${IDENTITY_NAMESPACE}" \
        --context "${context}" "${since}"
}

wait_for_exchange_key_logs() {
    local context="$1"
    local expected="$2"
    local kid pod
    local -a kids pods
    mapfile -t kids < <(jq -er '.keys[].kid' <<<"${expected}")
    mapfile -t pods < <(kubectl get pods -n "${IDENTITY_NAMESPACE}" --context "${context}" \
        -l app=spiffe-exchange -o name)
    if [[ "${#pods[@]}" -ne 2 ]]; then
        log_error "${context} has ${#pods[@]} Exchange Pods, want 2"
        return 1
    fi
    for pod in "${pods[@]}"; do
        for kid in "${kids[@]}"; do
            if ! wait_for_pod_log "${context}" "${pod}" --since=2h \
                '"msg":"Accepted public key"' "/kid/${kid}"; then
                log_error "${pod} did not accept kid ${kid}"
                return 1
            fi
        done
    done
}

read_aggregate() {
    local endpoint="$1"
    local expected="$2"
    local actual
    actual=$(curl -fsS --max-time 5 "${endpoint}/.well-known/jwks.json") || return 1
    if ! jq -e --argjson expected "${expected}" '
        (.keys | all(has("d") | not)) and
        ((.keys | sort_by(.kid)) == $expected.keys)
    ' <<<"${actual}" >/dev/null; then
        log_error "${endpoint} did not serve the expected aggregate"
        return 1
    fi
    printf '%s' "${actual}"
}

verify_tokens_with_jwks() {
    local jwks="$1"
    shift
    local tokens='[]' token
    for token in "$@"; do
        tokens=$(jq -cn --argjson tokens "${tokens}" --arg token "${token}" '$tokens + [$token]')
    done
    DSX_E2E_VERIFY_JWKS="${jwks}" DSX_E2E_VERIFY_TOKENS="${tokens}" \
        go test -count=1 -run '^TestLiveAggregateVerifiesTokens$' ./cmd/spiffe-exchange-service
}

assert_full_aggregates() {
    local expected="$1"
    shift
    local k1_jwks k2_jwks
    if ! jq -e '(.keys | map(.kid) | length) == (.keys | map(.kid) | unique | length)' \
        <<<"${expected}" >/dev/null; then
        log_error "Expected aggregate contains a duplicate kid"
        return 1
    fi
    wait_for_exchange_key_logs "${K1_CONTEXT}" "${expected}" || return 1
    wait_for_exchange_key_logs "${K2_CONTEXT}" "${expected}" || return 1
    k1_jwks=$(read_aggregate "${K1_EXCHANGE_URL}" "${expected}") || return 1
    k2_jwks=$(read_aggregate "${K2_EXCHANGE_URL}" "${expected}") || return 1
    verify_tokens_with_jwks "${k1_jwks}" "$@"
    verify_tokens_with_jwks "${k2_jwks}" "$@"
    log_success "Both Exchanges serve the expected aggregate and verify every shard token"
}

assert_k2_gateway_login() {
    local token="$1"
    local request response
    request=$(jq -cn --arg role basic-spiffe --arg jwt "${token}" '{role: $role, jwt: $jwt}')
    response=$(kubectl exec "${K2_POD}" -c nginx --namespace "${FIXTURE_NAMESPACE}" \
        --context "${K2_CONTEXT}" -- curl -fsS --max-time 10 -X POST -H 'Content-Type: application/json' \
        -d "${request}" http://gateway.sjc-1.nvidia.com:8200/v1/auth/jwt/cel/login)
    jq -e '.auth.client_token | type == "string" and length > 0' <<<"${response}" >/dev/null
    log_success "k2 reached external OpenBao through the k1 Gateway and authenticated its local Exchange token"
}

openbao_login() {
    local token="$1"
    BAO_TOKEN=""
    export BAO_TOKEN
    jq -n --arg role basic-spiffe --arg jwt "${token}" '{role: $role, jwt: $jwt}' | \
        bao_cli write -format=json auth/jwt/cel/login -
}

reload_openbao_jwks() {
    local root_token
    root_token=$(kubectl get secret openbao-init-keys -n openbao --context "${K1_CONTEXT}" \
        -o jsonpath='{.data.root_token}' | base64 -d)
    BAO_TOKEN="${root_token}" VAULT_TOKEN="${root_token}" bao_cli write auth/jwt/config \
        jwks_url="http://spiffe-exchange.dsx-identity.svc.cluster.local:8080/.well-known/jwks.json" \
        bound_issuer="${ISSUER}" \
        jwt_supported_algs=ES256 >/dev/null
}

assert_login_and_access() {
    local name="$1"
    local token="$2"
    local resource_type="$3"
    local response client_token path expected sibling_type
    response=$(openbao_login "${token}")
    jq -e --arg resource_type "${resource_type}" '
        ((.auth.policies - ["default"]) | sort) ==
        (["spiffe-authenticated", "region-ds9", "tenant-nca-0000000000000001", "service-dsx", "resource-type-" + $resource_type] | sort)
    ' <<<"${response}" >/dev/null
    client_token=$(jq -er '.auth.client_token' <<<"${response}")

    BAO_TOKEN="${client_token}"
    export BAO_TOKEN
    while read -r path expected; do
        bao_cli kv get -format=json "secret/${path}" | \
            jq -e --arg value "${expected}" '.data.data.value == $value' >/dev/null
    done <<EOF
region/ds9/e2e region-ds9
tenant/nca-0000000000000001/e2e tenant-1
service/dsx/e2e service-dsx
resource-type/${resource_type}/e2e resource-${resource_type}
EOF
    local status
    for sibling_type in dpu host vm; do
        [[ "${sibling_type}" == "${resource_type}" ]] && continue
        status=$(curl -sS --max-time 10 -o /dev/null -w '%{http_code}' \
            -H "X-Vault-Token: ${client_token}" \
            "http://127.0.0.1:8200/v1/secret/data/resource-type/${sibling_type}/e2e") || return 1
        [[ "${status}" == "403" ]] || {
            log_error "${name} token received HTTP ${status} for the ${sibling_type} resource secret"
            return 1
        }
    done
    status=$(curl -sS --max-time 10 -o /dev/null -w '%{http_code}' \
        -H "X-Vault-Token: ${client_token}" \
        http://127.0.0.1:8200/v1/secret/data/forbidden/admin/e2e) || return 1
    [[ "${status}" == "403" ]] || {
        log_error "${name} token received HTTP ${status} for the forbidden secret"
        return 1
    }
    log_success "${name} received exact policies and only its permitted secrets"
}

assert_login_fails() {
    local name="$1"
    local token="$2"
    local request output status body
    request=$(jq -cn --arg role basic-spiffe --arg jwt "${token}" \
        '{role: $role, jwt: $jwt}') || return 1
    output=$(curl -sS --max-time 10 -w $'\n%{http_code}' -X POST \
        -H 'Content-Type: application/json' \
        -d "${request}" \
        http://127.0.0.1:8200/v1/auth/jwt/cel/login) || return 1
    status=${output##*$'\n'}
    body=${output%$'\n'*}
    if [[ "${status}" != "400" ]] || ! jq -e '.errors | type == "array" and length > 0' <<<"${body}" >/dev/null; then
        log_error "${name} token was not rejected by OpenBao (HTTP ${status})"
        return 1
    fi
    log_success "${name} token was rejected"
}

rotate_k2_signing_key() {
    local k1_token="$1"
    local old_k2_token="$2"
    local generated new_kid new_private new_jwks retained_jwks expected
    local new_k2_token new_k2_subject new_k2_spire_id new_k2_spire_svid

    generated=$(go run ./cmd/generate-signing-keyset)
    new_kid=$(jq -er '.active_kid' <<<"${generated}")
    new_private=$(jq -c '.jwks.keys[0]' <<<"${generated}")
    if jq -e --arg kid "${new_kid}" 'any(.keys[]; .kid == $kid)' \
        <<<"$(aggregate_keysets "${K1_ORIGINAL_JWKS}" "${K2_ORIGINAL_JWKS}")" >/dev/null; then
        log_error "Generated rotation key reused kid ${new_kid}"
        return 1
    fi

    new_jwks=$(jq -cn --argjson active "${new_private}" '{keys: [$active]}')
    update_signing_secret "${K2_CONTEXT}" "${new_jwks}" "${new_kid}"
    K2_SIGNING_SECRET_CHANGED=true
    restart_exchange "${K2_CONTEXT}" 28080 K2_EXCHANGE_PF_PID
    retained_jwks=$(aggregate_keysets "${K2_ORIGINAL_JWKS}" "${new_jwks}")
    assert_retained_keys k2 "${retained_jwks}"
    expected=$(aggregate_keysets "${K1_ORIGINAL_JWKS}" "${retained_jwks}")
    assert_full_aggregates "${expected}" "${k1_token}" "${old_k2_token}"
    assert_login_and_access "k2 before old key publication lease expiry" "${old_k2_token}" host

    new_k2_subject=$(pod_subject "${K2_CONTEXT}" "${K2_POD}" host e2e-nginx)
    new_k2_spire_id=$(internal_pod_subject "${K2_CONTEXT}" "${K2_TRUST_DOMAIN}" "${K2_POD}")
    new_k2_spire_svid=$(fetch_spire_jwt_svid \
        "${K2_CONTEXT}" "${K2_POD}" "${new_k2_spire_id}" "${AUDIENCE}")
    new_k2_token=$(exchange_spire_svid "${K2_EXCHANGE_URL}" "${new_k2_spire_svid}")
    assert_token_contract "k2 rotated" "${new_k2_token}" "${new_k2_subject}"
    if [[ "$(token_kid "${new_k2_token}")" != "${new_kid}" ]]; then
        log_error "k2 Exchange did not sign with rotated kid ${new_kid}"
        return 1
    fi
    assert_full_aggregates "${expected}" "${k1_token}" "${old_k2_token}" "${new_k2_token}"
    assert_login_and_access "k2 rotated" "${new_k2_token}" host
    K2_SIGNING_SECRET_CHANGED=false
    log_success "k2 rotation preserved the old and new keys during overlap"
}

main() {
    validate_context_exists "${K1_CONTEXT}"
    validate_context_exists "${K2_CONTEXT}"
    cd "${PROJECT_ROOT}"

    K1_ORIGINAL_JWKS=$(kubectl get secret "${SIGNING_SECRET}" -n "${IDENTITY_NAMESPACE}" \
        --context "${K1_CONTEXT}" -o json | jq -er '.data["signing-jwks.json"]' | base64 -d)
    K1_ORIGINAL_KID=$(kubectl get secret "${SIGNING_SECRET}" -n "${IDENTITY_NAMESPACE}" \
        --context "${K1_CONTEXT}" -o json | jq -er '.data["active-kid"]' | base64 -d)
    K2_ORIGINAL_JWKS=$(kubectl get secret "${SIGNING_SECRET}" -n "${IDENTITY_NAMESPACE}" \
        --context "${K2_CONTEXT}" -o json | jq -er '.data["signing-jwks.json"]' | base64 -d)
    K2_ORIGINAL_KID=$(kubectl get secret "${SIGNING_SECRET}" -n "${IDENTITY_NAMESPACE}" \
        --context "${K2_CONTEXT}" -o json | jq -er '.data["active-kid"]' | base64 -d)
    if [[ "${K1_ORIGINAL_KID}" == "${K2_ORIGINAL_KID}" ]]; then
        log_error "k1 and k2 signing keysets reuse kid ${K1_ORIGINAL_KID}"
        return 1
    fi

    start_port_forwards
    kubectl wait --for=condition=Ready "pod/${K1_POD}" \
        --namespace "${FIXTURE_NAMESPACE}" --context "${K1_CONTEXT}" --timeout=180s >/dev/null
    kubectl wait --for=condition=Ready "pod/${K2_POD}" \
        --namespace "${FIXTURE_NAMESPACE}" --context "${K2_CONTEXT}" --timeout=180s >/dev/null
    assert_exchange_service_survives_pod_loss k1 "${K1_CONTEXT}" "${K1_POD}"
    assert_exchange_service_survives_pod_loss k2 "${K2_CONTEXT}" "${K2_POD}"
    start_exchange_port_forward "${K1_CONTEXT}" 18080 K1_EXCHANGE_PF_PID
    start_exchange_port_forward "${K2_CONTEXT}" 28080 K2_EXCHANGE_PF_PID
    prepare_spire_svid_probe

    local k1_token k2_token k1_subject k2_subject k1_spire_id k2_spire_id
    local k1_spire_svid k2_spire_svid wrong_audience wrong_audience_svid wrong_audience_token
    local local_k1_token local_k2_token expected restarted_k1_token restarted_k2_token
    k1_token=$(read_pod_token "${K1_CONTEXT}" "${K1_POD}" nginx)
    k2_token=$(read_pod_token "${K2_CONTEXT}" "${K2_POD}" nginx)
    k1_subject=$(pod_subject "${K1_CONTEXT}" "${K1_POD}" host e2e-nginx)
    k2_subject=$(pod_subject "${K2_CONTEXT}" "${K2_POD}" host e2e-nginx)
    assert_token_contract k1 "${k1_token}" "${k1_subject}"
    assert_token_contract k2 "${k2_token}" "${k2_subject}"
    [[ "$(token_kid "${k1_token}")" == "${K1_ORIGINAL_KID}" ]] || {
        log_error "k1 Pod did not use the k1 Exchange signing key"
        return 1
    }
    [[ "$(token_kid "${k2_token}")" == "${K2_ORIGINAL_KID}" ]] || {
        log_error "k2 Pod did not use the k2 Exchange signing key"
        return 1
    }

    k1_spire_id=$(internal_pod_subject "${K1_CONTEXT}" "${K1_TRUST_DOMAIN}" "${K1_POD}")
    k2_spire_id=$(internal_pod_subject "${K2_CONTEXT}" "${K2_TRUST_DOMAIN}" "${K2_POD}")
    k1_spire_svid=$(fetch_spire_jwt_svid "${K1_CONTEXT}" "${K1_POD}" "${k1_spire_id}" "${AUDIENCE}")
    k2_spire_svid=$(fetch_spire_jwt_svid "${K2_CONTEXT}" "${K2_POD}" "${k2_spire_id}" "${AUDIENCE}")
    local_k1_token=$(exchange_spire_svid "${K1_EXCHANGE_URL}" "${k1_spire_svid}")
    local_k2_token=$(exchange_spire_svid "${K2_EXCHANGE_URL}" "${k2_spire_svid}")
    assert_token_contract "k1 SPIRE JWT-SVID through k1 Exchange" "${local_k1_token}" "${k1_subject}"
    assert_token_contract "k2 SPIRE JWT-SVID through k2 Exchange" "${local_k2_token}" "${k2_subject}"
    [[ "$(token_kid "${local_k1_token}")" == "${K1_ORIGINAL_KID}" ]] || return 1
    [[ "$(token_kid "${local_k2_token}")" == "${K2_ORIGINAL_KID}" ]] || return 1
    assert_exchange_rejects "k2 SPIRE JWT-SVID at k1 Exchange" "${K1_EXCHANGE_URL}" "${k2_spire_svid}"
    assert_exchange_rejects "k1 SPIRE JWT-SVID at k2 Exchange" "${K2_EXCHANGE_URL}" "${k1_spire_svid}"

    expected=$(aggregate_keysets "${K1_ORIGINAL_JWKS}" "${K2_ORIGINAL_JWKS}")
    assert_retained_keys k1 "${K1_ORIGINAL_JWKS}"
    assert_retained_keys k2 "${K2_ORIGINAL_JWKS}"
    assert_full_aggregates "${expected}" "${k1_token}" "${k2_token}" "${local_k1_token}" "${local_k2_token}"
    assert_k2_gateway_login "${k2_token}"
    assert_login_and_access k1 "${k1_token}" host
    assert_login_and_access k2 "${k2_token}" host
    wrong_audience=spiffe://wrong.example/audience
    wrong_audience_svid=$(fetch_spire_jwt_svid \
        "${K1_CONTEXT}" "${K1_POD}" "${k1_spire_id}" "${wrong_audience}")
    wrong_audience_token=$(exchange_spire_svid "${K1_EXCHANGE_URL}" "${wrong_audience_svid}")
    assert_token_contract "real wrong-audience token" \
        "${wrong_audience_token}" "${k1_subject}" "${wrong_audience}"
    assert_login_fails "wrong audience" "${wrong_audience_token}"

    restart_exchange "${K1_CONTEXT}" 18080 K1_EXCHANGE_PF_PID
    assert_retained_keys k1 "${K1_ORIGINAL_JWKS}"
    assert_full_aggregates "${expected}" "${k1_token}" "${k2_token}"
    restart_exchange "${K2_CONTEXT}" 28080 K2_EXCHANGE_PF_PID
    assert_retained_keys k2 "${K2_ORIGINAL_JWKS}"
    assert_full_aggregates "${expected}" "${k1_token}" "${k2_token}"

    k1_spire_svid=$(fetch_spire_jwt_svid "${K1_CONTEXT}" "${K1_POD}" "${k1_spire_id}" "${AUDIENCE}")
    k2_spire_svid=$(fetch_spire_jwt_svid "${K2_CONTEXT}" "${K2_POD}" "${k2_spire_id}" "${AUDIENCE}")
    restarted_k1_token=$(exchange_spire_svid "${K1_EXCHANGE_URL}" "${k1_spire_svid}")
    restarted_k2_token=$(exchange_spire_svid "${K2_EXCHANGE_URL}" "${k2_spire_svid}")
    assert_token_contract "k1 after restart" "${restarted_k1_token}" "${k1_subject}"
    assert_token_contract "k2 after restart" "${restarted_k2_token}" "${k2_subject}"
    [[ "$(token_kid "${restarted_k1_token}")" == "${K1_ORIGINAL_KID}" ]] || return 1
    [[ "$(token_kid "${restarted_k2_token}")" == "${K2_ORIGINAL_KID}" ]] || return 1
    assert_full_aggregates "${expected}" "${restarted_k1_token}" "${restarted_k2_token}"
    assert_login_and_access "k2 after restart" "${restarted_k2_token}" host

    rotate_k2_signing_key "${restarted_k1_token}" "${restarted_k2_token}"
    log_success "Local Exchange isolation, shared JWKS, OpenBao, restart, and rotation checks passed"
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
main "$@"

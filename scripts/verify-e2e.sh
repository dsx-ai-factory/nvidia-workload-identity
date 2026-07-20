#!/bin/bash
# Verify cross-shard DSX Identity Keys, Event Bus, and OpenBao behavior.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
K1_CONTEXT="kind-k1"
K2_CONTEXT="kind-k2"
K1_TRUST_DOMAIN="sjc-1.nvidia.com"
K2_TRUST_DOMAIN="k2.sjc-1.nvidia.com"
GATEWAY_HOST="gateway.sjc-1.nvidia.com"
OPENBAO_URL="http://${GATEWAY_HOST}:8200"
EVENT_BUS_OAUTH_URL="http://${GATEWAY_HOST}:8080/realms/event-bus/protocol/openid-connect/token"
MQTT_BROKER="tcp://${GATEWAY_HOST}:1883"
AUDIENCE="spiffe://ds9.dsx.nvid.id/openbao"
ISSUER="spiffe://ds9.dsx.nvid.id"
SIGNING_SECRET="dsx-identity-keys-signing-keyset"
IDENTITY_NAMESPACE="dsx-identity"
OPERATOR_TENANT="operator-1"
K1_POD="e2e-nginx"
K2_POD="e2e-nginx"
K2_SPIRE_POD="e2e-spire-client"
K2_SPIRE_NAMESPACE="dsx-identity-spire-e2e"
FIXTURE_NAMESPACE="dsx-identity-e2e"
IDENTITY_PROBE_CONTAINER="identity-probe"
IDENTITY_KEYS_NODE_PORT=30080
GATEWAY_ADDRESS=""
E2E_PROBE_IMAGE=""
K1_IDENTITY_KEYS_URL=""
K2_IDENTITY_KEYS_URL=""
K1_ORIGINAL_JWKS=""
K1_ORIGINAL_KID=""
K2_ORIGINAL_JWKS=""
K2_ORIGINAL_KID=""

source "${PROJECT_ROOT}/scripts/common.sh"

external_curl() {
    docker run --rm --network kind \
        --add-host "${GATEWAY_HOST}:${GATEWAY_ADDRESS}" \
        --entrypoint curl "${E2E_PROBE_IMAGE}" "$@"
}

external_probe() {
    local test_pattern=$1
    shift
    local docker_env=() environment
    for environment in "$@"; do
        docker_env+=(--env "${environment}")
    done
    docker run --rm --network kind \
        --add-host "${GATEWAY_HOST}:${GATEWAY_ADDRESS}" \
        "${docker_env[@]}" \
        --entrypoint /usr/local/bin/identity-keys-probe "${E2E_PROBE_IMAGE}" \
        -test.v -test.run "${test_pattern}"
}

initialize_external_test_process() {
    local k1_node_address k2_node_address
    GATEWAY_ADDRESS=$(get_gateway_address \
        "${K1_CONTEXT}" envoy-gateway-system k1-gateway)
    E2E_PROBE_IMAGE=$(kubectl get pod "${K2_SPIRE_POD}" \
        --namespace "${K2_SPIRE_NAMESPACE}" --context "${K2_CONTEXT}" \
        -o jsonpath='{.spec.containers[?(@.name=="identity-probe")].image}')
    docker image inspect "${E2E_PROBE_IMAGE}" >/dev/null
    k1_node_address=$(docker inspect k1-control-plane \
        --format '{{(index .NetworkSettings.Networks "kind").IPAddress}}')
    k2_node_address=$(docker inspect k2-control-plane \
        --format '{{(index .NetworkSettings.Networks "kind").IPAddress}}')
    K1_IDENTITY_KEYS_URL="http://${k1_node_address}:${IDENTITY_KEYS_NODE_PORT}"
    K2_IDENTITY_KEYS_URL="http://${k2_node_address}:${IDENTITY_KEYS_NODE_PORT}"
}

event_bus_access_token() {
    local client_id="$1" secret_name="$2" client_secret
    client_secret=$(kubectl get secret "${secret_name}" -n "${IDENTITY_NAMESPACE}" \
        --context "${K1_CONTEXT}" -o json | jq -er '.data["client-secret"]' | base64 -d)
    external_curl -fsS --retry 30 --retry-all-errors --retry-connrefused \
        --retry-delay 1 --retry-max-time 30 --connect-timeout 1 --max-time 10 \
        --data-urlencode grant_type=client_credentials \
        --data-urlencode scope=mqtt \
        --data-urlencode "client_id=${client_id}" \
        --data-urlencode "client_secret=${client_secret}" \
        "${EVENT_BUS_OAUTH_URL}" | jq -er '.access_token'
}

assert_mqtt_authorization() {
    local access_token observer_token
    access_token=$(event_bus_access_token dsx-identity-keys dsx-identity-keys-mqtt-oauth)
    observer_token=$(event_bus_access_token event-bus-e2e-observer event-bus-e2e-observer-oauth)
    external_probe '^TestLiveMQTTAuthorization$' \
        "DSX_E2E_MQTT_BROKER=${MQTT_BROKER}" \
        "DSX_E2E_MQTT_TOKEN=${access_token}" \
        "DSX_E2E_MQTT_OBSERVER_TOKEN=${observer_token}"
}

assert_retained_keys() {
    local shard_id="$1"
    local jwks="$2"
    local access_token expected_jwks output deadline=$((SECONDS + 60))
    access_token=$(event_bus_access_token dsx-identity-keys dsx-identity-keys-mqtt-oauth)
    expected_jwks=$(jq -c '{keys: [.keys[] | del(.d)]}' <<<"${jwks}")
    while ((SECONDS < deadline)); do
        if output=$(external_probe '^TestLiveMQTTRetainedReplay$' \
            "DSX_E2E_MQTT_BROKER=${MQTT_BROKER}" \
            "DSX_E2E_MQTT_TOKEN=${access_token}" \
            "DSX_E2E_MQTT_SHARD_ID=${shard_id}" \
            "DSX_E2E_MQTT_EXPECTED_JWKS=${expected_jwks}" 2>&1); then
            printf '%s\n' "${output}"
            return 0
        fi
    done
    printf '%s\n' "${output}" >&2
    return 1
}

read_pod_token() {
    local context="$1"
    local pod="$2"
    local container="$3"
    local token="" deadline=$((SECONDS + 180))
    while ((SECONDS < deadline)); do
        token=$(kubectl exec "${pod}" -c "${container}" --namespace "${FIXTURE_NAMESPACE}" \
            --context "${context}" -- cat /run/secrets/nvidia.com/dsx/token 2>/dev/null) && {
            printf '%s' "${token}"
            return 0
        }
    done
    return 1
}

assert_application_token_mount() {
    local context="$1"
    local pod="$2"
    local allow_probe_socket="${3:-false}"
    local container
    while IFS= read -r container; do
        if [[ "${allow_probe_socket}" == "true" && "${container}" == "${IDENTITY_PROBE_CONTAINER}" ]]; then
            continue
        fi
        if kubectl exec "${pod}" -c "${container}" --namespace "${FIXTURE_NAMESPACE}" \
            --context "${context}" -- test -e /var/run/secrets/spire.io/spire-agent.sock \
            2>/dev/null; then
            log_error "${context} application container ${container} received the Workload API socket"
            return 1
        fi
    done < <(kubectl get pod "${pod}" --namespace "${FIXTURE_NAMESPACE}" \
        --context "${context}" -o json | jq -r '.spec.containers[].name')
    if kubectl exec "${pod}" -c nginx --namespace "${FIXTURE_NAMESPACE}" \
        --context "${context}" -- touch /run/secrets/nvidia.com/dsx/application-write-test 2>/dev/null; then
        log_error "${context} application token mount is writable"
        return 1
    fi
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
    local namespace="${3:-${FIXTURE_NAMESPACE}}"
    local tenant="${4:-}"
    local node uid role
    [[ -n "${tenant}" ]] || tenant="${OPERATOR_TENANT}"
    node=$(kubectl get pod "${pod}" --namespace "${namespace}" \
        --context "${context}" -o jsonpath='{.spec.nodeName}')
    uid=$(kubectl get pod "${pod}" --namespace "${namespace}" \
        --context "${context}" -o jsonpath='{.metadata.uid}')
    role=$(kubectl get pod "${pod}" --namespace "${namespace}" \
        --context "${context}" -o jsonpath='{.metadata.labels.spiffe\.nvidia\.com/role}')
    printf 'spiffe://%s.ds9.dsx.nvid.id/host/%s/pod/%s' "${tenant}" "${node}" "${uid}"
    [[ -z "${role}" ]] || printf '/role/%s' "${role}"
}

internal_pod_subject() {
    local context="$1"
    local trust_domain="$2"
    local pod="$3"
    local namespace="${4:-${FIXTURE_NAMESPACE}}"
    local tenant="${5:-}"
    local node uid role
    [[ -n "${tenant}" ]] || tenant="${OPERATOR_TENANT}"
    node=$(kubectl get pod "${pod}" --namespace "${namespace}" \
        --context "${context}" -o jsonpath='{.spec.nodeName}')
    uid=$(kubectl get pod "${pod}" --namespace "${namespace}" \
        --context "${context}" -o jsonpath='{.metadata.uid}')
    role=$(kubectl get pod "${pod}" --namespace "${namespace}" \
        --context "${context}" -o jsonpath='{.metadata.labels.spiffe\.nvidia\.com/role}')
    printf 'spiffe://%s/tenant/%s/service/dsx/host/%s/pod/%s' "${trust_domain}" "${tenant}" "${node}" "${uid}"
    [[ -z "${role}" ]] || printf '/role/%s' "${role}"
}

fetch_spire_jwt_svid() {
    local context="$1"
    local pod="$2"
    local expected_id="$3"
    local audience="$4"
    local hint="$5"
    local namespace="${6:-${FIXTURE_NAMESPACE}}"
    local output token deadline=$((SECONDS + 180))
    while ((SECONDS < deadline)); do
        output=$(kubectl exec "${pod}" -c "${IDENTITY_PROBE_CONTAINER}" --namespace "${namespace}" \
            --context "${context}" -- env \
            DSX_E2E_SPIFFE_ENDPOINT_SOCKET=unix:///var/run/secrets/spire.io/spire-agent.sock \
            DSX_E2E_SPIRE_JWT_AUDIENCE="${audience}" \
            DSX_E2E_SPIRE_JWT_ID="${expected_id}" \
            DSX_E2E_SPIRE_JWT_HINT="${hint}" \
            /usr/local/bin/spire-svid-probe -test.v -test.run '^TestLiveFetchSPIREJWTSVID$' \
            2>/dev/null) || continue
        token=$(awk -F= '/^SPIRE_JWT=/{sub(/^SPIRE_JWT=/, ""); print; exit}' <<<"${output}")
        if [[ "${token}" == *.*.* ]]; then
            printf '%s' "${token}"
            return 0
        fi
    done
    log_error "SPIRE Workload API did not return a JWT-SVID for ${expected_id}"
    return 1
}

exchange_spire_svid() {
    local identity_keys_url="$1"
    local token="$2"
    external_curl -fsS --max-time 10 -X POST \
        -H 'Content-Type: application/x-www-form-urlencoded' \
        --data-urlencode 'grant_type=urn:ietf:params:oauth:grant-type:token-exchange' \
        --data-urlencode "subject_token=${token}" \
        --data-urlencode 'subject_token_type=urn:ietf:params:oauth:token-type:jwt' \
        "${identity_keys_url}/v1/exchange" | jq -er '.access_token'
}

assert_exchange_rejects() {
    local name="$1"
    local identity_keys_url="$2"
    local token="$3"
    local output status body
    output=$(external_curl -sS --max-time 10 -w $'\n%{http_code}' -X POST \
        -H 'Content-Type: application/x-www-form-urlencoded' \
        --data-urlencode 'grant_type=urn:ietf:params:oauth:grant-type:token-exchange' \
        --data-urlencode "subject_token=${token}" \
        --data-urlencode 'subject_token_type=urn:ietf:params:oauth:token-type:jwt' \
        "${identity_keys_url}/v1/exchange") || return 1
    status=${output##*$'\n'}
    body=${output%$'\n'*}
    if [[ "${status}" != "401" ]] || ! jq -e '.error == "invalid_grant"' <<<"${body}" >/dev/null; then
        log_error "${name} was not rejected as invalid_grant (HTTP ${status})"
        return 1
    fi
    log_success "${name} was rejected"
}

assert_pod_registration_parent() {
    local context="$1" trust_domain="$2" pod="$3" namespace="$4" tenant="$5"
    local spiffe_id node uid expected_parent entries deadline=$((SECONDS + 180))
    node=$(kubectl get pod "${pod}" --namespace "${namespace}" --context "${context}" \
        -o jsonpath='{.spec.nodeName}')
    uid=$(kubectl get pod "${pod}" --namespace "${namespace}" --context "${context}" \
        -o jsonpath='{.metadata.uid}')
    spiffe_id=$(internal_pod_subject "${context}" "${trust_domain}" "${pod}" "${namespace}" "${tenant}")
    expected_parent="spiffe://${trust_domain}/spire/agent/dsx_imds/tenant/${tenant}/host/${node}"
    while ((SECONDS < deadline)); do
        entries=$(kubectl exec statefulset/dsx-identity-server --namespace "${IDENTITY_NAMESPACE}" \
            --context "${context}" -c spire-server -- \
            /opt/spire/bin/spire-server entry show -spiffeID "${spiffe_id}" -output json 2>/dev/null) || continue
        if jq -e --arg parent "${expected_parent}" --arg uid "${uid}" '
            .entries | length == 1 and
            ("spiffe://" + .[0].parent_id.trust_domain + .[0].parent_id.path) == $parent and
            .[0].hint == "dsx-pod" and
            ([.[0].selectors[] | .type + ":" + .value] | index("k8s:pod-uid:" + $uid)) != null
        ' <<<"${entries}" >/dev/null; then
            return 0
        fi
    done
    log_error "${context} Pod registration did not use exact dsx_imds parent ${expected_parent}"
    [[ -z "${entries:-}" ]] || jq . <<<"${entries}" >&2
    return 1
}

assert_relying_party_subject_validation() {
    local valid_token="$1"
    external_probe '^TestLiveRelyingPartyRejectsMalformedSubject$' \
        "DSX_E2E_RELYING_PARTY_URL=${OPENBAO_URL}/v1/auth/jwt/cel/login" \
        "DSX_E2E_SIGNING_JWKS=${K1_ORIGINAL_JWKS}" \
        "DSX_E2E_ACTIVE_KID=${K1_ORIGINAL_KID}" \
        "DSX_E2E_VALID_TOKEN=${valid_token}"
    log_success "OpenBao rejected a signed token outside the DSX subject grammar"
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

verify_tokens_with_jwks() {
    local jwks="$1"
    shift
    local tokens='[]' token
    for token in "$@"; do
        tokens=$(jq -cn --argjson tokens "${tokens}" --arg token "${token}" '$tokens + [$token]')
    done
    external_probe '^TestLiveAggregateVerifiesTokens$' \
        "DSX_E2E_VERIFY_JWKS=${jwks}" \
        "DSX_E2E_VERIFY_TOKENS=${tokens}"
}

assert_full_aggregates() {
    local required="$1"
    shift
    local k1_jwks k2_jwks deadline=$((SECONDS + 60))
    if ! jq -e '(.keys | map(.kid) | length) == (.keys | map(.kid) | unique | length)' \
        <<<"${required}" >/dev/null; then
        log_error "Required aggregate contains a duplicate kid"
        return 1
    fi
    while ((SECONDS < deadline)); do
        k1_jwks=$(external_curl -fsS --max-time 5 \
            "${K1_IDENTITY_KEYS_URL}/.well-known/jwks.json" 2>/dev/null) || continue
        k2_jwks=$(external_curl -fsS --max-time 5 \
            "${K2_IDENTITY_KEYS_URL}/.well-known/jwks.json" 2>/dev/null) || continue
        if jq -en --argjson k1 "${k1_jwks}" --argjson k2 "${k2_jwks}" \
            --argjson required "${required}" '
              def valid:
                (.keys | type == "array") and
                (.keys | all(has("d") | not)) and
                ((.keys | map(.kid) | length) == (.keys | map(.kid) | unique | length));
              ($k1 | valid) and
              ($k2 | valid) and
              (($k1.keys | sort_by(.kid)) == ($k2.keys | sort_by(.kid))) and
              ($required.keys | all(. as $want | $k1.keys | any(. == $want)))
            ' >/dev/null; then
            verify_tokens_with_jwks "${k1_jwks}" "$@"
            verify_tokens_with_jwks "${k2_jwks}" "$@"
            log_success "Both DSX Identity Keys instances serve the complete aggregate and verify every required shard token"
            return 0
        fi
    done
    log_error "DSX Identity Keys aggregates did not converge with every required key"
    return 1
}

main() {
    local script_started=${SECONDS} setup_started=${SECONDS} stage_started
    local k1_token k2_token k1_subject k2_subject k2_spire_subject k1_spire_id k2_spire_id
    local k1_spire_svid k2_spire_svid
    local local_k1_token local_k2_token expected

    validate_context_exists "${K1_CONTEXT}"
    validate_context_exists "${K2_CONTEXT}"
    cd "${PROJECT_ROOT}"
    initialize_external_test_process

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
    log_timing "setup" "${setup_started}"

    stage_started=${SECONDS}
    k1_token=$(read_pod_token "${K1_CONTEXT}" "${K1_POD}" "${IDENTITY_PROBE_CONTAINER}")
    k2_token=$(read_pod_token "${K2_CONTEXT}" "${K2_POD}" "${IDENTITY_PROBE_CONTAINER}")
    assert_application_token_mount "${K1_CONTEXT}" "${K1_POD}" true
    assert_application_token_mount "${K2_CONTEXT}" "${K2_POD}"
    k1_subject=$(pod_subject "${K1_CONTEXT}" "${K1_POD}")
    k2_subject=$(pod_subject "${K2_CONTEXT}" "${K2_POD}")
    assert_token_contract k1 "${k1_token}" "${k1_subject}"
    assert_token_contract k2 "${k2_token}" "${k2_subject}"
    assert_pod_registration_parent "${K1_CONTEXT}" "${K1_TRUST_DOMAIN}" \
        "${K1_POD}" "${FIXTURE_NAMESPACE}" operator-1
    assert_pod_registration_parent "${K2_CONTEXT}" "${K2_TRUST_DOMAIN}" \
        "${K2_POD}" "${FIXTURE_NAMESPACE}" operator-1
    [[ "$(token_kid "${k1_token}")" == "${K1_ORIGINAL_KID}" ]] || {
        log_error "k1 Pod did not use the k1 DSX Identity Keys signing key"
        return 1
    }
    [[ "$(token_kid "${k2_token}")" == "${K2_ORIGINAL_KID}" ]] || {
        log_error "k2 Pod did not use the k2 DSX Identity Keys signing key"
        return 1
    }
    log_timing "Pod token delivery and registration" "${stage_started}"

    stage_started=${SECONDS}
    k1_spire_id=$(internal_pod_subject "${K1_CONTEXT}" "${K1_TRUST_DOMAIN}" "${K1_POD}")
    k2_spire_id=$(internal_pod_subject \
        "${K2_CONTEXT}" "${K2_TRUST_DOMAIN}" "${K2_SPIRE_POD}" "${K2_SPIRE_NAMESPACE}")
    k2_spire_subject=$(pod_subject "${K2_CONTEXT}" "${K2_SPIRE_POD}" "${K2_SPIRE_NAMESPACE}")
    k1_spire_svid=$(fetch_spire_jwt_svid "${K1_CONTEXT}" "${K1_POD}" "${k1_spire_id}" "${AUDIENCE}" dsx-pod)
    k2_spire_svid=$(fetch_spire_jwt_svid \
        "${K2_CONTEXT}" "${K2_SPIRE_POD}" "${k2_spire_id}" "${AUDIENCE}" dsx-pod \
        "${K2_SPIRE_NAMESPACE}")
    local_k1_token=$(exchange_spire_svid "${K1_IDENTITY_KEYS_URL}" "${k1_spire_svid}")
    local_k2_token=$(exchange_spire_svid "${K2_IDENTITY_KEYS_URL}" "${k2_spire_svid}")
    assert_token_contract "k1 SPIRE JWT-SVID through k1 DSX Identity Keys" "${local_k1_token}" "${k1_subject}"
    assert_token_contract "k2 SPIRE JWT-SVID through k2 DSX Identity Keys" "${local_k2_token}" "${k2_spire_subject}"
    [[ "$(token_kid "${local_k1_token}")" == "${K1_ORIGINAL_KID}" ]] || return 1
    [[ "$(token_kid "${local_k2_token}")" == "${K2_ORIGINAL_KID}" ]] || return 1
    assert_exchange_rejects "k2 SPIRE JWT-SVID at k1 DSX Identity Keys" \
        "${K1_IDENTITY_KEYS_URL}" "${k2_spire_svid}"
    assert_exchange_rejects "k1 SPIRE JWT-SVID at k2 DSX Identity Keys" \
        "${K2_IDENTITY_KEYS_URL}" "${k1_spire_svid}"
    log_timing "local exchange and shard rejection" "${stage_started}"

    stage_started=${SECONDS}
    expected=$(aggregate_keysets "${K1_ORIGINAL_JWKS}" "${K2_ORIGINAL_JWKS}")
    assert_mqtt_authorization
    assert_retained_keys k1 "${K1_ORIGINAL_JWKS}"
    assert_retained_keys k2 "${K2_ORIGINAL_JWKS}"
    assert_full_aggregates "${expected}" "${k1_token}" "${k2_token}" "${local_k1_token}" "${local_k2_token}"
    log_timing "MQTT authorization and aggregate JWKS" "${stage_started}"

    stage_started=${SECONDS}
    assert_relying_party_subject_validation "${k1_token}"
    log_timing "OpenBao subject grammar" "${stage_started}"
    log_timing "cross-shard verification total" "${script_started}"
    log_success "Cross-shard acceptance checks passed"
}

trap 'exit 130' INT
trap 'exit 143' TERM
main "$@"

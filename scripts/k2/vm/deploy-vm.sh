#!/bin/bash
# deploy-vm.sh - Deploys Ubuntu VM to k2 cluster

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="k2"
CONTEXT="kind-${CLUSTER_NAME}"
VM_NAME="${VM_NAME:-e2e-ubuntu-vm}"
VM_TENANT="${VM_TENANT:-tenant1}"
NAMESPACE="tenant-${VM_TENANT}"
TENANT_SUBNET_OCTET="${TENANT_SUBNET_OCTET:-1}"
VM_IMAGE="quay.io/containerdisks/ubuntu:24.04"
SSH_CLIENT_IMAGE="nicolaka/netshoot:v0.16"
SSH_SECRET="${VM_NAME}-e2e-ssh"
LAUNCH_LAYER_SOURCE="e2e-vm-source-${VM_NAME}"
DPU_NAME="e2e-controller-dpu"
HOST_SOURCE_NAME="e2e-controller-host-source"
KEY_DIR=""

source "${PROJECT_ROOT}/scripts/common.sh"

cleanup_temp() {
    if [[ -n "${KEY_DIR}" ]]; then
        rm -rf "${KEY_DIR}"
        KEY_DIR=""
    fi
}

preload_images() {
    log_info "Preloading VM images into cluster..."
    if ! "${PROJECT_ROOT}/scripts/kind-load-image.sh" "${VM_IMAGE}" "${CLUSTER_NAME}"; then
        log_error "Failed to load image: ${VM_IMAGE}"
        return 1
    fi
    if ! "${PROJECT_ROOT}/scripts/kind-load-image.sh" "${SSH_CLIENT_IMAGE}" "${CLUSTER_NAME}"; then
        log_error "Failed to load image: ${SSH_CLIENT_IMAGE}"
        return 1
    fi
    log_success "Images preloaded"
}

deploy_tenant_namespace() {
    local namespace

    namespace=$(<"${SCRIPT_DIR}/tenant-namespace.yaml.tmpl")
    namespace="${namespace//TENANT_PLACEHOLDER/$VM_TENANT}"
    kubectl apply -f - --context "${CONTEXT}" <<<"${namespace}"
}

ensure_ssh_key() {
    if kubectl get secret "${SSH_SECRET}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" >/dev/null 2>&1; then
        return 0
    fi
    KEY_DIR=$(mktemp -d)
    docker run --rm "${SSH_CLIENT_IMAGE}" \
        /bin/sh -ec '
          key_dir=$(mktemp -d)
          ssh-keygen -q -t ed25519 -N "" -C dsx-identity-e2e -f "${key_dir}/id_ed25519"
          tar -C "${key_dir}" -cf - id_ed25519 id_ed25519.pub
        ' | tar -C "${KEY_DIR}" -xf -
    kubectl create secret generic "${SSH_SECRET}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" \
        --from-file="${KEY_DIR}/id_ed25519" \
        --from-file="${KEY_DIR}/id_ed25519.pub" \
        --dry-run=client -o yaml | kubectl apply -f - --context "${CONTEXT}" >/dev/null
    cleanup_temp
}

deploy_vm_resources() {
    log_info "Deploying VM resources for '${VM_NAME}'..."

    local client config controller_sources resources ssh_public_key udn
    udn=$(<"${SCRIPT_DIR}/tenant-udn.yaml.tmpl")
    udn="${udn//TENANT_PLACEHOLDER/$VM_TENANT}"
    udn="${udn//TENANT_SUBNET_OCTET/$TENANT_SUBNET_OCTET}"

    controller_sources=$(<"${SCRIPT_DIR}/e2e-controller-sources.yaml.tmpl")
    controller_sources="${controller_sources//DPU_NAME_PLACEHOLDER/$DPU_NAME}"
    controller_sources="${controller_sources//HOST_SOURCE_NAME_PLACEHOLDER/$HOST_SOURCE_NAME}"
    controller_sources="${controller_sources//TENANT_NAMESPACE_PLACEHOLDER/$NAMESPACE}"

    client=$(<"${SCRIPT_DIR}/imds-client.yaml.tmpl")
    client="${client//VM_NAME_PLACEHOLDER/$VM_NAME}"
    client="${client//TENANT_PLACEHOLDER/$VM_TENANT}"
    client="${client//SSH_CLIENT_IMAGE_PLACEHOLDER/$SSH_CLIENT_IMAGE}"

    config=$(<"${SCRIPT_DIR}/vm-template.yaml.tmpl")
    config="${config//VM_NAME_PLACEHOLDER/$VM_NAME}"
    config="${config//TENANT_PLACEHOLDER/$VM_TENANT}"
    ssh_public_key=$(kubectl get secret "${SSH_SECRET}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" -o json | \
        jq -er '.data["id_ed25519.pub"]' | base64 -d)
    config="${config//SSH_PUBLIC_KEY_PLACEHOLDER/$ssh_public_key}"

    # Submit all VM topology resources together. Nothing is derived from the running VM.
    resources="${udn}"$'\n---\n'"${controller_sources}"$'\n---\n'"${client}"$'\n---\n'"${config}"
    kubectl apply -n "${NAMESPACE}" -f - --context "${CONTEXT}" <<<"${resources}"
    log_success "VM resources deployed"
}

wait_for_resources() {
    local source_uid
    kubectl wait --for=condition=NetworkCreated \
        "userdefinednetwork/tenant-${VM_TENANT}-network" \
        -n "${NAMESPACE}" --timeout=60s --context "${CONTEXT}"
    source_uid=$(kubectl get launchlayersourcefixture "${LAUNCH_LAYER_SOURCE}" \
        -n "${NAMESPACE}" --context "${CONTEXT}" -o jsonpath='{.metadata.uid}')
    kubectl wait --for=create clusterstaticentry \
        -l "identity.dsx.nvidia.com/source-uid=${source_uid}" \
        --timeout=120s --context "${CONTEXT}" >/dev/null
    kubectl wait --for=condition=Ready "pod/${VM_NAME}-imds-client" \
        -n "${NAMESPACE}" --timeout=120s --context "${CONTEXT}" >/dev/null
    log_info "Waiting for VM to start..."
    if ! kubectl wait --for=condition=Ready "vm/${VM_NAME}" \
            -n "${NAMESPACE}" --timeout=300s --context "${CONTEXT}"; then
        log_error "VM did not become ready"
        kubectl get vm,vmi,pod -n "${NAMESPACE}" -o wide --context "${CONTEXT}" || true
        kubectl describe vm "${VM_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}" || true
        kubectl describe vmi "${VM_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}" || true
        kubectl describe pod -l kubevirt.io/domain="${VM_NAME}" \
            -n "${NAMESPACE}" --context "${CONTEXT}" || true
        kubectl logs -l kubevirt.io/domain="${VM_NAME}" --all-containers=true \
            --prefix --tail=100 -n "${NAMESPACE}" --context "${CONTEXT}" || true
        return 1
    fi
    log_success "VM and identity resources are ready"
}

display_info() {
    log_success "VM deployment complete"
    echo
    echo "Virtual Machine:"
    kubectl get vm "${VM_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}"
    echo
    echo "Virtual Machine Instance:"
    kubectl get vmi "${VM_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}" 2>/dev/null || echo "VMI not ready"
}

cleanup() {
    log_info "Removing VM..."

    kubectl delete vm "${VM_NAME}" -n "${NAMESPACE}" --context "${CONTEXT}" \
        --ignore-not-found=true

    kubectl delete launchlayersourcefixture "${LAUNCH_LAYER_SOURCE}" -n "${NAMESPACE}" \
        --context "${CONTEXT}" --ignore-not-found=true --wait=true --timeout=60s

    kubectl delete userdefinednetwork "tenant-${VM_TENANT}-network" -n "${NAMESPACE}" \
        --context "${CONTEXT}" --ignore-not-found=true
    kubectl delete namespace "${NAMESPACE}" --context "${CONTEXT}" --ignore-not-found=true

    log_success "VM removed"
}

main() {
    case "${1:-}" in
        cleanup)
            cleanup
            ;;
        *)
            validate_context_exists "${CONTEXT}"
            preload_images
            deploy_tenant_namespace
            ensure_ssh_key
            deploy_vm_resources
            wait_for_resources
            display_info
            ;;
    esac
}

trap cleanup_temp EXIT
trap 'log_error "Script interrupted"; exit 1' INT TERM
main "$@"

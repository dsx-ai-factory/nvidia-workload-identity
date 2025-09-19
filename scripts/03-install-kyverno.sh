#!/bin/bash

# 03-install-kyverno.sh
# Installs Kyverno policy engine using Helm

set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KYVERNO_NAMESPACE="${KYVERNO_NAMESPACE:-kyverno}"
KYVERNO_CHART_VERSION="${KYVERNO_CHART_VERSION:-}"

# Source common functions
source "${SCRIPT_DIR}/common.sh"

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."
    
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed. Please install kubectl first."
        exit 1
    fi
    
    if ! command -v helm &> /dev/null; then
        log_error "helm is not installed. Please install helm first."
        log_info "Installation: https://helm.sh/docs/intro/install/"
        exit 1
    fi
    
    # Check if cluster is accessible
    if ! kubectl cluster-info &> /dev/null; then
        log_error "Cannot access Kubernetes cluster. Please ensure cluster is running."
        exit 1
    fi
    
    log_success "All prerequisites are met"
}

# Add Kyverno Helm repository
add_helm_repo() {
    log_info "Adding Kyverno Helm repository..."
    
    # Add the repository
    helm repo add kyverno https://kyverno.github.io/kyverno/
    
    # Update repository
    log_info "Updating Helm repositories..."
    helm repo update
    
    log_success "Kyverno Helm repository added and updated"
}

# Install Kyverno
install_kyverno() {
    log_info "Installing Kyverno..."
    
    # Prepare helm install command
    local helm_cmd="helm install kyverno kyverno/kyverno --namespace ${KYVERNO_NAMESPACE} --create-namespace"
    
    # Add version if specified
    if [[ -n "${KYVERNO_CHART_VERSION}" ]]; then
        helm_cmd="${helm_cmd} --version ${KYVERNO_CHART_VERSION}"
        log_info "Using Kyverno chart version: ${KYVERNO_CHART_VERSION}"
    else
        log_info "Using latest Kyverno chart version"
    fi
    
    # Install Kyverno
    log_info "Installing Kyverno in namespace '${KYVERNO_NAMESPACE}'..."
    eval "${helm_cmd}"
    
    # Wait for Kyverno to be ready
    log_info "Waiting for Kyverno pods to be ready..."
    kubectl wait --for=condition=Ready pods --all -n ${KYVERNO_NAMESPACE} --timeout=300s
    
    log_success "Kyverno installed successfully"
}

# Verify Kyverno installation
verify_kyverno() {
    log_info "Verifying Kyverno installation..."
    
    # Check if Kyverno pods are running
    log_info "Checking Kyverno pod status..."
    kubectl get pods -n ${KYVERNO_NAMESPACE}
    
    # Check Kyverno admission controller
    log_info "Checking Kyverno admission controller..."
    kubectl get validatingadmissionpolicies.admissionregistration.k8s.io 2>/dev/null || log_info "No ValidatingAdmissionPolicies found (expected for older K8s versions)"
    kubectl get mutatingwebhookconfigurations | grep kyverno || log_warning "Kyverno mutating webhooks not found"
    kubectl get validatingwebhookconfigurations | grep kyverno || log_warning "Kyverno validating webhooks not found"
    
    log_success "Kyverno verification completed"
}

# Display installation status
display_status() {
    log_success "Kyverno installation complete!"
    echo
    echo "Installation Status:"
    echo "==================="
    
    # Get Kyverno release info
    echo "Helm Release:"
    helm list -n ${KYVERNO_NAMESPACE}
    
    echo
    echo "Pod Status:"
    echo "==========="
    kubectl get pods -n ${KYVERNO_NAMESPACE}
    
    echo
    echo "Webhook Configurations:"
    echo "======================"
    echo "Mutating Webhooks:"
    kubectl get mutatingwebhookconfigurations | grep kyverno | wc -l | xargs echo "  Count:"
    echo "Validating Webhooks:"
    kubectl get validatingwebhookconfigurations | grep kyverno | wc -l | xargs echo "  Count:"
    
    echo
    echo "Next Steps:"
    echo "==========="
    echo "1. Run 'scripts/04-install-spire-helm.sh' to install SPIRE"
    echo "2. Create Kyverno policies for SPIFFE sidecar injection"
    echo "3. Kyverno documentation: https://kyverno.io/docs/"
}

# Cleanup function
cleanup_kyverno() {
    log_info "Cleaning up Kyverno..."
    
    # Uninstall Kyverno Helm release
    if helm list -n ${KYVERNO_NAMESPACE} | grep -q kyverno; then
        log_info "Uninstalling Kyverno Helm release..."
        helm uninstall kyverno -n ${KYVERNO_NAMESPACE}
    fi
    
    # Delete namespace if it exists and is empty
    if kubectl get namespace ${KYVERNO_NAMESPACE} &> /dev/null; then
        log_info "Deleting Kyverno namespace..."
        kubectl delete namespace ${KYVERNO_NAMESPACE} --timeout=60s || log_warning "Failed to delete namespace (may contain other resources)"
    fi
    
    # Clean up webhook configurations (in case they weren't cleaned up automatically)
    log_info "Cleaning up webhook configurations..."
    kubectl delete mutatingwebhookconfigurations -l app.kubernetes.io/name=kyverno 2>/dev/null || true
    kubectl delete validatingwebhookconfigurations -l app.kubernetes.io/name=kyverno 2>/dev/null || true
    
    log_success "Kyverno cleanup completed!"
}

# Display usage
usage() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Installs Kyverno policy engine using Helm"
    echo ""
    echo "Options:"
    echo "  cleanup    Remove Kyverno from the cluster"
    echo "  help       Show this help message"
    echo ""
    echo "Environment Variables:"
    echo "  KYVERNO_NAMESPACE      Namespace for Kyverno (default: kyverno)"
    echo "  KYVERNO_CHART_VERSION  Specific chart version to install (default: latest)"
    echo ""
    echo "Prerequisites:"
    echo "  - Kubernetes cluster must be running and accessible"
    echo "  - kubectl must be configured to access the cluster"
    echo "  - Helm 3.x must be installed"
    echo ""
    echo "Features:"
    echo "  - Kyverno policy engine for Kubernetes governance"
    echo "  - Admission control for policy enforcement"
    echo "  - Support for mutation and validation policies"
    echo "  - Required for SPIFFE sidecar injection policies"
}

# Main execution
main() {
    case "${1:-}" in
        "cleanup")
            cleanup_kyverno
            ;;
        "help")
            usage
            ;;
        "")
            log_info "Starting Kyverno installation..."
            check_prerequisites
            add_helm_repo
            install_kyverno
            verify_kyverno
            display_status
            ;;
        *)
            echo "Error: Unknown option '$1'"
            echo ""
            usage
            exit 1
            ;;
    esac
}

# Handle script interruption
trap 'log_error "Script interrupted"; exit 1' INT TERM

# Run main function
main "$@"

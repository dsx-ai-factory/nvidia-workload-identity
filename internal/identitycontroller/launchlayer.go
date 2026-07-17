package identitycontroller

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	spirev1alpha1 "github.com/spiffe/spire-controller-manager/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	"dsx-identity/internal/identity"
)

// LaunchLayerSourceGVK identifies the temporary source contract used until
// LaunchLayer publishes its final API.
var LaunchLayerSourceGVK = schema.GroupVersionKind{
	Group:   "e2e.dsx-identity.nvidia.com",
	Version: "v1alpha1",
	Kind:    "LaunchLayerSourceFixture",
}

// LaunchLayerReconciler maps each read-only source to one entry. A host source
// produces a host SPIRE ID with parent /spire/agent/dsx_dpu/<parentID>, hint
// dsx-imds, and configured DPU metadata selectors. A VM source produces a VM
// SPIRE ID with its optional role, parent
// /spire/agent/dsx_imds/tenant/<operator-tenant>/host/<parentID>, hint
// dsx-imds-vm, and source-provided holder selectors.
type LaunchLayerReconciler struct {
	client.Client
	Config   Config
	Recorder record.EventRecorder
}

func (r *LaunchLayerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	source := &unstructured.Unstructured{}
	source.SetGroupVersionKind(LaunchLayerSourceGVK)
	return ctrl.NewControllerManagedBy(mgr).
		Named("launch-layer-source").
		For(source).
		Watches(&spirev1alpha1.ClusterStaticEntry{}, handler.EnqueueRequestsFromMapFunc(requestForSource(LaunchLayerSourceGVK.Group, LaunchLayerSourceGVK.Kind))).
		Complete(r)
}

func (r *LaunchLayerReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	return reconcileSource(ctx, r.Client, r.Recorder, request, LaunchLayerSourceGVK, r.desiredEntry)
}

func (r *LaunchLayerReconciler) desiredEntry(source *unstructured.Unstructured) (*spirev1alpha1.ClusterStaticEntry, error) {
	td, err := r.Config.trustDomain()
	if err != nil {
		return nil, err
	}
	spec, err := launchLayerSourceSpec(source)
	if err != nil {
		return nil, err
	}
	spiffeID, err := (identity.Subject{
		Tenant:       spec.TenantID,
		ResourceType: spec.Type,
		ResourceID:   spec.ResourceID,
		Role:         spec.Role,
	}).SPIFFEID(td)
	if err != nil {
		return nil, err
	}
	parentID, hint, err := spec.parentAgent(td, r.Config.OperatorTenant)
	if err != nil {
		return nil, err
	}
	var selectors []string
	switch spec.Type {
	case identity.ResourceHost:
		selectors = slices.Clone(r.Config.DPUMetadataSelectors)
	case identity.ResourceVM:
		selectors, err = spec.Holder.selectors()
		if err != nil {
			return nil, err
		}
	}
	return newEntry(source, spiffeID, parentID, hint, r.Config.ClassName, selectors)
}

// launchLayerSpec contains derivation inputs only. Complete SPIFFE IDs remain
// controller-owned outputs.
type launchLayerSpec struct {
	Type       identity.ResourceType `json:"type"`
	TenantID   string                `json:"tenantID"`
	ResourceID string                `json:"resourceID"`
	Role       string                `json:"role,omitempty"`
	ParentID   string                `json:"parentID"`
	Holder     Holder                `json:"holder,omitempty"`
}

// parentAgent uses the configured operator tenant for a VM's Kubernetes worker
// agent. The source tenant belongs only to the VM identity.
func (s launchLayerSpec) parentAgent(td spiffeid.TrustDomain, operatorTenant string) (spiffeid.ID, string, error) {
	if !identity.ValidResourceID(s.ParentID) {
		return spiffeid.ID{}, "", fmt.Errorf("invalid parent resource ID %q", s.ParentID)
	}

	switch s.Type {
	case identity.ResourceHost:
		if s.Role != "" {
			return spiffeid.ID{}, "", errors.New("role is only valid for VM LaunchLayer source entries")
		}
		if !s.Holder.empty() {
			return spiffeid.ID{}, "", errors.New("holder is only valid for VM LaunchLayer source entries")
		}
		parentID, err := spiffeid.FromSegments(td, "spire", "agent", "dsx_dpu", s.ParentID)
		if err != nil {
			return spiffeid.ID{}, "", fmt.Errorf("DPU parent ID: %w", err)
		}
		return parentID, "dsx-imds", nil

	case identity.ResourceVM:
		if s.Holder.empty() {
			return spiffeid.ID{}, "", errors.New("holder is required for VM LaunchLayer source entries")
		}
		parentID, err := spiffeid.FromSegments(td,
			"spire", "agent", "dsx_imds", "tenant", operatorTenant, "host", s.ParentID)
		if err != nil {
			return spiffeid.ID{}, "", fmt.Errorf("host parent ID: %w", err)
		}
		return parentID, "dsx-imds-vm", nil

	default:
		return spiffeid.ID{}, "", fmt.Errorf("unsupported LaunchLayer source resource type %q", s.Type)
	}
}

func launchLayerSourceSpec(source *unstructured.Unstructured) (launchLayerSpec, error) {
	raw, found, err := unstructured.NestedMap(source.Object, "spec")
	if err != nil {
		return launchLayerSpec{}, fmt.Errorf("spec: %w", err)
	}
	if !found {
		return launchLayerSpec{}, errors.New("spec is required")
	}
	var spec launchLayerSpec
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &spec); err != nil {
		return launchLayerSpec{}, fmt.Errorf("spec: %w", err)
	}
	for _, field := range []struct{ name, value string }{
		{"type", string(spec.Type)},
		{"tenantID", spec.TenantID},
		{"resourceID", spec.ResourceID},
		{"parentID", spec.ParentID},
	} {
		if field.value == "" {
			return launchLayerSpec{}, fmt.Errorf("spec.%s is required", field.name)
		}
	}
	if spec.Type == identity.ResourceVM {
		for _, field := range []struct{ name, value string }{
			{"holder.namespace", spec.Holder.Namespace},
			{"holder.serviceAccount", spec.Holder.ServiceAccount},
		} {
			if field.value == "" {
				return launchLayerSpec{}, fmt.Errorf("spec.%s is required", field.name)
			}
		}
	}
	return spec, nil
}

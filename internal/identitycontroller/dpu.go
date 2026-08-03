package identitycontroller

import (
	"context"
	"fmt"
	"slices"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	spirev1alpha1 "github.com/spiffe/spire-controller-manager/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	dpuidentity "dsx-identity/internal/dpu"
	"dsx-identity/internal/identity"
)

// DPUGVK identifies the externally owned hardware inventory source.
var DPUGVK = schema.GroupVersionKind{
	Group:   "provisioning.dpu.nvidia.com",
	Version: "v1alpha1",
	Kind:    "DPU",
}

// DPUReconciler maps each read-only DPU source to one entry. metadata.name is
// the DPU resource ID and OperatorTenant is its tenant. The parent agent is
// /spire/agent/dpu_hw/<serial>, matching the DPF-provisioned NodeAttestor.
// Configured Unix selectors identify the token fetcher that calls the Workload
// API.
type DPUReconciler struct {
	client.Client
	Config   Config
	Recorder record.EventRecorder
}

func (r *DPUReconciler) SetupWithManager(mgr ctrl.Manager) error {
	dpu := &unstructured.Unstructured{}
	dpu.SetGroupVersionKind(DPUGVK)
	return ctrl.NewControllerManagedBy(mgr).
		Named("dsx-dpu-identity").
		For(dpu).
		Watches(&spirev1alpha1.ClusterStaticEntry{}, handler.EnqueueRequestsFromMapFunc(requestForSource(DPUGVK.Group, DPUGVK.Kind))).
		Complete(r)
}

func (r *DPUReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	return reconcileSource(ctx, r.Client, r.Recorder, request, DPUGVK, r.desiredEntry)
}

func (r *DPUReconciler) desiredEntry(dpu *unstructured.Unstructured) (*spirev1alpha1.ClusterStaticEntry, error) {
	td, err := r.Config.trustDomain()
	if err != nil {
		return nil, err
	}
	spiffeID, err := (identity.Subject{
		Tenant:       r.Config.OperatorTenant,
		ResourceType: identity.ResourceDPU,
		ResourceID:   dpu.GetName(),
	}).SPIFFEID(td)
	if err != nil {
		return nil, err
	}
	rawSerial, found, err := unstructured.NestedString(dpu.Object, "spec", "serialNumber")
	if err != nil {
		return nil, fmt.Errorf("DPU spec.serialNumber: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("DPU spec.serialNumber is required")
	}
	serial, err := dpuidentity.NormalizeSerial(rawSerial)
	if err != nil {
		return nil, fmt.Errorf("DPU spec.serialNumber: %w", err)
	}
	parentID, err := spiffeid.FromSegments(td, "spire", "agent", dpuidentity.PluginName, serial)
	if err != nil {
		return nil, fmt.Errorf("DPU parent ID: %w", err)
	}
	return newEntry(dpu, spiffeID, parentID, "dsx-dpu", r.Config.ClassName, slices.Clone(r.Config.DPUTokenFetcherSelectors))
}

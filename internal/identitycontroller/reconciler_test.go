package identitycontroller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	spirev1alpha1 "github.com/spiffe/spire-controller-manager/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var testConfig = Config{
	TrustDomain:              "k2.sjc-1.nvidia.com",
	OperatorTenant:           "operator-1",
	ClassName:                "dsx-identity-dsx-identity",
	DPUTokenFetcherSelectors: []string{"unix:uid:1000", "unix:path:/usr/local/bin/dsx-token-fetcher"},
	DPUMetadataSelectors:     []string{"k8s:ns:dsx-imds", "k8s:sa:metadata-service"},
}

func TestConfigRejectsInvalidDPUTokenFetcherSelectors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		selectors []string
		want      string
	}{
		{name: "missing", want: "at least one DPU token fetcher selector is required"},
		{name: "empty", selectors: []string{""}, want: "DPU token fetcher selector must not be empty"},
		{name: "Kubernetes", selectors: []string{"k8s:sa:metadata-service"}, want: "must use the Unix WorkloadAttestor"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := testConfig
			config.DPUTokenFetcherSelectors = test.selectors
			if err := config.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestConfigRejectsInvalidDPUHostConfiguration(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		configure func(*Config)
		want      string
	}{
		{
			name: "missing metadata selector",
			configure: func(config *Config) {
				config.DPUMetadataSelectors = nil
			},
			want: "at least one DPU metadata selector is required",
		},
		{
			name: "empty metadata selector",
			configure: func(config *Config) {
				config.DPUMetadataSelectors = []string{""}
			},
			want: "DPU metadata selector must not be empty",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := testConfig
			test.configure(&config)
			if err := config.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestDPUReconcileCreatesBaseIdentityAndCleansUp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dpu := &unstructured.Unstructured{}
	dpu.SetGroupVersionKind(DPUGVK)
	dpu.SetName("dpu-1")
	dpu.SetUID(types.UID("f6e44986-13f5-4778-8424-1f0514a0f20c"))
	setSourceSpecField(t, dpu, "serialNumber", "52TF43G001G")
	c := &applyClient{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(dpu).Build()}
	r := &DPUReconciler{Client: c, Config: testConfig, Recorder: record.NewFakeRecorder(4)}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: dpu.GetName()}}
	if _, err := r.desiredEntry(dpu); err != nil {
		t.Fatal(err)
	}

	if result, err := r.Reconcile(ctx, request); err != nil || result.Requeue {
		t.Fatalf("DPU reconciliation = %#v, %v", result, err)
	}
	storedDPU := &unstructured.Unstructured{}
	storedDPU.SetGroupVersionKind(DPUGVK)
	if err := c.Get(ctx, request.NamespacedName, storedDPU); err != nil {
		t.Fatal(err)
	}
	if len(storedDPU.GetFinalizers()) != 0 {
		t.Fatal("DPU source was finalized by the identity controller")
	}
	entry := getEntry(t, c, childName(dpu))
	assertEntry(t, entry,
		"spiffe://k2.sjc-1.nvidia.com/tenant/operator-1/service/dsx/dpu/dpu-1",
		"spiffe://k2.sjc-1.nvidia.com/spire/agent/dpu_hw/52tf43g001g",
		"dsx-dpu",
		[]string{"unix:uid:1000", "unix:path:/usr/local/bin/dsx-token-fetcher"},
	)
	if len(entry.OwnerReferences) != 0 {
		t.Fatal("cluster-scoped entry must not have an owner reference")
	}
	if entry.Labels["identity.dsx.nvidia.com/source-uid"] != string(dpu.GetUID()) {
		t.Fatalf("unexpected source UID label: %q", entry.Labels["identity.dsx.nvidia.com/source-uid"])
	}
	assertSourceAnnotations(t, entry, DPUGVK.Group, DPUGVK.Kind, "", dpu.GetName())
	assertApplyCalls(t, c, 1)

	requests := requestForSource(DPUGVK.Group, DPUGVK.Kind)(ctx, entry)
	if len(requests) != 1 || requests[0] != request {
		t.Fatalf("child mapped to %#v, want %#v", requests, request)
	}
	if err := c.Delete(ctx, entry); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, requests[0]); err != nil {
		t.Fatal(err)
	}
	entry = getEntry(t, c, childName(dpu))
	assertEntry(t, entry,
		"spiffe://k2.sjc-1.nvidia.com/tenant/operator-1/service/dsx/dpu/dpu-1",
		"spiffe://k2.sjc-1.nvidia.com/spire/agent/dpu_hw/52tf43g001g",
		"dsx-dpu",
		[]string{"unix:uid:1000", "unix:path:/usr/local/bin/dsx-token-fetcher"},
	)
	assertApplyCalls(t, c, 2)

	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(DPUGVK)
	if err := c.Get(ctx, request.NamespacedName, current); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, current); err != nil {
		t.Fatal(err)
	}
	if result, err := r.Reconcile(ctx, request); err != nil || result.Requeue {
		t.Fatalf("DPU deletion reconciliation = %#v, %v", result, err)
	}
	if err := c.Get(ctx, client.ObjectKey{Name: entry.Name}, &spirev1alpha1.ClusterStaticEntry{}); !apierrors.IsNotFound(err) {
		t.Fatalf("entry was not deleted: %v", err)
	}
	deletedDPU := &unstructured.Unstructured{}
	deletedDPU.SetGroupVersionKind(DPUGVK)
	if err := c.Get(ctx, request.NamespacedName, deletedDPU); !apierrors.IsNotFound(err) {
		t.Fatalf("DPU source was not deleted: %v", err)
	}
}

func TestDPUDesiredEntryRequiresValidSerial(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		serial any
		want   string
	}{
		{name: "missing", want: "spec.serialNumber is required"},
		{name: "wrong type", serial: int64(1), want: "spec.serialNumber"},
		{name: "invalid", serial: "invalid serial", want: "invalid character"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dpu := &unstructured.Unstructured{}
			dpu.SetGroupVersionKind(DPUGVK)
			dpu.SetName("dpu-1")
			if test.serial != nil {
				setSourceSpecField(t, dpu, "serialNumber", test.serial)
			}
			_, err := (&DPUReconciler{Config: testConfig}).desiredEntry(dpu)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("desiredEntry() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestLaunchLayerSourceReconcileLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	host := newLaunchLayerSource("host-source", "tenant-system", "7ae7fe75-e671-4b0e-a76d-ef02bb5b040b", map[string]any{
		"type": "bm", "tenantID": "tenant-2", "resourceID": "host-1", "role": "database", "parentID": "dpu-1",
	})
	vm := newLaunchLayerSource("vm-source", "tenant-system", "e35e41ae-a112-4f64-8841-298c2a6662b0", map[string]any{
		"type": "vm", "tenantID": "tenant-2", "resourceID": "vm-1", "role": "database", "parentID": "host-1",
		"holder": map[string]any{"namespace": "tenant-vms", "serviceAccount": "vm-metadata"},
	})
	recorder := record.NewFakeRecorder(8)
	c := &applyClient{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(host, vm).Build()}
	r := &LaunchLayerReconciler{Client: c, Config: testConfig, Recorder: recorder}

	hostRequest := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(host)}
	vmRequest := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(vm)}
	for _, request := range []ctrl.Request{hostRequest, vmRequest} {
		if result, err := r.Reconcile(ctx, request); err != nil || result.Requeue {
			t.Fatalf("LaunchLayer reconciliation = %#v, %v", result, err)
		}
	}
	for _, request := range []ctrl.Request{hostRequest, vmRequest} {
		stored := &unstructured.Unstructured{}
		stored.SetGroupVersionKind(LaunchLayerSourceGVK)
		if err := c.Get(ctx, request.NamespacedName, stored); err != nil {
			t.Fatal(err)
		}
		if len(stored.GetFinalizers()) != 0 {
			t.Fatal("LaunchLayer source was finalized by the identity controller")
		}
	}

	hostEntry := getEntry(t, c, childName(host))
	assertEntry(t, hostEntry,
		"spiffe://k2.sjc-1.nvidia.com/tenant/tenant-2/service/dsx/bm/host-1/role/database",
		"spiffe://k2.sjc-1.nvidia.com/spire/agent/dsx_dpu/dpu-1",
		"dsx-imds",
		[]string{"k8s:ns:dsx-imds", "k8s:sa:metadata-service"},
	)
	vmEntry := getEntry(t, c, childName(vm))
	assertEntry(t, vmEntry,
		"spiffe://k2.sjc-1.nvidia.com/tenant/tenant-2/service/dsx/vm/vm-1/role/database",
		"spiffe://k2.sjc-1.nvidia.com/spire/agent/dsx_imds/tenant/operator-1/bm/host-1",
		"dsx-imds-vm",
		[]string{"k8s:ns:tenant-vms", "k8s:sa:vm-metadata"},
	)
	assertSourceAnnotations(t, hostEntry, LaunchLayerSourceGVK.Group, LaunchLayerSourceGVK.Kind, host.GetNamespace(), host.GetName())
	assertSourceAnnotations(t, vmEntry, LaunchLayerSourceGVK.Group, LaunchLayerSourceGVK.Kind, vm.GetNamespace(), vm.GetName())
	assertApplyCalls(t, c, 2)
	invalidRole := vm.DeepCopy()
	setSourceSpecField(t, invalidRole, "role", "BadRole")
	if _, err := r.desiredEntry(invalidRole); err == nil {
		t.Fatal("expected invalid role to be rejected")
	}
	hostEntry.Spec.ParentID = "spiffe://drift.invalid/spire/agent/wrong"
	if err := c.Update(ctx, hostEntry); err != nil {
		t.Fatal(err)
	}
	driftRequests := requestForSource(LaunchLayerSourceGVK.Group, LaunchLayerSourceGVK.Kind)(ctx, hostEntry)
	if len(driftRequests) != 1 || driftRequests[0] != hostRequest {
		t.Fatalf("drifted child mapped to %#v, want %#v", driftRequests, hostRequest)
	}
	if _, err := r.Reconcile(ctx, driftRequests[0]); err != nil {
		t.Fatal(err)
	}
	hostEntry = getEntry(t, c, hostEntry.Name)
	assertEntry(t, hostEntry,
		"spiffe://k2.sjc-1.nvidia.com/tenant/tenant-2/service/dsx/bm/host-1/role/database",
		"spiffe://k2.sjc-1.nvidia.com/spire/agent/dsx_dpu/dpu-1",
		"dsx-imds",
		[]string{"k8s:ns:dsx-imds", "k8s:sa:metadata-service"},
	)
	assertApplyCalls(t, c, 3)

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(LaunchLayerSourceGVK)
	if err := c.Get(ctx, hostRequest.NamespacedName, updated); err != nil {
		t.Fatal(err)
	}
	setSourceSpecField(t, updated, "tenantID", "tenant-3")
	setSourceSpecField(t, updated, "resourceID", "host-2")
	setSourceSpecField(t, updated, "parentID", "dpu-2")
	if err := c.Update(ctx, updated); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, hostRequest); err != nil {
		t.Fatal(err)
	}
	assertEntry(t, getEntry(t, c, hostEntry.Name),
		"spiffe://k2.sjc-1.nvidia.com/tenant/tenant-3/service/dsx/bm/host-2/role/database",
		"spiffe://k2.sjc-1.nvidia.com/spire/agent/dsx_dpu/dpu-2",
		"dsx-imds",
		[]string{"k8s:ns:dsx-imds", "k8s:sa:metadata-service"},
	)
	assertApplyCalls(t, c, 4)

	if err := c.Get(ctx, hostRequest.NamespacedName, updated); err != nil {
		t.Fatal(err)
	}
	setSourceSpecField(t, updated, "holder", map[string]any{"namespace": "dsx-imds", "serviceAccount": "metadata-service"})
	if err := c.Update(ctx, updated); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, hostRequest); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKey{Name: hostEntry.Name}, &spirev1alpha1.ClusterStaticEntry{}); !apierrors.IsNotFound(err) {
		t.Fatalf("invalid update retained stale entry: %v", err)
	}
	select {
	case event := <-recorder.Events:
		if !strings.Contains(event, "Warning InvalidIdentity") {
			t.Fatalf("unexpected warning event: %q", event)
		}
	case <-time.After(time.Second):
		t.Fatal("expected invalid identity event")
	}

	deletingVM := &unstructured.Unstructured{}
	deletingVM.SetGroupVersionKind(LaunchLayerSourceGVK)
	if err := c.Get(ctx, vmRequest.NamespacedName, deletingVM); err != nil {
		t.Fatal(err)
	}
	c.childDeleteFailures = 1
	if err := c.Delete(ctx, deletingVM); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, vmRequest); !errors.Is(err, errInjectedChildDelete) {
		t.Fatalf("first child deletion error = %v, want %v", err, errInjectedChildDelete)
	}
	if result, err := r.Reconcile(ctx, vmRequest); err != nil || result.Requeue {
		t.Fatalf("LaunchLayer deletion reconciliation = %#v, %v", result, err)
	}
	if err := c.Get(ctx, vmRequest.NamespacedName, deletingVM); !apierrors.IsNotFound(err) {
		t.Fatalf("LaunchLayer source was not deleted: %v", err)
	}
	if err := c.Get(ctx, client.ObjectKey{Name: vmEntry.Name}, &spirev1alpha1.ClusterStaticEntry{}); !apierrors.IsNotFound(err) {
		t.Fatalf("VM entry was not deleted: %v", err)
	}
}

func TestLaunchLayerConflictsRecoverAfterSourceCorrection(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	vmA := newLaunchLayerSource("vm-a", "tenant-system", "3f94f124-72cd-467b-9b83-b5f536a8620d", map[string]any{
		"type": "vm", "tenantID": "tenant-2", "resourceID": "vm-a", "parentID": "worker-1",
		"holder": map[string]any{"namespace": "tenant-vms", "serviceAccount": "vm-metadata"},
	})
	vmB := newLaunchLayerSource("vm-b", "tenant-system", "db1f2eb8-7ec4-4a72-af0e-089f8d6dc427", map[string]any{
		"type": "vm", "tenantID": "tenant-2", "resourceID": "vm-b", "parentID": "worker-1",
		"holder": map[string]any{"namespace": "tenant-vms", "serviceAccount": "vm-metadata", "podUID": "1e605955-c4b7-4090-b12a-8fcf3b9f334d"},
	})
	vmC := newLaunchLayerSource("vm-c", "tenant-system", "93fc5f2d-5ba2-4f77-a67c-c8ac260cd70a", map[string]any{
		"type": "vm", "tenantID": "tenant-2", "resourceID": "vm-b", "parentID": "worker-1",
		"holder": map[string]any{"namespace": "tenant-vms", "serviceAccount": "vm-metadata-c"},
	})
	c := &applyClient{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(vmA, vmB, vmC).Build()}
	r := &LaunchLayerReconciler{Client: c, Config: testConfig, Recorder: record.NewFakeRecorder(8)}
	requestA := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(vmA)}
	requestB := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(vmB)}
	requestC := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(vmC)}

	if _, err := r.Reconcile(ctx, requestA); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, requestB); err == nil || !strings.Contains(err.Error(), "holder selectors overlap") {
		t.Fatalf("overlapping holder error = %v", err)
	}
	if err := c.Get(ctx, client.ObjectKey{Name: childName(vmB)}, &spirev1alpha1.ClusterStaticEntry{}); !apierrors.IsNotFound(err) {
		t.Fatalf("conflicting holder entry exists: %v", err)
	}

	updateSourceField(t, ctx, c, requestB.NamespacedName, "holder", map[string]any{
		"namespace": "tenant-vms", "serviceAccount": "vm-metadata-b",
	})
	if _, err := r.Reconcile(ctx, requestB); err != nil {
		t.Fatalf("holder conflict did not recover: %v", err)
	}
	getEntry(t, c, childName(vmB))

	if _, err := r.Reconcile(ctx, requestC); err == nil || !strings.Contains(err.Error(), "SPIFFE ID") {
		t.Fatalf("duplicate SPIFFE ID error = %v", err)
	}
	if err := c.Get(ctx, client.ObjectKey{Name: childName(vmC)}, &spirev1alpha1.ClusterStaticEntry{}); !apierrors.IsNotFound(err) {
		t.Fatalf("duplicate SPIFFE ID entry exists: %v", err)
	}

	updateSourceField(t, ctx, c, requestC.NamespacedName, "resourceID", "vm-c")
	if _, err := r.Reconcile(ctx, requestC); err != nil {
		t.Fatalf("SPIFFE ID conflict did not recover: %v", err)
	}
	getEntry(t, c, childName(vmC))
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := spirev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func newLaunchLayerSource(name, namespace, uid string, spec map[string]any) *unstructured.Unstructured {
	source := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	source.SetGroupVersionKind(LaunchLayerSourceGVK)
	source.SetName(name)
	source.SetNamespace(namespace)
	source.SetUID(types.UID(uid))
	return source
}

func setSourceSpecField(t *testing.T, source *unstructured.Unstructured, name string, value any) {
	t.Helper()
	if err := unstructured.SetNestedField(source.Object, value, "spec", name); err != nil {
		t.Fatal(err)
	}
}

func updateSourceField(t *testing.T, ctx context.Context, c client.Client, key client.ObjectKey, name string, value any) {
	t.Helper()
	source := &unstructured.Unstructured{}
	source.SetGroupVersionKind(LaunchLayerSourceGVK)
	if err := c.Get(ctx, key, source); err != nil {
		t.Fatal(err)
	}
	setSourceSpecField(t, source, name, value)
	if err := c.Update(ctx, source); err != nil {
		t.Fatal(err)
	}
}

func getEntry(t *testing.T, c client.Client, name string) *spirev1alpha1.ClusterStaticEntry {
	t.Helper()
	entry := &spirev1alpha1.ClusterStaticEntry{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: name}, entry); err != nil {
		t.Fatal(err)
	}
	return entry
}

func assertEntry(t *testing.T, entry *spirev1alpha1.ClusterStaticEntry, spiffeID, parentID, hint string, selectors []string) {
	t.Helper()
	if entry.Spec.SPIFFEID != spiffeID {
		t.Fatalf("SPIFFE ID = %q, want %q", entry.Spec.SPIFFEID, spiffeID)
	}
	if entry.Spec.ParentID != parentID {
		t.Fatalf("parent ID = %q, want %q", entry.Spec.ParentID, parentID)
	}
	if entry.Spec.Hint != hint {
		t.Fatalf("hint = %q, want %q", entry.Spec.Hint, hint)
	}
	if entry.Spec.ClassName != testConfig.ClassName {
		t.Fatalf("class name = %q, want %q", entry.Spec.ClassName, testConfig.ClassName)
	}
	if len(entry.Spec.Selectors) != len(selectors) {
		t.Fatalf("selectors = %#v, want %#v", entry.Spec.Selectors, selectors)
	}
	for i := range selectors {
		if entry.Spec.Selectors[i] != selectors[i] {
			t.Fatalf("selectors = %#v, want %#v", entry.Spec.Selectors, selectors)
		}
	}
}

func assertSourceAnnotations(t *testing.T, entry *spirev1alpha1.ClusterStaticEntry, group, kind, namespace, name string) {
	t.Helper()
	want := map[string]string{
		sourceGroupAnnotation:     group,
		sourceKindAnnotation:      kind,
		sourceNamespaceAnnotation: namespace,
		sourceNameAnnotation:      name,
	}
	for key, value := range want {
		if entry.Annotations[key] != value {
			t.Fatalf("annotation %q = %q, want %q", key, entry.Annotations[key], value)
		}
	}
}

func assertApplyCalls(t *testing.T, c *applyClient, want int) {
	t.Helper()
	if len(c.applyCalls) != want {
		t.Fatalf("server-side apply calls = %d, want %d", len(c.applyCalls), want)
	}
	for i, call := range c.applyCalls {
		if call.patchType != types.ApplyPatchType || call.fieldManager != fieldOwner || !call.force {
			t.Fatalf("apply call %d = type %q, manager %q, force %v", i, call.patchType, call.fieldManager, call.force)
		}
	}
}

// applyClient gives controller-runtime's fake client the API server's
// create-or-update behavior for server-side apply patches.
type applyClient struct {
	client.Client
	applyCalls          []applyCall
	childDeleteFailures int
}

type applyCall struct {
	patchType    types.PatchType
	fieldManager string
	force        bool
}

var errInjectedChildDelete = errors.New("injected ClusterStaticEntry deletion failure")

func (c *applyClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if patch.Type() != types.ApplyPatchType {
		return c.Client.Patch(ctx, obj, patch, opts...)
	}
	options := (&client.PatchOptions{}).ApplyOptions(opts)
	c.applyCalls = append(c.applyCalls, applyCall{
		patchType:    patch.Type(),
		fieldManager: options.FieldManager,
		force:        options.Force != nil && *options.Force,
	})
	current := obj.DeepCopyObject().(client.Object)
	err := c.Client.Get(ctx, client.ObjectKeyFromObject(obj), current)
	if apierrors.IsNotFound(err) {
		return c.Client.Create(ctx, obj)
	}
	if err != nil {
		return err
	}
	obj.SetResourceVersion(current.GetResourceVersion())
	return c.Client.Update(ctx, obj)
}

func (c *applyClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if _, ok := obj.(*spirev1alpha1.ClusterStaticEntry); ok && c.childDeleteFailures > 0 {
		c.childDeleteFailures--
		return errInjectedChildDelete
	}
	return c.Client.Delete(ctx, obj, opts...)
}

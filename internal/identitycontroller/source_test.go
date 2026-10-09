// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package identitycontroller

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	spirev1alpha1 "github.com/spiffe/spire-controller-manager/api/v1alpha1"
	"golang.org/x/sync/errgroup"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	toolscache "k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	controllerconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

func TestControllersRecoverLateSourceAPIs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	scheme := testScheme(t)
	// Register all fake client types before concurrent lists can mutate the scheme.
	for _, gvk := range []schema.GroupVersionKind{DPUGVK, VirtualInstanceGVK, BareMetalInstanceGVK} {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	mapper := &lateSourceMapper{RESTMapper: meta.NewDefaultRESTMapper(nil)}
	c := &applyClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithRESTMapper(mapper).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				gvk := list.GetObjectKind().GroupVersionKind()
				if gvk.Kind == "BareMetalInstanceList" {
					if _, err := mapper.RESTMapping(BareMetalInstanceGVK.GroupKind(), BareMetalInstanceGVK.Version); err != nil {
						return err
					}
				}
				return c.List(ctx, list, opts...)
			},
		}).Build()}
	// The fake apply client records patches. Serialize those writes across controllers.
	serialized := &lockedApplyClient{applyClient: c}
	informerCache := &sourceTestCache{ctx: ctx, client: serialized, informers: make(map[schema.GroupVersionKind]toolscache.SharedIndexInformer)}
	mgr := &sourceTestManager{scheme: scheme, mapper: mapper, cache: informerCache}
	config := testConfig
	config.SourceAPIPollInterval = time.Millisecond
	if err := (&DPUReconciler{Client: serialized, Config: config}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	for _, gvk := range []schema.GroupVersionKind{VirtualInstanceGVK, BareMetalInstanceGVK} {
		if err := (&LaunchLayerReconciler{Client: serialized, Config: config, SourceGVK: gvk}).SetupWithManager(mgr); err != nil {
			t.Fatal(err)
		}
	}
	// Start the same managed tasks as production and collect their errors on cleanup.
	var group errgroup.Group
	for _, runnable := range mgr.runnables {
		group.Go(func() error { return runnable.Start(ctx) })
	}
	t.Cleanup(func() {
		cancel()
		if err := group.Wait(); err != nil {
			t.Error(err)
		}
	})

	// Install DPU first while both Launch Layer APIs remain absent.
	dpu := &unstructured.Unstructured{}
	dpu.SetGroupVersionKind(DPUGVK)
	dpu.SetName("late-dpu")
	dpu.SetNamespace("default")
	dpu.SetUID("dpu-uid")
	dpu.SetLabels(map[string]string{dpuMachineIDLabel: "machine-1"})
	setSourceSpecField(t, dpu, "serialNumber", "serial-1")
	if err := serialized.Create(ctx, dpu); err != nil {
		t.Fatal(err)
	}
	mapper.install(DPUGVK)
	waitForSourceEntry(t, ctx, serialized, dpu, "spiffe://k2.sjc-1.nvidia.com/tenant/operator-1/service/dsx/bm/machine-1")

	// Initial informer lists must recover pre-existing VM and tenant BM sources.
	vm := newVirtualInstance("late-vm", "default", "vm-uid", "tenant-1", "vm-1", "host-1", "940863e9-1e6f-40ea-9104-c46a7b041ce5")
	bm := newBareMetalInstance("late-bm", "default", "bm-uid", "tenant-2", "machine-1", "machine-1", "serial-1")
	for _, object := range []*unstructured.Unstructured{vm, bm} {
		if err := serialized.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
		mapper.install(object.GroupVersionKind())
	}
	waitForSourceEntry(t, ctx, serialized, vm, "spiffe://k2.sjc-1.nvidia.com/tenant/tenant-1/service/dsx/vm/vm-1")
	waitForSourceEntry(t, ctx, serialized, bm, "spiffe://k2.sjc-1.nvidia.com/tenant/tenant-2/service/dsx/bm/machine-1")
	waitForEntryDeletion(t, ctx, serialized, dpu)

	// A later deletion event must restore the operator fallback without rediscovery.
	if err := serialized.Delete(ctx, bm); err != nil {
		t.Fatal(err)
	}
	informerCache.mu.Lock()
	events := informerCache.events
	informerCache.mu.Unlock()
	events.Delete(bm)
	waitForEntryDeletion(t, ctx, serialized, bm)
	waitForSourceEntry(t, ctx, serialized, dpu, "spiffe://k2.sjc-1.nvidia.com/tenant/operator-1/service/dsx/bm/machine-1")
}

func waitForSourceEntry(t *testing.T, ctx context.Context, c client.Client, object *unstructured.Unstructured, id string) {
	t.Helper()
	if err := wait.PollUntilContextCancel(ctx, 10*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		entry := &spirev1alpha1.ClusterStaticEntry{}
		err := c.Get(ctx, client.ObjectKey{Name: childName(object)}, entry)
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return err == nil && entry.Spec.SPIFFEID == id, err
	}); err != nil {
		t.Fatalf("wait for %s identity %s: %v", object.GetName(), id, err)
	}
}

func waitForEntryDeletion(t *testing.T, ctx context.Context, c client.Client, object *unstructured.Unstructured) {
	t.Helper()
	if err := wait.PollUntilContextCancel(ctx, 10*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		err := c.Get(ctx, client.ObjectKey{Name: childName(object)}, &spirev1alpha1.ClusterStaticEntry{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}); err != nil {
		t.Fatalf("wait for %s entry deletion: %v", object.GetName(), err)
	}
}

type lateSourceMapper struct {
	meta.RESTMapper
	mu sync.Mutex
}

func (m *lateSourceMapper) install(gvk schema.GroupVersionKind) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RESTMapper.(*meta.DefaultRESTMapper).Add(gvk, meta.RESTScopeNamespace)
}

func (m *lateSourceMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.RESTMapper.RESTMapping(gk, versions...)
}

type sourceTestManager struct {
	ctrl.Manager
	scheme    *runtime.Scheme
	mapper    meta.RESTMapper
	cache     cache.Cache
	runnables []manager.Runnable
	timeout   time.Duration
}

func (m *sourceTestManager) GetScheme() *runtime.Scheme     { return m.scheme }
func (m *sourceTestManager) GetRESTMapper() meta.RESTMapper { return m.mapper }
func (m *sourceTestManager) GetCache() cache.Cache          { return m.cache }
func (m *sourceTestManager) GetLogger() logr.Logger         { return logr.Discard() }
func (m *sourceTestManager) GetControllerOptions() controllerconfig.Controller {
	return controllerconfig.Controller{SkipNameValidation: new(true), CacheSyncTimeout: m.timeout}
}
func (m *sourceTestManager) Add(r manager.Runnable) error {
	m.runnables = append(m.runnables, r)
	return nil
}

type sourceTestCache struct {
	cache.Cache
	ctx       context.Context
	client    client.Client
	mu        sync.Mutex
	informers map[schema.GroupVersionKind]toolscache.SharedIndexInformer
	events    *watch.RaceFreeFakeWatcher
}

func (c *sourceTestCache) GetInformer(ctx context.Context, object client.Object, _ ...cache.InformerGetOption) (cache.Informer, error) {
	gvk, err := apiutil.GVKForObject(object, c.client.Scheme())
	if err != nil {
		return nil, err
	}
	if gvk.Kind != "ClusterStaticEntry" {
		if _, err := c.client.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	if informer, ok := c.informers[gvk]; ok {
		c.mu.Unlock()
		return informer, nil
	}
	events := watch.NewRaceFreeFake()
	if gvk == BareMetalInstanceGVK {
		c.events = events
	}
	informer := toolscache.NewSharedIndexInformer(&toolscache.ListWatch{
		ListFunc: func(metav1.ListOptions) (runtime.Object, error) {
			if gvk.Kind == "ClusterStaticEntry" {
				list := &spirev1alpha1.ClusterStaticEntryList{}
				return list, c.client.List(c.ctx, list)
			}
			list := &unstructured.UnstructuredList{}
			list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
			return list, c.client.List(c.ctx, list)
		},
		WatchFunc: func(metav1.ListOptions) (watch.Interface, error) { return events, nil },
	}, object, 0, nil)
	c.informers[gvk] = informer
	c.mu.Unlock()
	go informer.Run(c.ctx.Done())
	if !toolscache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		return nil, ctx.Err()
	}
	return informer, nil
}

func (c *sourceTestCache) WaitForCacheSync(ctx context.Context) bool {
	c.mu.Lock()
	var synced []toolscache.InformerSynced
	for _, informer := range c.informers {
		synced = append(synced, informer.HasSynced)
	}
	c.mu.Unlock()
	return toolscache.WaitForCacheSync(ctx.Done(), synced...)
}

type lockedApplyClient struct {
	*applyClient
	mu sync.Mutex
}

func (c *lockedApplyClient) Patch(ctx context.Context, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.applyClient.Patch(ctx, object, patch, opts...)
}

func TestDeferredSourceWatchErrors(t *testing.T) {
	for _, failure := range []error{
		errors.New("discovery unavailable"),
		apierrors.NewForbidden(schema.GroupResource{Group: DPUGVK.Group, Resource: "dpus"}, "", errors.New("denied")),
		&meta.NoKindMatchError{GroupKind: DPUGVK.GroupKind(), SearchedVersions: []string{DPUGVK.Version}},
	} {
		t.Run(failure.Error(), func(t *testing.T) {
			mgr := &sourceTestManager{mapper: meta.NewDefaultRESTMapper(nil)}
			if err := watchSourceWhenAvailable(mgr, nil, DPUGVK, nil, time.Hour); err != nil {
				t.Fatal(err)
			}
			mgr.mapper = mappingErrorMapper{RESTMapper: mgr.mapper, err: failure}
			if meta.IsNoMatchError(failure) {
				mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Group: DPUGVK.Group, Version: "v1beta1"}})
				gvk := DPUGVK
				gvk.Version = "v1beta1"
				mapper.Add(gvk, meta.RESTScopeNamespace)
				mgr.mapper = mapper
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := mgr.runnables[0].Start(ctx)
			if !errors.Is(err, failure) && !(meta.IsNoMatchError(failure) && meta.IsNoMatchError(err)) {
				t.Fatalf("deferred watch error = %v, want %v", err, failure)
			}
		})
	}
}

func TestDeferredSourceWatchStopsOnCancellation(t *testing.T) {
	mgr := &sourceTestManager{mapper: meta.NewDefaultRESTMapper(nil)}
	if err := watchSourceWhenAvailable(mgr, nil, DPUGVK, nil, time.Hour); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mgr.runnables[0].Start(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDeferredSourceInformerErrors(t *testing.T) {
	for _, failure := range []error{
		errors.New("list unavailable"),
		apierrors.NewForbidden(schema.GroupResource{Group: DPUGVK.Group, Resource: "dpus"}, "", errors.New("denied")),
		context.DeadlineExceeded,
	} {
		t.Run(failure.Error(), func(t *testing.T) {
			mapper := &lateSourceMapper{RESTMapper: meta.NewDefaultRESTMapper(nil)}
			cacheFailure := failure
			if errors.Is(failure, context.DeadlineExceeded) {
				cacheFailure = nil
			}
			mgr := &sourceTestManager{mapper: mapper, cache: &failingSourceCache{err: cacheFailure}, timeout: 10 * time.Millisecond}
			if err := watchSourceWhenAvailable(mgr, nil, DPUGVK, nil, time.Hour); err != nil {
				t.Fatal(err)
			}
			mapper.install(DPUGVK)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := mgr.runnables[0].Start(ctx); !errors.Is(err, failure) {
				t.Fatalf("deferred informer error = %v, want %v", err, failure)
			}
		})
	}
}

type failingSourceCache struct {
	cache.Cache
	err error
}

func (c *failingSourceCache) GetInformer(ctx context.Context, _ client.Object, _ ...cache.InformerGetOption) (cache.Informer, error) {
	if c.err != nil {
		return nil, c.err
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

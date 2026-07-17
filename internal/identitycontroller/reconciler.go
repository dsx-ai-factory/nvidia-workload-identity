// Package identitycontroller derives one SPIRE ClusterStaticEntry from each
// read-only DPU or LaunchLayer source. Source and child watches drive creation,
// drift repair, and cleanup.
package identitycontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	spirev1alpha1 "github.com/spiffe/spire-controller-manager/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	"dsx-identity/internal/identity"
)

const (
	fieldOwner                = "dsx-identity-controller"
	sourceGroupAnnotation     = "identity.dsx.nvidia.com/source-group"
	sourceKindAnnotation      = "identity.dsx.nvidia.com/source-kind"
	sourceNamespaceAnnotation = "identity.dsx.nvidia.com/source-namespace"
	sourceNameAnnotation      = "identity.dsx.nvidia.com/source-name"
)

// Config holds shard-wide controller values. TrustDomain encodes local SPIRE
// IDs. OperatorTenant supplies the tenant segment for DPU subjects and the
// host-agent parent of a VM. A tenant supplied by LaunchLayer remains on the
// standalone host or VM subject. ClassName is placed on every entry.
// DPUTokenFetcherSelectors select the process that calls the Workload API and
// writes the DPU token file.
type Config struct {
	TrustDomain              string
	OperatorTenant           string
	ClassName                string
	DPUTokenFetcherSelectors []string
	DPUMetadataSelectors     []string
}

// Holder contains stable SPIRE namespace and service account selectors. PodUID
// narrows selection only when the external source supplies it.
type Holder struct {
	Namespace      string `json:"namespace"`
	ServiceAccount string `json:"serviceAccount"`
	PodUID         string `json:"podUID,omitempty"`
}

// Validate verifies shard configuration before either controller starts.
func (c Config) Validate() error {
	if _, err := c.trustDomain(); err != nil {
		return err
	}
	if !identity.ValidTenant(c.OperatorTenant) {
		return fmt.Errorf("invalid operator tenant %q", c.OperatorTenant)
	}
	if c.ClassName == "" {
		return errors.New("class name is required")
	}
	if len(c.DPUTokenFetcherSelectors) == 0 {
		return errors.New("at least one DPU token fetcher selector is required")
	}
	for _, selector := range c.DPUTokenFetcherSelectors {
		if selector == "" {
			return errors.New("DPU token fetcher selector must not be empty")
		}
		if !strings.HasPrefix(selector, "unix:") {
			return fmt.Errorf("DPU token fetcher selector %q must use the Unix WorkloadAttestor", selector)
		}
	}
	if len(c.DPUMetadataSelectors) == 0 {
		return errors.New("at least one DPU metadata selector is required")
	}
	for _, selector := range c.DPUMetadataSelectors {
		if selector == "" {
			return errors.New("DPU metadata selector must not be empty")
		}
	}
	return nil
}

func (c Config) trustDomain() (spiffeid.TrustDomain, error) {
	td, err := spiffeid.TrustDomainFromString(c.TrustDomain)
	if err != nil {
		return spiffeid.TrustDomain{}, fmt.Errorf("trust domain: %w", err)
	}
	return td, nil
}

// reconcileSource owns the one deterministic ClusterStaticEntry for a source
// key. A valid live source is applied. A source with a deletion timestamp is
// deleted immediately. If Get returns NotFound, the GVK, namespace, and name
// in the request reproduce the child name for deletion. Invalid input deletes
// any stale entry and emits a warning. No source finalizer is needed because
// the controller never mutates externally owned sources and the reverse child
// watch recovers missed cleanup.
func reconcileSource(
	ctx context.Context,
	c client.Client,
	recorder record.EventRecorder,
	request ctrl.Request,
	gvk schema.GroupVersionKind,
	buildEntry func(*unstructured.Unstructured) (*spirev1alpha1.ClusterStaticEntry, error),
) (ctrl.Result, error) {
	source := &unstructured.Unstructured{}
	source.SetGroupVersionKind(gvk)
	if err := c.Get(ctx, request.NamespacedName, source); err != nil {
		if apierrors.IsNotFound(err) {
			return deleteEntry(ctx, c, childNameFor(gvk.Group, gvk.Kind, request.Namespace, request.Name))
		}
		return ctrl.Result{}, err
	}

	entryName := childName(source)
	if !source.GetDeletionTimestamp().IsZero() {
		return deleteEntry(ctx, c, entryName)
	}

	entry, err := buildEntry(source)
	if err != nil {
		if _, deleteErr := deleteEntry(ctx, c, entryName); deleteErr != nil {
			return ctrl.Result{}, deleteErr
		}
		if recorder != nil {
			recorder.Event(source, "Warning", "InvalidIdentity", err.Error())
		}
		// A source update is the meaningful retry trigger for invalid input.
		return ctrl.Result{}, nil
	}
	if err := validateEntryConflicts(ctx, c, entry); err != nil {
		if _, deleteErr := deleteEntry(ctx, c, entryName); deleteErr != nil {
			return ctrl.Result{}, errors.Join(err, deleteErr)
		}
		if recorder != nil {
			recorder.Event(source, "Warning", "ConflictingIdentity", err.Error())
		}
		return ctrl.Result{}, err
	}
	// This controller exclusively owns derived entry fields. Server-side apply
	// restores drift without merging another writer's values.
	return ctrl.Result{}, c.Patch(ctx, entry, client.Apply, client.FieldOwner(fieldOwner), client.ForceOwnership)
}

func validateEntryConflicts(ctx context.Context, c client.Client, desired *spirev1alpha1.ClusterStaticEntry) error {
	var entries spirev1alpha1.ClusterStaticEntryList
	if err := c.List(ctx, &entries, client.MatchingLabels{"app.kubernetes.io/managed-by": fieldOwner}); err != nil {
		return fmt.Errorf("list managed identities: %w", err)
	}
	for i := range entries.Items {
		entry := &entries.Items[i]
		if entry.Name == desired.Name {
			continue
		}
		if entry.Spec.SPIFFEID == desired.Spec.SPIFFEID {
			return fmt.Errorf("SPIFFE ID %q is already managed by %q", desired.Spec.SPIFFEID, entry.Name)
		}
		if entry.Spec.Hint == "dsx-imds-vm" && desired.Spec.Hint == "dsx-imds-vm" &&
			vmHoldersOverlap(entry.Spec.Selectors, desired.Spec.Selectors) {
			return fmt.Errorf("VM holder selectors overlap with managed identity %q", entry.Name)
		}
	}
	return nil
}

func vmHoldersOverlap(a, b []string) bool {
	selector := func(selectors []string, prefix string) string {
		for _, value := range selectors {
			if suffix, ok := strings.CutPrefix(value, prefix); ok {
				return suffix
			}
		}
		return ""
	}
	if selector(a, "k8s:ns:") != selector(b, "k8s:ns:") ||
		selector(a, "k8s:sa:") != selector(b, "k8s:sa:") {
		return false
	}
	aUID := selector(a, "k8s:pod-uid:")
	bUID := selector(b, "k8s:pod-uid:")
	return aUID == "" || bUID == "" || aUID == bUID
}

// deleteEntry is idempotent because a missing child is already deleted.
func deleteEntry(ctx context.Context, c client.Client, name string) (ctrl.Result, error) {
	entry := &spirev1alpha1.ClusterStaticEntry{ObjectMeta: metav1.ObjectMeta{Name: name}}
	return ctrl.Result{}, client.IgnoreNotFound(c.Delete(ctx, entry))
}

func newEntry(source client.Object, spiffeID, parentID spiffeid.ID, hint, className string, selectors []string) (*spirev1alpha1.ClusterStaticEntry, error) {
	gvk := source.GetObjectKind().GroupVersionKind()
	if gvk.Group == "" || gvk.Kind == "" {
		return nil, errors.New("source group and kind are required")
	}

	// Source tuple annotations reconstruct the source key for the reverse child
	// watch. The source UID label is diagnostic only.
	labels := map[string]string{
		"app.kubernetes.io/managed-by":       fieldOwner,
		"identity.dsx.nvidia.com/source-uid": string(source.GetUID()),
	}
	annotations := map[string]string{
		sourceGroupAnnotation:     gvk.Group,
		sourceKindAnnotation:      gvk.Kind,
		sourceNamespaceAnnotation: source.GetNamespace(),
		sourceNameAnnotation:      source.GetName(),
	}
	return &spirev1alpha1.ClusterStaticEntry{
		TypeMeta: metav1.TypeMeta{APIVersion: spirev1alpha1.GroupVersion.String(), Kind: "ClusterStaticEntry"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        childName(source),
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: spirev1alpha1.ClusterStaticEntrySpec{
			SPIFFEID:  spiffeID.String(),
			ParentID:  parentID.String(),
			Selectors: selectors,
			Hint:      hint,
			ClassName: className,
		},
	}, nil
}

func requestForSource(group, kind string) handler.MapFunc {
	// ClusterStaticEntry annotations reconstruct the source reconcile key.
	// Existing children are enqueued by add events during informer startup.
	// Watch relists recover disconnects. Child updates and deletes also requeue
	// the source so server-side apply restores drift or recreates the child. The
	// pinned controller-runtime v0.20.4 default 10-hour resync is the final
	// backstop when no ordinary event is observed. There is no repository scan.
	return func(_ context.Context, child client.Object) []ctrl.Request {
		annotations := child.GetAnnotations()
		if annotations[sourceGroupAnnotation] != group || annotations[sourceKindAnnotation] != kind || annotations[sourceNameAnnotation] == "" {
			return nil
		}
		return []ctrl.Request{{NamespacedName: client.ObjectKey{
			Namespace: annotations[sourceNamespaceAnnotation],
			Name:      annotations[sourceNameAnnotation],
		}}}
	}
}

func (h Holder) validate() error {
	if errs := validation.IsDNS1123Label(h.Namespace); len(errs) > 0 {
		return fmt.Errorf("holder namespace: %s", errs[0])
	}
	if errs := validation.IsDNS1123Subdomain(h.ServiceAccount); len(errs) > 0 {
		return fmt.Errorf("holder service account: %s", errs[0])
	}
	if h.PodUID != "" && !identity.ValidPodUID(h.PodUID) {
		return fmt.Errorf("holder pod UID %q is not a canonical UUID", h.PodUID)
	}
	return nil
}

func (h Holder) selectors() ([]string, error) {
	if err := h.validate(); err != nil {
		return nil, err
	}
	selectors := []string{"k8s:ns:" + h.Namespace, "k8s:sa:" + h.ServiceAccount}
	if h.PodUID != "" {
		selectors = append(selectors, "k8s:pod-uid:"+h.PodUID)
	}
	return selectors, nil
}

func (h Holder) empty() bool {
	return h.Namespace == "" && h.ServiceAccount == "" && h.PodUID == ""
}

func childName(source client.Object) string {
	gvk := source.GetObjectKind().GroupVersionKind()
	return childNameFor(gvk.Group, gvk.Kind, source.GetNamespace(), source.GetName())
}

func childNameFor(group, kind, namespace, name string) string {
	// UID is excluded because it is unavailable after source deletion. GVK,
	// namespace, and name remain in the reconcile request. NUL separators make
	// that tuple unambiguous. The 80-bit prefix keeps the name compact while
	// retaining ample collision resistance.
	sum := sha256.Sum256([]byte(group + "\x00" + kind + "\x00" + namespace + "\x00" + name))
	return "dsx-identity-" + hex.EncodeToString(sum[:10])
}

package apply

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

	"pangolin-kube-controller/internal/kube/resources"
)

func TestIngressRouteOpsPropagatesTraefikIdentity(t *testing.T) {
	t.Parallel()

	const unrelatedLabel = "example.com/owner"
	client := &fakeResourceClient{}
	ops := &IngressRouteOps{
		ResIfc:                    client,
		Namespace:                 TestNS,
		ManagedLabelKey:           ManagedBy,
		ManagedLabelValue:         Controller,
		TraefikInstanceLabelKey:   InstanceLabelKeyFull,
		TraefikInstanceLabelValue: InstanceLabelValueMyInstance,
		ManagedAnnoKey:            AnnoKey,
		ManagedAnnoValue:          AnnoVal,
		IngressClass:              TraefikIngressClass,
	}

	desired := map[string]interface{}{
		"apiVersion": "traefik.io/v1alpha1",
		"kind":       "IngressRoute",
		"metadata":   map[string]interface{}{"name": TestRoute},
		"spec":       map[string]interface{}{},
	}
	require.NoError(t, ops.Apply(context.Background(), TestRoute, desired))
	labels := patchLabels(t, client.patchBytes)
	require.Equal(t, InstanceLabelValueMyInstance, labels[InstanceLabelKeyFull])
	require.Equal(t, FieldManager, client.patchOptions.FieldManager)

	client.existing = &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name": TestRoute, "namespace": TestNS,
			"labels":      map[string]interface{}{ManagedBy: Controller, unrelatedLabel: "platform"},
			"annotations": map[string]interface{}{AnnoKey: AnnoVal},
		},
	}}
	require.NoError(t, ops.Apply(context.Background(), TestRoute, desired))
	labels = patchLabels(t, client.patchBytes)
	require.Equal(t, InstanceLabelValueMyInstance, labels[InstanceLabelKeyFull])
	require.NotContains(t, labels, unrelatedLabel)
	require.NotNil(t, client.patchOptions.Force, "repairing missing managed identity must adopt SSA ownership")
	require.True(t, *client.patchOptions.Force)

	client.existing.SetLabels(map[string]string{
		ManagedBy: Controller, unrelatedLabel: "platform", InstanceLabelKeyFull: InstanceLabelValueMyInstance,
	})
	client.existing.SetAnnotations(map[string]string{
		AnnoKey: AnnoVal, "traefikconfig.ingress.kubernetes.io/router.ingressclass": TraefikIngressClass,
	})
	require.NoError(t, ops.Apply(context.Background(), TestRoute, desired))
	labels = patchLabels(t, client.patchBytes)
	require.Equal(t, InstanceLabelValueMyInstance, labels[InstanceLabelKeyFull])
	require.NotContains(t, labels, unrelatedLabel)
	require.Nil(t, client.patchOptions.Force, "repeated reconcile must not force unchanged ownership")
}

func TestIngressRouteOpsApplySinglePropagatesTraefikIdentity(t *testing.T) {
	t.Parallel()
	client := &fakeResourceClient{}
	ops := &IngressRouteOps{
		ResIfc: client, Namespace: TestNS,
		ManagedLabelKey: ManagedBy, ManagedLabelValue: Controller,
		TraefikInstanceLabelKey: InstanceLabelKeyFull, TraefikInstanceLabelValue: InstanceLabelValueMyInstance,
		ManagedAnnoKey: AnnoKey, ManagedAnnoValue: AnnoVal,
	}
	require.NoError(t, ops.ApplySingle(context.Background(), map[string]interface{}{
		"apiVersion": "traefik.io/v1alpha1",
		"kind":       "IngressRouteTCP",
		"metadata":   map[string]interface{}{"name": TestMW},
		"spec":       map[string]interface{}{},
	}, "IngressRouteTCP"))
	require.Equal(t, InstanceLabelValueMyInstance, patchLabels(t, client.patchBytes)[InstanceLabelKeyFull])
}

func patchLabels(t *testing.T, patch []byte) map[string]interface{} {
	t.Helper()
	var obj map[string]interface{}
	require.NoError(t, json.Unmarshal(patch, &obj))
	meta, ok := obj["metadata"].(map[string]interface{})
	require.True(t, ok)
	labels, ok := meta["labels"].(map[string]interface{})
	require.True(t, ok)
	return labels
}

func TestIngressRouteOpsApplyReadOnly(t *testing.T) {
	t.Parallel()

	fakeClient := &fakeResourceClient{}
	ops := &IngressRouteOps{
		ResIfc:            fakeClient,
		Namespace:         TestNS,
		ManagedLabelKey:   ManagedBy,
		ManagedLabelValue: Controller,
		ManagedAnnoKey:    AnnoKey,
		ManagedAnnoValue:  AnnoVal,
		IngressClass:      TraefikIngressClass,
		ReadOnly:          true,
	}

	u := map[string]interface{}{
		"metadata": map[string]interface{}{"name": TestRoute},
	}

	err := ops.Apply(context.Background(), TestRoute, u)
	require.NoError(t, err, "ReadOnly mode should return no error and not create resource")
	require.Equal(t, 0, fakeClient.createCount, "Create should not be called in ReadOnly mode")
}

func TestIngressRouteOpsApplyUpdatesExisting(t *testing.T) {
	t.Parallel()

	fakeClient := &fakeResourceClient{
		existing: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{
					"name":            TestRoute,
					"namespace":       TestNS,
					"resourceVersion": "1",
				},
			},
		},
	}
	ops := &IngressRouteOps{
		ResIfc:            fakeClient,
		Namespace:         TestNS,
		ManagedLabelKey:   ManagedBy,
		ManagedLabelValue: Controller,
		ManagedAnnoKey:    AnnoKey,
		ManagedAnnoValue:  AnnoVal,
		IngressClass:      TraefikIngressClass,
		ReadOnly:          false,
	}

	u := map[string]interface{}{
		"metadata": map[string]interface{}{"name": TestRoute},
	}

	err := ops.Apply(context.Background(), TestRoute, u)
	require.NoError(t, err, "Apply should succeed for existing resource")
	require.Equal(t, 1, fakeClient.patchCount, "Patch should be called once")
}

func TestIngressRouteOpsApplyGetError(t *testing.T) {
	t.Parallel()

	fakeClient := &fakeResourceClient{getErr: errors.New(GetError)}
	ops := &IngressRouteOps{
		ResIfc:            fakeClient,
		Namespace:         TestNS,
		ManagedLabelKey:   ManagedBy,
		ManagedLabelValue: Controller,
		ReadOnly:          false,
	}

	u := map[string]interface{}{}

	err := ops.Apply(context.Background(), TestRoute, u)
	require.Error(t, err, "Get error should be returned")
	require.Contains(t, err.Error(), GetError)
}

func TestIngressRouteOpsApplySingleReadOnly(t *testing.T) {
	t.Parallel()

	fakeClient := &fakeResourceClient{}
	ops := &IngressRouteOps{
		ResIfc:            fakeClient,
		Namespace:         TestNS,
		ManagedLabelKey:   ManagedBy,
		ManagedLabelValue: Controller,
		IngressClass:      TraefikIngressClass,
		ReadOnly:          true,
	}

	m := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "test-kind"},
	}

	err := ops.ApplySingle(context.Background(), m, "Middleware")
	require.NoError(t, err, "ReadOnly should return no error")
	require.Equal(t, 0, fakeClient.createCount, "Create should not be called")
}

func TestIngressRouteOpsApplySingleWithEmptyName(t *testing.T) {
	t.Parallel()

	fakeClient := &fakeResourceClient{}
	ops := &IngressRouteOps{
		ResIfc:            fakeClient,
		Namespace:         TestNS,
		ManagedLabelKey:   ManagedBy,
		ManagedLabelValue: Controller,
		IngressClass:      TraefikIngressClass,
		ReadOnly:          false,
	}

	m := map[string]interface{}{
		"metadata": map[string]interface{}{"name": ""},
	}

	err := ops.ApplySingle(context.Background(), m, "Middleware")
	require.NoError(t, err, "Empty name should return nil without calling client")
	require.Equal(t, 0, fakeClient.createCount, "Create should not be called for empty name")
}

func TestIngressRouteOpsApplySingleUpdatesExisting(t *testing.T) {
	t.Parallel()

	fakeClient := &fakeResourceClient{
		existing: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{
					"name":            TestMW,
					"namespace":       TestNS,
					"resourceVersion": "1",
				},
			},
		},
	}
	ops := &IngressRouteOps{
		ResIfc:            fakeClient,
		Namespace:         TestNS,
		ManagedLabelKey:   ManagedBy,
		ManagedLabelValue: Controller,
		IngressClass:      TraefikIngressClass,
		ReadOnly:          false,
	}

	m := map[string]interface{}{
		"metadata": map[string]interface{}{"name": TestMW},
	}

	err := ops.ApplySingle(context.Background(), m, "Middleware")
	require.NoError(t, err, "ApplySingle should succeed for existing resource")
	require.Equal(t, 1, fakeClient.patchCount, "Patch should be called")
}

func TestIngressRouteOpsApplySingleGetError(t *testing.T) {
	t.Parallel()

	fakeClient := &fakeResourceClient{getErr: errors.New(GetError)}
	ops := &IngressRouteOps{
		ResIfc:            fakeClient,
		Namespace:         TestNS,
		ManagedLabelKey:   ManagedBy,
		ManagedLabelValue: Controller,
		IngressClass:      TraefikIngressClass,
		ReadOnly:          false,
	}

	m := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "test-mw"},
	}

	err := ops.ApplySingle(context.Background(), m, "Middleware")
	require.Error(t, err, "Get error should be returned")
	require.Contains(t, err.Error(), GetError)
}

type fakeResourceClient struct {
	resources.ResourceClient
	existing     *unstructured.Unstructured
	getErr       error
	createErr    error
	patchErr     error
	createCount  int
	patchCount   int
	patchBytes   []byte
	patchOptions metav1.PatchOptions
}

func (c *fakeResourceClient) Get(_ context.Context, name string, _ metav1.GetOptions) (*unstructured.Unstructured, error) {
	if c.getErr != nil {
		return nil, c.getErr
	}
	if c.existing != nil {
		return c.existing, nil
	}
	return nil, k8serrors.NewNotFound(schema.GroupResource{Group: "traefik.io", Resource: "ingressroute"}, name)
}

func (c *fakeResourceClient) Create(_ context.Context, obj *unstructured.Unstructured, _ metav1.CreateOptions) (*unstructured.Unstructured, error) {
	c.createCount++
	if c.createErr != nil {
		return nil, c.createErr
	}
	return obj, nil
}

func (c *fakeResourceClient) Patch(_ context.Context, name string, _ types.PatchType, patch []byte, options metav1.PatchOptions) (*unstructured.Unstructured, error) {
	c.patchCount++
	c.patchBytes = append([]byte(nil), patch...)
	c.patchOptions = options
	if c.patchErr != nil {
		return nil, c.patchErr
	}
	if c.existing != nil {
		return c.existing, nil
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{"metadata": map[string]interface{}{"name": name}}}, nil
}

func (*fakeResourceClient) Delete(_ context.Context, _ string, _ metav1.DeleteOptions) error {
	return nil
}

func (*fakeResourceClient) List(_ context.Context, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return nil, nil
}

func (*fakeResourceClient) Watch(_ context.Context, _ metav1.ListOptions) (watch.Interface, error) {
	return nil, nil
}

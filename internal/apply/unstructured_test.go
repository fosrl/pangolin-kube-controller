package apply

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"pangolin-kube-controller/internal/kube/resources"
)

func TestDiffSpecKeys(t *testing.T) {
	t.Parallel()

	existing := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"spec": map[string]interface{}{
				"port": int64(8080),
				"name": "old-svc",
			},
		},
	}
	desired := map[string]interface{}{
		"apiVersion": TraefikAPIVersion,
		"kind":       KindIngressRoute,
		"metadata":   map[string]interface{}{"name": TestName},
		"spec": map[string]interface{}{
			"port": float64(8080),
			"name": "new-svc",
			"new":  "field",
		},
	}

	changed := DiffSpecKeys(existing, desired)
	require.Len(t, changed, 2)
	require.Contains(t, changed, "name")
	require.Contains(t, changed, "new")
}

func TestDiffSpecKeysNoSpecChange(t *testing.T) {
	t.Parallel()

	existing := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"spec": map[string]interface{}{
				"port": int64(8080),
			},
		},
	}
	desired := map[string]interface{}{
		"spec": map[string]interface{}{
			"port": float64(8080),
		},
	}

	changed := DiffSpecKeys(existing, desired)
	require.Len(t, changed, 0)
}

func TestDiffSpecKeysBothNilSpec(t *testing.T) {
	t.Parallel()

	existing := &unstructured.Unstructured{
		Object: map[string]interface{}{},
	}
	desired := map[string]interface{}{}

	changed := DiffSpecKeys(existing, desired)
	require.Len(t, changed, 0)
}

type fakeResourceClientForUnstructured struct {
	resources.ResourceClient
	existing    *unstructured.Unstructured
	getErr      error
	createErr   error
	patchErr    error
	createCount int
	patchCount  int
	patchOpts   []metav1.PatchOptions
}

func (c *fakeResourceClientForUnstructured) Get(_ context.Context, _ string, _ metav1.GetOptions) (*unstructured.Unstructured, error) {
	if c.getErr != nil {
		return nil, c.getErr
	}
	if c.existing != nil {
		return c.existing, nil
	}
	return nil, errors.New("not found")
}

func (c *fakeResourceClientForUnstructured) Create(_ context.Context, obj *unstructured.Unstructured, _ metav1.CreateOptions) (*unstructured.Unstructured, error) {
	c.createCount++
	if c.createErr != nil {
		return nil, c.createErr
	}
	return obj, nil
}

func (c *fakeResourceClientForUnstructured) Patch(_ context.Context, _ string, _ types.PatchType, _ []byte, opts metav1.PatchOptions) (*unstructured.Unstructured, error) {
	c.patchCount++
	c.patchOpts = append(c.patchOpts, opts)
	if c.patchErr != nil {
		return nil, c.patchErr
	}
	return c.existing, nil
}

func TestCreateUnstructuredUsesStableSSAFieldManager(t *testing.T) {
	t.Parallel()

	fakeClient := &fakeResourceClientForUnstructured{}
	u := map[string]interface{}{
		"apiVersion": TraefikAPIVersion,
		"kind":       KindIngressRoute,
		"metadata":   map[string]interface{}{"name": TestName},
	}

	err := CreateUnstructured(context.Background(), fakeClient, u, "IngressRoute")
	require.NoError(t, err)
	require.Zero(t, fakeClient.createCount)
	require.Equal(t, 1, fakeClient.patchCount)
	require.Equal(t, FieldManager, fakeClient.patchOpts[0].FieldManager)
	require.Nil(t, fakeClient.patchOpts[0].Force)
}

func TestCreateUnstructuredWithFieldValidation(t *testing.T) {
	t.Parallel()

	fakeClient := &fakeResourceClientForUnstructured{}
	u := map[string]interface{}{
		"apiVersion": TraefikAPIVersion,
		"kind":       KindTraefikService,
		"metadata":   map[string]interface{}{"name": TestName},
	}

	err := CreateUnstructured(context.Background(), fakeClient, u, "TraefikService")
	require.NoError(t, err)
	require.Zero(t, fakeClient.createCount)
	require.Equal(t, 1, fakeClient.patchCount)
	require.Equal(t, metav1.FieldValidationIgnore, fakeClient.patchOpts[0].FieldValidation)
}

func managedObjectWithSpecOwner(manager string, operation metav1.ManagedFieldsOperationType) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "test"},
		"spec":     map[string]interface{}{"name": "old"},
	}}
	u.SetLabels(map[string]string{managedLabelKey: managedLabelValue})
	u.SetManagedFields([]metav1.ManagedFieldsEntry{{
		Manager:    manager,
		Operation:  operation,
		APIVersion: TraefikAPIVersion,
		FieldsType: "FieldsV1",
		FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:name":{}}}`)},
	}})
	return u
}

func desiredManagedObject() map[string]interface{} {
	return map[string]interface{}{
		"apiVersion": TraefikAPIVersion,
		"kind":       KindTraefikService,
		"metadata": map[string]interface{}{
			"name":   "test",
			"labels": map[string]interface{}{managedLabelKey: managedLabelValue},
		},
		"spec": map[string]interface{}{"name": "new"},
	}
}

func TestSubsequentSpecChangeUsesSSAWithoutForce(t *testing.T) {
	t.Parallel()
	existing := managedObjectWithSpecOwner(FieldManager, metav1.ManagedFieldsOperationApply)
	fakeClient := &fakeResourceClientForUnstructured{existing: existing}

	err := PatchUnstructured(context.Background(), fakeClient, "test", desiredManagedObject(), existing, KindTraefikService, PatchConfig{MetadataConfig: defaultMetadataConfig()})
	require.NoError(t, err)
	require.Equal(t, FieldManager, fakeClient.patchOpts[0].FieldManager)
	require.Nil(t, fakeClient.patchOpts[0].Force)
}

func TestLegacyControllerManagedSpecIsSafelyAdopted(t *testing.T) {
	t.Parallel()
	existing := managedObjectWithSpecOwner("controller", metav1.ManagedFieldsOperationUpdate)
	fakeClient := &fakeResourceClientForUnstructured{existing: existing}

	err := PatchUnstructured(context.Background(), fakeClient, "test", desiredManagedObject(), existing, KindTraefikService, PatchConfig{MetadataConfig: defaultMetadataConfig()})
	require.NoError(t, err)
	require.NotNil(t, fakeClient.patchOpts[0].Force)
	require.True(t, *fakeClient.patchOpts[0].Force)
}

func TestExternalSpecOwnershipIsNotStolen(t *testing.T) {
	t.Parallel()
	existing := managedObjectWithSpecOwner("external-operator", metav1.ManagedFieldsOperationApply)
	fakeClient := &fakeResourceClientForUnstructured{existing: existing}

	err := PatchUnstructured(context.Background(), fakeClient, "test", desiredManagedObject(), existing, KindTraefikService, PatchConfig{MetadataConfig: defaultMetadataConfig()})
	require.NoError(t, err)
	require.Nil(t, fakeClient.patchOpts[0].Force)
}

func TestRepeatedReconcileUsesSameSSAOwnership(t *testing.T) {
	t.Parallel()
	existing := managedObjectWithSpecOwner(FieldManager, metav1.ManagedFieldsOperationApply)
	fakeClient := &fakeResourceClientForUnstructured{existing: existing}

	for range 2 {
		err := PatchUnstructured(context.Background(), fakeClient, "test", desiredManagedObject(), existing, KindTraefikService, PatchConfig{MetadataConfig: defaultMetadataConfig()})
		require.NoError(t, err)
	}
	require.Equal(t, 2, fakeClient.patchCount)
	for _, opts := range fakeClient.patchOpts {
		require.Equal(t, FieldManager, opts.FieldManager)
		require.Nil(t, opts.Force)
	}
}

func TestPatchUnstructured(t *testing.T) {
	t.Parallel()

	fakeClient := &fakeResourceClientForUnstructured{
		existing: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{
					"name": "test",
				},
			},
		},
	}

	existing := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name": "test",
			},
			"spec": map[string]interface{}{
				"port": int64(8080),
			},
		},
	}
	u := map[string]interface{}{
		"metadata": map[string]interface{}{
			"name": "test",
		},
		"spec": map[string]interface{}{
			"port": float64(9090),
		},
	}

	err := PatchUnstructured(context.Background(), fakeClient, "test", u, existing, "IngressRoute", PatchConfig{})
	require.NoError(t, err)
	require.Equal(t, 1, fakeClient.patchCount)
}

func TestPatchUnstructuredWithForce(t *testing.T) {
	t.Parallel()

	fakeClient := &fakeResourceClientForUnstructured{
		existing: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{
					"name": "test",
				},
			},
		},
	}

	existing := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name": "test",
			},
			"spec": map[string]interface{}{
				"port": int64(8080),
			},
		},
	}
	u := map[string]interface{}{
		"metadata": map[string]interface{}{
			"name": "test",
		},
		"spec": map[string]interface{}{
			"port": float64(9090),
		},
	}

	err := PatchUnstructured(context.Background(), fakeClient, "test", u, existing, "IngressRoute", PatchConfig{Force: true})
	require.NoError(t, err)
	require.Equal(t, 1, fakeClient.patchCount)
}

func TestPatchUnstructuredNilExisting(t *testing.T) {
	t.Parallel()

	fakeClient := &fakeResourceClientForUnstructured{}

	u := map[string]interface{}{
		"metadata": map[string]interface{}{
			"name": "test",
		},
	}

	err := PatchUnstructured(context.Background(), fakeClient, "test", u, nil, "IngressRoute", PatchConfig{})
	require.NoError(t, err)
}

func TestApplySSAWithRetrySuccess(t *testing.T) {
	t.Parallel()

	fakeClient := &fakeResourceClientForUnstructured{}
	patchBytes := []byte(`{"metadata":{"name":"test"}}`)

	err := ApplySSAWithRetry(context.Background(), fakeClient, "test", patchBytes, "IngressRoute", SSAOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, fakeClient.patchCount)
}

func TestApplySSAWithRetryNonConflictError(t *testing.T) {
	t.Parallel()

	fakeClient := &fakeResourceClientForUnstructured{
		patchErr: errors.New("not found"),
	}
	patchBytes := []byte(`{"metadata":{"name":"test"}}`)

	err := ApplySSAWithRetry(context.Background(), fakeClient, "test", patchBytes, "IngressRoute", SSAOptions{})
	require.Error(t, err)
	require.Equal(t, 1, fakeClient.patchCount, "Should not retry non-conflict errors")
}

func TestApplySSAWithRetryContextCancelled(t *testing.T) {
	t.Parallel()

	fakeClient := &fakeResourceClientForUnstructured{
		patchErr: k8serrors.NewConflict(schema.GroupResource{Group: "traefik.io", Resource: "ingressroute"}, "test", errors.New("conflict")),
	}
	patchBytes := []byte(`{"metadata":{"name":"test"}}`)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := ApplySSAWithRetry(ctx, fakeClient, "test", patchBytes, "IngressRoute", SSAOptions{Force: true})
	require.Error(t, err)
}

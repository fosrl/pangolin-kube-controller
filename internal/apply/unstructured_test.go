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

func (c *fakeResourceClientForUnstructured) Patch(_ context.Context, _ string, _ types.PatchType, _ []byte, _ metav1.PatchOptions) (*unstructured.Unstructured, error) {
	c.patchCount++
	if c.patchErr != nil {
		return nil, c.patchErr
	}
	return c.existing, nil
}

func TestCreateUnstructured(t *testing.T) {
	t.Parallel()

	fakeClient := &fakeResourceClientForUnstructured{}
	u := map[string]interface{}{
		"apiVersion": TraefikAPIVersion,
		"kind":       KindIngressRoute,
		"metadata":   map[string]interface{}{"name": TestName},
	}

	err := CreateUnstructured(context.Background(), fakeClient, u, "IngressRoute")
	require.NoError(t, err)
	// Objects are created with server-side apply, never with Create.
	require.Equal(t, 0, fakeClient.createCount)
	require.Equal(t, 1, fakeClient.patchCount)
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

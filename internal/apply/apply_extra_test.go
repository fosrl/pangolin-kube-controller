package apply

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	traefikconfig "pangolin-kube-controller/internal/transform/config"
)

var testGVR = schema.GroupVersionResource{
	Group:    traefikconfig.Group,
	Version:  traefikconfig.Version,
	Resource: "middlewares",
}

func newTestUnstructuredOps(t *testing.T) (*UnstructuredOps, *fake.FakeDynamicClient) {
	t.Helper()
	listKinds := map[schema.GroupVersionResource]string{
		testGVR: "MiddlewareList",
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	dyn.PrependReactor("patch", testGVR.Resource, func(action k8stesting.Action) (bool, runtime.Object, error) {
		patchAction := action.(k8stesting.PatchAction)
		obj := &unstructured.Unstructured{}
		if err := obj.UnmarshalJSON(patchAction.GetPatch()); err != nil {
			return true, nil, err
		}
		obj.SetNamespace(action.GetNamespace())
		_, err := dyn.Tracker().Get(testGVR, action.GetNamespace(), obj.GetName())
		switch {
		case k8serrors.IsNotFound(err):
			err = dyn.Tracker().Create(testGVR, obj, action.GetNamespace())
		case err == nil:
			err = dyn.Tracker().Update(testGVR, obj, action.GetNamespace())
		}
		return true, obj, err
	})
	ops := &UnstructuredOps{
		Dyn:       dyn,
		GVR:       testGVR,
		Namespace: TestNS,
	}
	return ops, dyn
}

func testMetadataConfig() MetadataConfig {
	return MetadataConfig{
		ManagedLabelKey:           ManagedLabelKeyFull,
		ManagedLabelValue:         ManagedLabelValueController,
		TraefikInstanceLabelKey:   InstanceLabelKeyFull,
		TraefikInstanceLabelValue: InstanceLabelValueMyInstance,
		ManagedAnnoKey:            ManagedAnnoKeyPangolin,
		ManagedAnnoValue:          ManagedLabelValueController,
	}
}

// TestApplyCreatesNewResource verifies that SSA creates the resource when it
// does not yet exist in the fake client.
func TestApplyCreatesNewResource(t *testing.T) {
	t.Parallel()

	ops, _ := newTestUnstructuredOps(t)
	raw := json.RawMessage(`{"passHostHeader": true}`)

	err := ops.Apply(context.Background(), "my-middleware", raw, testMetadataConfig())
	require.NoError(t, err)
}

// TestApplyWithEmptySpec creates a resource from an empty JSON object spec.
func TestApplyWithEmptySpec(t *testing.T) {
	t.Parallel()

	ops, _ := newTestUnstructuredOps(t)
	raw := json.RawMessage(`{}`)

	err := ops.Apply(context.Background(), "empty-middleware", raw, testMetadataConfig())
	require.NoError(t, err)
}

// TestApplyInvalidJSON returns an error on malformed JSON.
func TestApplyInvalidJSON(t *testing.T) {
	t.Parallel()

	ops, _ := newTestUnstructuredOps(t)
	raw := json.RawMessage(`{not-valid-json`)

	err := ops.Apply(context.Background(), "bad-json", raw, testMetadataConfig())
	require.Error(t, err)
}

// TestApplyUpdateExistingResource calls Apply twice on the same name and
// verifies that the changed spec is reconciled through the same SSA path.
func TestApplyUpdateExistingResourceTriesPatch(t *testing.T) {
	t.Parallel()

	ops, dyn := newTestUnstructuredOps(t)
	raw := json.RawMessage(`{"passHostHeader": true}`)

	// First apply creates the resource through SSA.
	require.NoError(t, ops.Apply(context.Background(), "reused-mw", raw, testMetadataConfig()))

	// Second apply with changed spec uses SSA again.
	raw2 := json.RawMessage(`{"passHostHeader": false}`)
	require.NoError(t, ops.Apply(context.Background(), "reused-mw", raw2, testMetadataConfig()))
	got, err := dyn.Resource(testGVR).Namespace(TestNS).Get(context.Background(), "reused-mw", metav1.GetOptions{})
	require.NoError(t, err)
	value, found, err := unstructured.NestedBool(got.Object, "spec", "passHostHeader")
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, value)
}

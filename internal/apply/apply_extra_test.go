package apply

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"pangolin-kube-controller/internal/testutil"

	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"

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
	testutil.InstallApplyReactor(dyn)
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

// TestApplyCreatesNewResource verifies that Apply creates the resource when it
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

// TestApplyUpdateExistingResource calls Apply twice on the same name: the
// second call should trigger the patch (update) path. Since the fake dynamic
// client does not support SSA ApplyPatchType, the second call is expected to
// return an error (verifying the update path is reached without a panic).
func TestApplyUpdateExistingResourceTriesPatch(t *testing.T) {
	t.Parallel()

	ops, _ := newTestUnstructuredOps(t)
	raw := json.RawMessage(`{"passHostHeader": true}`)

	// First apply – creates the resource.
	require.NoError(t, ops.Apply(context.Background(), "reused-mw", raw, testMetadataConfig()))

	// Second apply with changed spec replaces the spec.
	raw2 := json.RawMessage(`{"passHostHeader": false}`)
	require.NoError(t, ops.Apply(context.Background(), "reused-mw", raw2, testMetadataConfig()))
	got, err := ops.Dyn.Resource(ops.GVR).Namespace(TestNS).Get(context.Background(), "reused-mw", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, false, got.Object["spec"].(map[string]interface{})["passHostHeader"])
}

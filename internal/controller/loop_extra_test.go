package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	fakekube "k8s.io/client-go/kubernetes/fake"

	"pangolin-kube-controller/internal/config"
	prometheus "pangolin-kube-controller/internal/observability/metrics_prometheus"
	applytest "pangolin-kube-controller/internal/testutil"
	"pangolin-kube-controller/internal/transform/testutil"
)

// ---- recordSyncSuccess -------------------------------------------------------

func TestRecordSyncSuccessWithCollector(t *testing.T) {
	t.Parallel()

	mc := prometheus.NewCollector()
	c := NewController(&config.Config{}, nil, nil, mc)

	start := time.Now().Add(-100 * time.Millisecond)
	c.recordSyncSuccess(start)

	// The lastSuccessfulReconcile atomic should be set to a recent time.
	stored := c.lastSuccessfulReconcile.Load()
	require.Greater(t, stored, int64(0), "lastSuccessfulReconcile should be positive after success")
}

func TestRecordSyncSuccessWithoutCollector(t *testing.T) {
	t.Parallel()

	// When collector is nil, recordSyncSuccess must not panic.
	c := NewController(&config.Config{}, nil, nil, nil)
	require.NotPanics(t, func() {
		c.recordSyncSuccess(time.Now())
	})
}

func TestRecordSyncSuccessSetsReadiness(t *testing.T) {
	t.Parallel()

	mc := prometheus.NewCollector()
	c := NewController(&config.Config{}, nil, nil, mc)

	c.recordSyncSuccess(time.Now())
	// After a successful sync, the controller is ready.
	require.True(t, c.Ready(), "controller should be ready after recordSyncSuccess")
}

// ---- processAndApply --------------------------------------------------------

// newTestController builds a controller wired with a fake dynamic client and
// fake Kubernetes client, suitable for reconcile-level tests.
func newTestController(t *testing.T) *Controller {
	t.Helper()

	listKinds := map[schema.GroupVersionResource]string{
		{Group: testutil.TestTraefikGroup, Version: "v1alpha1", Resource: "middlewares"}:          "MiddlewareList",
		{Group: testutil.TestTraefikGroup, Version: "v1alpha1", Resource: "ingressroutes"}:        "IngressRouteList",
		{Group: testutil.TestTraefikGroup, Version: "v1alpha1", Resource: "traefikservices"}:      "TraefikServiceList",
		{Group: testutil.TestTraefikGroup, Version: "v1alpha1", Resource: "serverstransports"}:    "ServersTransportList",
		{Group: testutil.TestTraefikGroup, Version: "v1alpha1", Resource: "ingressroutetcps"}:     "IngressRouteTCPList",
		{Group: testutil.TestTraefikGroup, Version: "v1alpha1", Resource: "ingressrouteudps"}:     "IngressRouteUDPList",
		{Group: testutil.TestTraefikGroup, Version: "v1alpha1", Resource: "serverstransporttcps"}: "ServersTransportTCPList",
		{Group: testutil.TestTraefikGroup, Version: "v1alpha1", Resource: "tlsoptions"}:           "TLSOptionList",
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	applytest.InstallApplyReactor(dyn)
	kube := fakekube.NewClientset()
	mc := prometheus.NewCollector()
	cfg := &config.Config{
		Endpoint:          testEndpoint,
		PollInterval:      10 * time.Millisecond,
		Namespace:         "default",
		ManagedLabelKey:   "app.kubernetes.io/managed-by",
		ManagedLabelValue: "pangolin",
	}
	return NewController(cfg, dyn, kube, mc)
}

func TestProcessAndApplyInvalidJSON(t *testing.T) {
	t.Parallel()

	c := newTestController(t)
	errCount := 0
	ok := c.processAndApply(context.Background(), []byte("not-json{{{"), &errCount)
	require.False(t, ok, "processAndApply should return false on invalid JSON")
	require.Equal(t, 1, errCount, "errCount should be incremented on parse failure")
}

func TestProcessAndApplyEmptyConfig(t *testing.T) {
	t.Parallel()

	c := newTestController(t)
	errCount := 0
	emptyBody := []byte(`{}`)
	ok := c.processAndApply(context.Background(), emptyBody, &errCount)
	require.True(t, ok, "processAndApply should succeed with empty valid JSON config")
	require.Equal(t, 0, errCount, "errCount should remain 0 on success")
}

func TestProcessAndApplyValidConfig(t *testing.T) {
	t.Parallel()

	c := newTestController(t)
	errCount := 0
	body, err := json.Marshal(map[string]interface{}{
		"http": map[string]interface{}{
			"routers":     map[string]interface{}{},
			"middlewares": map[string]interface{}{},
			"services":    map[string]interface{}{},
		},
	})
	require.NoError(t, err)

	ok := c.processAndApply(context.Background(), body, &errCount)
	require.True(t, ok)
	require.Equal(t, 0, errCount)
}

// ---- runLoop with successful reconcile (recordSyncSuccess path) -------------
// Note: The full runLoop path is exercised by TestRunLoopSuccess200Cancels in
// the existing loop_test.go. The tests below verify the recordSyncSuccess
// behaviour in isolation.

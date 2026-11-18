//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"pangolin-kube-controller/internal/controller"
	prometheus "pangolin-kube-controller/internal/observability/metrics_prometheus"
	testschema "pangolin-kube-controller/internal/testschema"
	traefikconfig "pangolin-kube-controller/internal/transform/config"
)

func traefikGVR(resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: traefikconfig.Group, Version: traefikconfig.Version, Resource: resource}
}

func loadPangolinFixture(t *testing.T) *traefikconfig.Config {
	t.Helper()
	b, err := os.ReadFile(testschema.TestDataPath("traefik-configs", "pangolin-1.26", "full.json"))
	require.NoError(t, err)
	var cfg traefikconfig.Config
	require.NoError(t, json.Unmarshal(b, &cfg))
	return &cfg
}

func countManaged(t *testing.T, ns, resource string) int {
	t.Helper()
	l, err := dynCli.Resource(traefikGVR(resource)).Namespace(ns).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	return len(l.Items)
}

// TestPangolin126FixtureOnAPIServer applies a Pangolin 1.26 config against a
// real API server with the Traefik CRDs installed: every object must pass
// validation, a second apply must not change anything, and removing entries
// must garbage-collect their objects.
func TestPangolin126FixtureOnAPIServer(t *testing.T) {
	ctx := context.Background()
	ns := newNamespace(t)
	ctrl := controller.NewController(newTestConfig(ns), dynCli, kubeCli, prometheus.NewCollector())

	require.NoError(t, ctrl.ApplyConfigForTest(ctx, loadPangolinFixture(t)))

	want := map[string]int{
		"middlewares": 7, "ingressroutes": 6, "traefikservices": 4, "serverstransports": 1,
		"ingressroutetcps": 1, "ingressrouteudps": 1, "serverstransporttcps": 1, "tlsoptions": 1,
	}
	for res, n := range want {
		require.Equal(t, n, countManaged(t, ns, res), res)
	}
	svcs, err := kubeCli.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	// 1-app (2 buckets), 2-api (2), 3-lan (1), 4-maintenance (1: the
	// pangolin Service lives in another namespace here), tcp (2), udp (1).
	require.Len(t, svcs.Items, 9)
	_, err = kubeCli.CoreV1().Secrets(ns).Get(ctx, "resource-1-mtls-client-ca", metav1.GetOptions{})
	require.NoError(t, err)

	// Re-applying an unchanged config is a no-op for the API server.
	app, err := dynCli.Resource(traefikGVR("traefikservices")).Namespace(ns).Get(ctx, "1-app-service", metav1.GetOptions{})
	require.NoError(t, err)
	es, err := kubeCli.DiscoveryV1().EndpointSlices(ns).Get(ctx, "pkc-1-app-service-0-v4-41000", metav1.GetOptions{})
	require.NoError(t, err)
	require.NoError(t, ctrl.ApplyConfigForTest(ctx, loadPangolinFixture(t)))
	app2, err := dynCli.Resource(traefikGVR("traefikservices")).Namespace(ns).Get(ctx, "1-app-service", metav1.GetOptions{})
	require.NoError(t, err)
	es2, err := kubeCli.DiscoveryV1().EndpointSlices(ns).Get(ctx, "pkc-1-app-service-0-v4-41000", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, app.GetResourceVersion(), app2.GetResourceVersion())
	require.Equal(t, es.ResourceVersion, es2.ResourceVersion)

	// Drop the mTLS resource, its TLS option and the UDP section.
	reduced := loadPangolinFixture(t)
	delete(reduced.HTTP.Routers, "1-app-router")
	delete(reduced.HTTP.Routers, "1-app-router-redirect")
	delete(reduced.HTTP.Services, "1-app-service")
	delete(reduced.HTTP.Middlewares, "resource-1-mtls-headers")
	reduced.TLS = nil
	reduced.UDP = nil
	require.NoError(t, ctrl.ApplyConfigForTest(ctx, reduced))
	require.Equal(t, 0, countManaged(t, ns, "tlsoptions"))
	require.Equal(t, 0, countManaged(t, ns, "ingressrouteudps"))
	require.Equal(t, 4, countManaged(t, ns, "ingressroutes"))
	svcs, err = kubeCli.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, svcs.Items, 6)
	_, err = kubeCli.CoreV1().Secrets(ns).Get(ctx, "resource-1-mtls-client-ca", metav1.GetOptions{})
	require.Error(t, err)
}

// TestAlpha1ObjectIsMigratedToApply creates an IngressRoute the way
// 0.1.0-alpha.1 did (a plain Create, recorded under the "controller" Update
// manager) with a TLS block Pangolin no longer emits, and checks the next
// apply removes it. Without migrating field ownership the stale block would
// survive every server-side apply.
func TestAlpha1ObjectIsMigratedToApply(t *testing.T) {
	ctx := context.Background()
	ns := newNamespace(t)
	cfg := newTestConfig(ns)
	res := dynCli.Resource(traefikGVR("ingressroutes")).Namespace(ns)

	legacy := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": traefikconfig.GroupVersion,
		"kind":       "IngressRoute",
		"metadata": map[string]interface{}{
			"name":        "r1",
			"labels":      map[string]interface{}{cfg.ManagedLabelKey: cfg.ManagedLabelValue},
			"annotations": map[string]interface{}{cfg.ManagedAnnoKey: cfg.ManagedAnnoValue},
		},
		"spec": map[string]interface{}{
			"entryPoints": []interface{}{"websecure"},
			"routes": []interface{}{map[string]interface{}{
				"match": "Host(`a.example.com`)", "kind": "Rule",
				"services": []interface{}{map[string]interface{}{"name": "svc1", "kind": "TraefikService"}},
			}},
			"tls": map[string]interface{}{"certResolver": "old"},
		},
	}}
	_, err := res.Create(ctx, legacy, metav1.CreateOptions{FieldManager: "controller"})
	require.NoError(t, err)

	pangolin := &traefikconfig.Config{HTTP: traefikconfig.HTTPConfig{
		Routers:  map[string]json.RawMessage{"r1": json.RawMessage("{\"entryPoints\":[\"web\"],\"rule\":\"Host(`a.example.com`)\",\"service\":\"svc1\"}")},
		Services: map[string]json.RawMessage{"svc1": json.RawMessage(`{"loadBalancer":{"servers":[{"url":"http://10.0.0.1:80"}]}}`)},
	}}
	ctrl := controller.NewController(cfg, dynCli, kubeCli, prometheus.NewCollector())
	require.NoError(t, ctrl.ApplyConfigForTest(ctx, pangolin))

	got, err := res.Get(ctx, "r1", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotContains(t, specOf(t, got), "tls")
	require.Equal(t, []interface{}{"web"}, specOf(t, got)["entryPoints"])
	for _, mf := range got.GetManagedFields() {
		require.False(t, mf.Manager == "controller" && mf.Operation == metav1.ManagedFieldsOperationUpdate, "legacy manager still owns fields")
	}
}

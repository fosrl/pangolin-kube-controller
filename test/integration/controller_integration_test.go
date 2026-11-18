//go:build integration

// This integration test file is only built when the 'integration' build tag is supplied.
// Run with: go test -tags=integration ./test/integration/ ... (plus any needed env/kube setup)

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"

	"pangolin-kube-controller/internal/config"
	"pangolin-kube-controller/internal/controller"
	prometheus "pangolin-kube-controller/internal/observability/metrics_prometheus"
	tst "pangolin-kube-controller/internal/testutil"
	traefikconfig "pangolin-kube-controller/internal/transform/config"
)

// Helper to reduce duplication of HTTP handlers in integration tests.
func okHandlerWithPayload(t *testing.T, b []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(b); err != nil {
			require.NoError(t, err)
		}
	})
}

const traefikGroup = traefikconfig.Group
const traefikVersion = traefikconfig.Version

// Increase deletion timeout to avoid flakes on slower CI runners.
const namespaceDeleteTimeout = 60 * time.Second

var (
	middlewareGroupVersionResource = schema.GroupVersionResource{Group: traefikGroup, Version: traefikVersion, Resource: "middlewares"}
	ingressRouteGVR                = schema.GroupVersionResource{Group: traefikGroup, Version: traefikVersion, Resource: "ingressroutes"}
	traefikServiceGVR              = schema.GroupVersionResource{Group: traefikGroup, Version: traefikVersion, Resource: "traefikservices"}
)

type httpConfig struct {
	HTTP traefikconfig.HTTPConfig `json:"http"`
}

func newTestConfig(ns string) *config.Config {
	c := config.LoadFromEnv()
	c.Namespace = ns
	// Slightly slower polling in integration tests to reduce API pressure in CI environments.
	c.PollInterval = 500 * time.Millisecond
	c.MaxBackoff = time.Second
	c.LeaderEnabled = false
	c.ReadOnly = false
	// Allow plaintext HTTP config endpoints in integration tests (httptest.Server).
	c.AllowInsecureHTTP = true
	c.IngressClass = tst.DefaultIngressClass
	c.ManagedLabelKey = tst.ManagedLabelKey
	c.ManagedLabelValue = tst.ManagedLabelValue
	c.ManagedAnnoKey = tst.ManagedAnnoKey
	c.ManagedAnnoValue = tst.ManagedAnnoValue
	c.SSAForce = false
	return c
}

func randomSuffix(t *testing.T, numBytes int) string {
	randomBytes := make([]byte, numBytes)
	_, err := rand.Read(randomBytes)
	require.NoError(t, err)
	return hex.EncodeToString(randomBytes)
}

// newNamespace creates a unique test namespace and waits for full deletion on cleanup
// to avoid AlreadyExists races with terminating namespaces.
func newNamespace(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("pangolin-test-%d-%s", time.Now().Unix(), randomSuffix(t, 3))
	_, err := kubeCli.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		err := kubeCli.CoreV1().Namespaces().Delete(context.Background(), name, metav1.DeleteOptions{})
		if err != nil {
			t.Logf("cleanup: failed to delete namespace %s: %v", name, err)
		}
		ctx := context.Background()
		err = wait.PollUntilContextTimeout(
			ctx,
			200*time.Millisecond,
			namespaceDeleteTimeout,
			true,
			func(ctx context.Context) (done bool, err error) {
				_, getErr := kubeCli.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
				if apierrors.IsNotFound(getErr) {
					return true, nil
				}
				if getErr != nil {
					return false, getErr
				}
				return false, nil
			},
		)
		if err != nil {
			t.Logf("cleanup: namespace %s may not have been fully deleted: %v", name, err)
		}
	})
	return name
}

func startController(t *testing.T, cfg *config.Config) (context.CancelFunc, func()) {
	t.Helper()
	// Allow integration tests to use local httptest servers over plaintext HTTP.
	// The production code disallows plaintext CONFIG_ENDPOINT by default; tests
	// that serve config over HTTP must opt into insecure mode.
	t.Setenv("CONFIG_ALLOW_INSECURE_HTTP", "true")
	m := prometheus.NewCollector()
	ctrl := controller.NewController(cfg, dynCli, kubeCli, m)
	ctx, cancel := context.WithCancel(context.Background())
	go ctrl.Run(ctx)
	stop := func() {
		cancel()
		time.Sleep(100 * time.Millisecond)
	}
	return cancel, stop
}

func eventuallyGet(t *testing.T, res dynamic.ResourceInterface, name string) *unstructured.Unstructured {
	t.Helper()
	var out *unstructured.Unstructured
	require.NoError(t, wait.PollUntilContextTimeout(context.Background(), 100*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		obj, err := res.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil // resource not yet created, keep polling
			}
			return false, err
		}
		out = obj
		return true, nil
	}))
	return out
}

func specOf(t *testing.T, u *unstructured.Unstructured) map[string]interface{} {
	specVal, specExists := u.UnstructuredContent()["spec"]
	if !specExists {
		require.FailNowf(t, "specOf: assertion failed: spec field missing",
			"Object kind=%q name=%q namespace=%q; content: %#v",
			u.GetKind(), u.GetName(), u.GetNamespace(), u.UnstructuredContent())
	}
	m, ok := specVal.(map[string]interface{})
	if !ok {
		specJSON, err := json.MarshalIndent(specVal, "", "  ")
		if err != nil {
			// Fallback to basic type info if JSON marshaling fails
			require.FailNowf(t, "specOf: assertion failed: spec field is not map[string]interface{}",
				"kind=%q name=%q namespace=%q, type=%T",
				u.GetKind(), u.GetName(), u.GetNamespace(), specVal)
		}
		require.FailNowf(t, "specOf: assertion failed: spec field is not map[string]interface{}",
			"kind=%q name=%q namespace=%q\nspec value (formatted):\n%s",
			u.GetKind(), u.GetName(), u.GetNamespace(), string(specJSON))
	}
	return m
}

func TestApplyAllThreeKinds(t *testing.T) {
	ns := newNamespace(t)

	// Prepare TraefikConfig JSON.
	cfgBody := map[string]interface{}{
		"http": map[string]interface{}{
			"middlewares": map[string]interface{}{
				"mw1": map[string]interface{}{
					"headers": map[string]interface{}{
						"customRequestHeaders": map[string]interface{}{"X-Foo": "bar"},
					},
				},
			},
			"services": map[string]interface{}{
				"svc1": map[string]interface{}{
					"loadBalancer": map[string]interface{}{
						"servers": []interface{}{map[string]interface{}{"url": "http://10.0.0.1:80"}}, // NOSONAR
					},
				},
			},
			"routers": map[string]interface{}{
				"r1": map[string]interface{}{
					"entryPoints": []interface{}{"web"},
					"rule":        "Host(`example.com`)",
					"service":     "svc1",
					"middlewares": []interface{}{"mw1"},
					"priority":    10,
					"tls":         map[string]interface{}{"options": "default"},
				},
			},
		},
	}
	b, err := json.Marshal(cfgBody)
	require.NoError(t, err)

	handlerErrCh := make(chan error, 1)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(b); err != nil {
			select {
			case handlerErrCh <- fmt.Errorf("error writing response: %v", err):
			default:
			}
		}
	}))
	defer hs.Close()
	t.Cleanup(func() {
		// After closing the server, check for any handler error.
		select {
		case herr := <-handlerErrCh:
			t.Errorf("HTTP server handler error: %v", herr)
		default:
		}
	})
	cfg := newTestConfig(ns)
	cfg.Endpoint = hs.URL

	_, stop := startController(t, cfg)
	defer stop()

	mwRes := dynCli.Resource(middlewareGroupVersionResource).Namespace(ns)
	irRes := dynCli.Resource(ingressRouteGVR).Namespace(ns)
	tsRes := dynCli.Resource(traefikServiceGVR).Namespace(ns)

	mw := eventuallyGet(t, mwRes, "mw1")
	ir := eventuallyGet(t, irRes, "r1")
	ts := eventuallyGet(t, tsRes, "svc1")

	require.NotNil(t, mw)
	require.NotNil(t, ir)
	require.NotNil(t, ts)

	// Basic spec mapping assertions for IngressRoute.
	spec := specOf(t, ir)
	eps, ok := spec["entryPoints"].([]interface{})
	require.True(t, ok, fmt.Sprintf("entryPoints field is missing or is not []interface{}, actual type: %T", spec["entryPoints"]))
	require.Contains(t, eps, "web")
	routes, ok := spec["routes"].([]interface{})
	require.True(t, ok, fmt.Sprintf("spec['routes'] is not a []interface{}: got %T", spec["routes"]))
	require.Len(t, routes, 1)
	route, ok := routes[0].(map[string]interface{})
	require.True(t, ok, fmt.Sprintf("routes[0] is not a map[string]interface{}: got %T, value: %v", routes[0], routes[0]))
	require.Equal(t, "Rule", route["kind"])                 // kind=Rule
	require.Equal(t, "Host(`example.com`)", route["match"]) // match=rule

	// Services reference kind TraefikService.
	svcRefs, _ := route["services"].([]interface{})
	require.Len(t, svcRefs, 1)
	ref, _ := svcRefs[0].(map[string]interface{})
	require.Equal(t, "TraefikService", ref["kind"])
	require.Equal(t, "svc1", ref["name"])

	// Middlewares references.
	mwRefs, _ := route["middlewares"].([]interface{})
	require.Contains(t, mwRefs, map[string]interface{}{"name": "mw1"})

	// TLS options are a TLSOption reference object in the CRD.
	tls, _ := spec["tls"].(map[string]interface{})
	require.Equal(t, map[string]interface{}{"name": "default"}, tls["options"])

	// The TraefikService points at a generated Service whose EndpointSlice
	// carries the server address.
	requireBackend(t, ns, ts, "pkc-svc1", "10.0.0.1", 80)
}

// requireBackend checks that a TraefikService has one weighted child pointing
// at the named generated Service, and that the Service's EndpointSlice holds
// addr:port under the port name Traefik matches on.
func requireBackend(t *testing.T, ns string, ts *unstructured.Unstructured, svcName, addr string, port int32) {
	t.Helper()
	weighted, _ := specOf(t, ts)["weighted"].(map[string]interface{})
	children, _ := weighted["services"].([]interface{})
	require.Len(t, children, 1)
	child, _ := children[0].(map[string]interface{})
	require.Equal(t, svcName, child["name"])
	require.Equal(t, "Service", child["kind"])
	require.Equal(t, "web", child["port"])

	_, err := kubeCli.CoreV1().Services(ns).Get(context.Background(), svcName, metav1.GetOptions{})
	require.NoError(t, err)
	slices, err := kubeCli.DiscoveryV1().EndpointSlices(ns).List(context.Background(), metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=" + svcName})
	require.NoError(t, err)
	require.Len(t, slices.Items, 1)
	es := slices.Items[0]
	require.Equal(t, "web", *es.Ports[0].Name)
	require.Equal(t, port, *es.Ports[0].Port)
	require.Equal(t, []string{addr}, es.Endpoints[0].Addresses)
}

func TestIdempotentSSANoSpecChangeOnReapply(t *testing.T) {
	ns := newNamespace(t)
	services := map[string]interface{}{
		"svc1": map[string]interface{}{
			"loadBalancer": map[string]interface{}{
				"servers": []interface{}{
					map[string]interface{}{"url": "http://10.0.0.1"}, // NOSONAR
				},
			},
		},
	}

	cfgBody := map[string]interface{}{
		"http": map[string]interface{}{
			"middlewares": map[string]interface{}{"mw1": map[string]interface{}{}},
			"services":    services,
			"routers":     map[string]interface{}{"r1": map[string]interface{}{"entryPoints": []interface{}{"web"}, "rule": "Path(`/`)", "service": "svc1"}},
		},
	}
	b, err := json.Marshal(cfgBody)
	require.NoError(t, err)
	configHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(b); err != nil {
			t.Fatalf("failed to write response: %v", err)
		}
	})
	hs := httptest.NewServer(configHandler)
	defer hs.Close()

	cfg := newTestConfig(ns)
	cfg.Endpoint = hs.URL
	_, stop := startController(t, cfg)
	defer stop()

	res := dynCli.Resource(ingressRouteGVR).Namespace(ns)
	first := eventuallyGet(t, res, "r1")
	firstSpec := specOf(t, first)

	// Wait for at least one more reconcile cycle.
	time.Sleep(cfg.PollInterval * 2)

	second := eventuallyGet(t, res, "r1")
	secondSpec := specOf(t, second)

	if diff := cmp.Diff(firstSpec, secondSpec); diff != "" {
		t.Fatalf("IngressRoute spec changed after reapply (-want +got):\n%s", diff)
	}
}

func TestGCDeletesStaleManagedPreservesForeign(t *testing.T) {
	ns := newNamespace(t)
	cfgBody := map[string]interface{}{"http": map[string]interface{}{"middlewares": map[string]interface{}{"mw1": map[string]interface{}{}}}}
	b, err := json.Marshal(cfgBody)
	require.NoError(t, err)
	hs := httptest.NewServer(okHandlerWithPayload(t, b))
	defer hs.Close()

	cfg := newTestConfig(ns)
	cfg.Endpoint = hs.URL
	cfg.GCGracePeriod = 0

	mwRes := dynCli.Resource(middlewareGroupVersionResource).Namespace(ns)

	// Pre-create a managed-but-stale object.
	stale := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "traefik.io/v1alpha1",
		"kind":       "Middleware",
		"metadata": map[string]interface{}{
			"name":      "stale",
			"namespace": ns,
			"labels":    map[string]interface{}{cfg.ManagedLabelKey: cfg.ManagedLabelValue},
			"annotations": map[string]interface{}{
				cfg.ManagedAnnoKey: cfg.ManagedAnnoValue,
			},
		},
		"spec": map[string]interface{}{},
	}}
	_, err = mwRes.Create(context.Background(), stale, metav1.CreateOptions{})
	require.NoError(t, err)

	// Pre-create a foreign object (no managed markers).
	foreign := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "traefik.io/v1alpha1",
		"kind":       "Middleware",
		"metadata": map[string]interface{}{
			"name":      "foreign",
			"namespace": ns,
		},
		"spec": map[string]interface{}{},
	}}
	_, err = mwRes.Create(context.Background(), foreign, metav1.CreateOptions{})
	require.NoError(t, err)

	_, stop := startController(t, cfg)
	defer stop()

	// mw1 should be created; stale should be deleted; foreign should remain.
	_ = eventuallyGet(t, mwRes, "mw1")

	// Eventually stale is gone.
	require.NoError(t, wait.PollUntilContextTimeout(context.Background(), 100*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		_, err := mwRes.Get(ctx, "stale", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}))

	// Foreign remains.
	_, err = mwRes.Get(context.Background(), "foreign", metav1.GetOptions{})
	require.NoError(t, err)
}

// newTestServer returns an httptest.Server writing the given byte body with HTTP 200.
func newTestServer(body []byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(body); err != nil {
			http.Error(w, fmt.Sprintf("failed to write response: %v", err), http.StatusInternalServerError)
		}
	}))
}

func TestReadOnlyNoWrites(t *testing.T) {
	ns := newNamespace(t)
	cfgBody := map[string]interface{}{"http": map[string]interface{}{"middlewares": map[string]interface{}{"mw1": map[string]interface{}{}}}}
	b, err := json.Marshal(cfgBody)
	require.NoError(t, err)
	hs := newTestServer(b)
	defer hs.Close()

	cfg := newTestConfig(ns)
	cfg.ReadOnly = true
	cfg.Endpoint = hs.URL
	_, stop := startController(t, cfg)
	defer stop()

	mwRes := dynCli.Resource(middlewareGroupVersionResource).Namespace(ns)

	// Ensure it does not get created within a reasonable time.
	require.Error(t, wait.PollUntilContextTimeout(context.Background(), 100*time.Millisecond, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		_, err := mwRes.Get(ctx, "mw1", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil // Not created yet, keep waiting
		}
		return err == nil, err // If found (err==nil), return (true, nil); otherwise propagate error
	}))
}

func TestTraefikServiceFillDefaultLBURL(t *testing.T) {
	ns := newNamespace(t)
	cfgBody := map[string]interface{}{
		"http": map[string]interface{}{
			"services": map[string]interface{}{
				"svc1": map[string]interface{}{},
			},
			"routers": map[string]interface{}{
				"r1": map[string]interface{}{
					"rule":    "Path(`/`)",
					"service": "svc1",
				},
			},
		},
	}
	b, err := json.Marshal(cfgBody)
	require.NoError(t, err)
	hs := newTestServer(b)
	defer hs.Close()

	cfg := newTestConfig(ns)
	cfg.Endpoint = hs.URL
	// Default test Traefik LB URL using a placeholder IP address.
	// NOSONAR: Hardcoded IP in tests is intentional and safe for testing purposes.
	cfg.TraefikLBURL = "http://1.2.3.4:8080" // NOSONAR
	_, stop := startController(t, cfg)
	defer stop()

	res := dynCli.Resource(traefikServiceGVR).Namespace(ns)
	ts := eventuallyGet(t, res, "svc1")
	requireBackend(t, ns, ts, "pkc-svc1", "1.2.3.4", 8080)

	// A router without entry points is written without the field.
	ir := eventuallyGet(t, dynCli.Resource(ingressRouteGVR).Namespace(ns), "r1")
	require.NotContains(t, specOf(t, ir), "entryPoints")
}

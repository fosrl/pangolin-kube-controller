package e2e

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	fakekube "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"pangolin-kube-controller/internal/config"
	"pangolin-kube-controller/internal/controller"
	prometheus "pangolin-kube-controller/internal/observability/metrics_prometheus"
	testschema "pangolin-kube-controller/internal/testschema"
	tst "pangolin-kube-controller/internal/testutil"
	traefikconfig "pangolin-kube-controller/internal/transform/config"
)

const pangolinFixtureCRDVersion = "v3.7.19"

var traefikResources = map[string]string{
	"middlewares":          "MiddlewareList",
	"ingressroutes":        "IngressRouteList",
	"traefikservices":      "TraefikServiceList",
	"serverstransports":    "ServersTransportList",
	"ingressroutetcps":     "IngressRouteTCPList",
	"ingressrouteudps":     "IngressRouteUDPList",
	"serverstransporttcps": "ServersTransportTCPList",
	"tlsoptions":           "TLSOptionList",
}

type harness struct {
	t    *testing.T
	ctx  context.Context
	dyn  *fake.FakeDynamicClient
	kube *fakekube.Clientset
	ctrl *controller.Controller
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	listKinds := map[schema.GroupVersionResource]string{}
	for res, list := range traefikResources {
		listKinds[schema.GroupVersionResource{Group: traefikconfig.Group, Version: traefikconfig.Version, Resource: res}] = list
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	tst.InstallApplyReactor(dyn)
	kube := fakekube.NewClientset()

	cfg := config.LoadFromEnv()
	cfg.Namespace = tst.TestNamespace
	cfg.IngressClass = tst.DefaultIngressClass
	cfg.ManagedLabelKey = "app.kubernetes.io/managed-by"
	cfg.ManagedLabelValue = tst.ManagedLabelValue
	cfg.ManagedAnnoKey = tst.ManagedAnnoKey
	cfg.ManagedAnnoValue = tst.ManagedAnnoValue
	cfg.TraefikInstanceLabelKey = "traefik.io/instance"
	cfg.TraefikInstanceLabelValue = "edge"
	cfg.ReadOnly = false
	return &harness{t: t, ctx: context.Background(), dyn: dyn, kube: kube, ctrl: controller.NewController(cfg, dyn, kube, prometheus.NewCollector())}
}

func (h *harness) apply(cfg *traefikconfig.Config) {
	h.t.Helper()
	require.NoError(h.t, h.ctrl.ApplyConfigForTest(h.ctx, cfg))
}

func (h *harness) list(resource string) []unstructured.Unstructured {
	h.t.Helper()
	gvr := schema.GroupVersionResource{Group: traefikconfig.Group, Version: traefikconfig.Version, Resource: resource}
	l, err := h.dyn.Resource(gvr).Namespace(tst.TestNamespace).List(h.ctx, metav1.ListOptions{})
	require.NoError(h.t, err)
	return l.Items
}

func (h *harness) get(resource, name string) map[string]interface{} {
	h.t.Helper()
	gvr := schema.GroupVersionResource{Group: traefikconfig.Group, Version: traefikconfig.Version, Resource: resource}
	u, err := h.dyn.Resource(gvr).Namespace(tst.TestNamespace).Get(h.ctx, name, metav1.GetOptions{})
	require.NoError(h.t, err, "%s %s", resource, name)
	return u.Object
}

func (h *harness) services() map[string]corev1.Service {
	l, err := h.kube.CoreV1().Services(tst.TestNamespace).List(h.ctx, metav1.ListOptions{})
	require.NoError(h.t, err)
	out := map[string]corev1.Service{}
	for _, s := range l.Items {
		out[s.Name] = s
	}
	return out
}

func (h *harness) slices() map[string]discoveryv1.EndpointSlice {
	l, err := h.kube.DiscoveryV1().EndpointSlices(tst.TestNamespace).List(h.ctx, metav1.ListOptions{})
	require.NoError(h.t, err)
	out := map[string]discoveryv1.EndpointSlice{}
	for _, s := range l.Items {
		out[s.Name] = s
	}
	return out
}

func loadFixture(t *testing.T) *traefikconfig.Config {
	t.Helper()
	b, err := os.ReadFile(testschema.TestDataPath("traefik-configs", "pangolin-1.26", "full.json"))
	require.NoError(t, err)
	var cfg traefikconfig.Config
	require.NoError(t, json.Unmarshal(b, &cfg))
	return &cfg
}

func nested(t *testing.T, obj map[string]interface{}, fields ...string) interface{} {
	t.Helper()
	v, found, err := unstructured.NestedFieldNoCopy(obj, fields...)
	require.NoError(t, err)
	require.True(t, found, "missing %s", strings.Join(fields, "."))
	return v
}

// TestPangolin126ObjectsMatchTraefikCRDs applies a Pangolin 1.26 config and
// validates every generated Traefik object against the Traefik 3.7.19 CRD
// schemas, unknown fields included: the API server prunes those silently,
// which is how invalid TraefikServices used to look applied.
func TestPangolin126ObjectsMatchTraefikCRDs(t *testing.T) {
	h := newHarness(t)
	h.apply(loadFixture(t))

	crds, err := testschema.LoadTraefikCRDs(TraefikVersion(pangolinFixtureCRDVersion))
	require.NoError(t, err)
	crdMap := testschema.MapCRDByKind(crds)
	total := 0
	for res := range traefikResources {
		for i := range h.list(res) {
			obj := h.list(res)[i]
			total++
			require.Empty(t, testschema.Validate(&obj, crdMap), "%s %s", obj.GetKind(), obj.GetName())
			require.Equal(t, "edge", obj.GetLabels()["traefik.io/instance"], "%s %s instance label", obj.GetKind(), obj.GetName())
		}
	}
	require.Equal(t, 22, total)
}

func TestPangolin126HTTPServices(t *testing.T) {
	h := newHarness(t)
	h.apply(loadFixture(t))
	svcs := h.services()
	slices := h.slices()

	// Two targets on one Newt site share its address. Traefik de-duplicates
	// endpoints by address, so they must sit in different Services.
	app := h.get("traefikservices", "1-app-service")
	children := nested(t, app, "spec", "weighted", "services").([]interface{})
	require.Len(t, children, 2)
	first := children[0].(map[string]interface{})
	require.Equal(t, "pkc-1-app-service-0", first["name"])
	require.Equal(t, "web", first["port"])
	require.EqualValues(t, 2, first["weight"])
	require.Equal(t, "p2c", first["strategy"])
	require.Equal(t, "p_sticky", nested(t, first, "sticky", "cookie", "name"))
	require.Equal(t, "p_sticky_pool", nested(t, app, "spec", "weighted", "sticky", "cookie", "name"))

	es := slices["pkc-1-app-service-0-v4-41000"]
	require.Equal(t, "pkc-1-app-service-0", es.Labels[discoveryv1.LabelServiceName])
	require.Equal(t, "web", *es.Ports[0].Name)
	require.Equal(t, []string{"100.89.128.8"}, es.Endpoints[0].Addresses)
	require.Contains(t, slices, "pkc-1-app-service-0-v4-44602")
	require.Contains(t, slices, "pkc-1-app-service-1-v4-44603")
	require.Equal(t, corev1.ClusterIPNone, svcs["pkc-1-app-service-0"].Spec.ClusterIP)
	require.Equal(t, "web", svcs["pkc-1-app-service-0"].Spec.Ports[0].Name)

	// Different schemes need different Services; IPv6 and hostnames get their own slices.
	api := nested(t, h.get("traefikservices", "2-api-service"), "spec", "weighted", "services").([]interface{})
	require.Len(t, api, 2)
	schemes := map[string]bool{}
	for _, c := range api {
		m := c.(map[string]interface{})
		schemes[m["scheme"].(string)] = true
		require.Equal(t, "2-transport", m["serversTransport"])
	}
	require.Equal(t, map[string]bool{"http": true, "https": true}, schemes)
	require.Equal(t, discoveryv1.AddressTypeIPv6, slices["pkc-2-api-service-1-v6-8080"].AddressType)
	require.Equal(t, discoveryv1.AddressTypeFQDN, slices["pkc-2-api-service-1-fqdn-8080"].AddressType)

	// A server that is already a Service in the namespace is referenced directly.
	maint := nested(t, h.get("traefikservices", "4-maintenance-service"), "spec", "weighted", "services").([]interface{})
	require.Len(t, maint, 1)
	ref := maint[0].(map[string]interface{})
	require.Equal(t, "pangolin", ref["name"])
	require.EqualValues(t, 3002, ref["port"])
	require.Equal(t, true, ref["passHostHeader"])
	require.NotContains(t, svcs, "pkc-4-maintenance-service")
}

func TestPangolin126RoutesAndTLS(t *testing.T) {
	h := newHarness(t)
	h.apply(loadFixture(t))

	app := h.get("ingressroutes", "1-app-router")
	require.Equal(t, map[string]interface{}{"name": "resource-1-mtls"}, nested(t, app, "spec", "tls", "options"))
	require.Equal(t, "traefik", app["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})["kubernetes.io/ingress.class"])
	route := nested(t, app, "spec", "routes").([]interface{})[0].(map[string]interface{})
	var mws []string
	for _, m := range route["middlewares"].([]interface{}) {
		mws = append(mws, m.(map[string]interface{})["name"].(string))
	}
	require.Equal(t, []string{"resource-1-mtls-headers", "badger", "crowdsec@file", "1-app-headers-middleware"}, mws)

	redirect := h.get("ingressroutes", "7-redirect-old-router")
	svc := nested(t, redirect, "spec", "routes").([]interface{})[0].(map[string]interface{})["services"].([]interface{})[0].(map[string]interface{})
	require.Equal(t, "noop@internal", svc["name"])
	require.Equal(t, "TraefikService", svc["kind"])

	chain := nested(t, h.get("middlewares", "auth-chain"), "spec", "chain", "middlewares")
	require.Equal(t, []interface{}{map[string]interface{}{"name": "badger"}, map[string]interface{}{"name": "1-app-headers-middleware"}}, chain)

	opt := h.get("tlsoptions", "resource-1-mtls")
	require.Equal(t, []interface{}{"resource-1-mtls-client-ca"}, nested(t, opt, "spec", "clientAuth", "secretNames"))
	_, hasCAFiles, _ := unstructured.NestedFieldNoCopy(opt, "spec", "clientAuth", "caFiles")
	require.False(t, hasCAFiles)
	secret, err := h.kube.CoreV1().Secrets(tst.TestNamespace).Get(h.ctx, "resource-1-mtls-client-ca", metav1.GetOptions{})
	require.NoError(t, err)
	require.Contains(t, string(secret.Data["ca.crt"]), "-----BEGIN CERTIFICATE-----")
}

func TestPangolin126TCPAndUDP(t *testing.T) {
	h := newHarness(t)
	h.apply(loadFixture(t))

	ssh := h.get("ingressroutetcps", "5-ssh-router")
	require.Equal(t, "traefik", ssh["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})["kubernetes.io/ingress.class"])
	route := nested(t, ssh, "spec", "routes").([]interface{})[0].(map[string]interface{})
	require.Equal(t, "HostSNI(`*`)", route["match"])
	services := route["services"].([]interface{})
	require.Len(t, services, 2, "two ports on one site address need two Services")
	for _, s := range services {
		m := s.(map[string]interface{})
		require.Equal(t, "pp-transport-v1", m["serversTransport"])
		require.Equal(t, "tcp", m["port"])
	}
	require.EqualValues(t, 1, nested(t, h.get("serverstransporttcps", "pp-transport-v1"), "spec", "proxyProtocol", "version"))

	dns := h.get("ingressrouteudps", "6-dns-router")
	udpSvcs := nested(t, dns, "spec", "routes").([]interface{})[0].(map[string]interface{})["services"].([]interface{})
	require.Len(t, udpSvcs, 1)
	require.EqualValues(t, 2, udpSvcs[0].(map[string]interface{})["weight"])
	svc := h.services()["pkc-udp-6-dns-service"]
	require.Equal(t, corev1.ProtocolUDP, svc.Spec.Ports[0].Protocol)
}

func TestPangolin126GarbageCollectionAndIdempotence(t *testing.T) {
	h := newHarness(t)
	cfg := loadFixture(t)
	h.apply(cfg)
	h.apply(cfg)
	before := len(h.services())

	reduced := loadFixture(t)
	delete(reduced.HTTP.Routers, "1-app-router")
	delete(reduced.HTTP.Routers, "1-app-router-redirect")
	delete(reduced.HTTP.Services, "1-app-service")
	delete(reduced.HTTP.Middlewares, "resource-1-mtls-headers")
	reduced.TLS = nil
	reduced.UDP = nil
	h.apply(reduced)

	svcs := h.services()
	require.Len(t, svcs, before-3)
	require.NotContains(t, svcs, "pkc-1-app-service-0")
	require.NotContains(t, svcs, "pkc-udp-6-dns-service")
	for name := range h.slices() {
		require.False(t, strings.HasPrefix(name, "pkc-1-app-service"), name)
	}
	require.Empty(t, h.list("tlsoptions"))
	require.Empty(t, h.list("ingressrouteudps"))
	_, err := h.kube.CoreV1().Secrets(tst.TestNamespace).Get(h.ctx, "resource-1-mtls-client-ca", metav1.GetOptions{})
	require.Error(t, err)
	require.Len(t, h.list("ingressroutes"), 4)
}

// TestPangolin126BrokenEntryKeepsExistingObjects checks that one malformed
// Pangolin service does not delete what is already serving traffic.
func TestPangolin126BrokenEntryKeepsExistingObjects(t *testing.T) {
	h := newHarness(t)
	h.apply(loadFixture(t))

	broken := loadFixture(t)
	broken.HTTP.Services["3-lan-service"] = json.RawMessage(`{"loadBalancer":{"servers":[{"url":"ftp://10.0.0.6"}]}}`)
	h.apply(broken)

	h.get("traefikservices", "3-lan-service")
	require.Contains(t, h.services(), "pkc-3-lan-service")
}

// TestUnmanagedServiceIsNotTakenOver ensures the controller refuses to
// overwrite a Service it did not create.
func TestUnmanagedServiceIsNotTakenOver(t *testing.T) {
	h := newHarness(t)
	_, err := h.kube.CoreV1().Services(tst.TestNamespace).Create(h.ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "pkc-3-lan-service", Namespace: tst.TestNamespace},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	err = h.ctrl.ApplyConfigForTest(h.ctx, loadFixture(t))
	require.ErrorContains(t, err, "not managed by this controller")
}

// TestRejectedObjectDoesNotBlockOthers makes the API server reject one
// TraefikService and checks that the rest of the config is still applied and
// the error is reported, in both reconcile modes.
func TestRejectedObjectDoesNotBlockOthers(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		h := newHarness(t)
		h.ctrl = controller.NewController(func() *config.Config {
			cfg := config.LoadFromEnv()
			cfg.Namespace = tst.TestNamespace
			cfg.IngressClass = tst.DefaultIngressClass
			cfg.ManagedLabelKey = "app.kubernetes.io/managed-by"
			cfg.ManagedLabelValue = tst.ManagedLabelValue
			cfg.ManagedAnnoKey = tst.ManagedAnnoKey
			cfg.ManagedAnnoValue = tst.ManagedAnnoValue
			cfg.ReconcileParallel = parallel
			return cfg
		}(), h.dyn, h.kube, prometheus.NewCollector())
		h.dyn.PrependReactor("patch", "traefikservices", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if action.(k8stesting.PatchAction).GetName() == "1-app-service" {
				return true, nil, apierrors.NewInvalid(schema.GroupKind{Group: "traefik.io", Kind: "TraefikService"}, "1-app-service", nil)
			}
			return false, nil, nil
		})
		err := h.ctrl.ApplyConfigForTest(h.ctx, loadFixture(t))
		require.ErrorContains(t, err, "1-app-service")
		require.Len(t, h.list("traefikservices"), 3, "parallel=%v", parallel)
		require.Len(t, h.list("ingressroutes"), 6, "parallel=%v", parallel)
		require.Len(t, h.list("ingressroutetcps"), 1, "parallel=%v", parallel)
	}
}

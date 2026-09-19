package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	fakekube "k8s.io/client-go/kubernetes/fake"

	"pangolin-kube-controller/internal/config"
	"pangolin-kube-controller/internal/controller"
	prometheus "pangolin-kube-controller/internal/observability/metrics_prometheus"
	testschema "pangolin-kube-controller/internal/testschema"
	tst "pangolin-kube-controller/internal/testutil"
	traefikconfig "pangolin-kube-controller/internal/transform/config"
)

// Offline E2E: parse extended.json, run controller.applyConfig with fake clients,
// collect Traefik CRDs, validate against schemas, and compare to golden (when added).
const (
	traefikGroupLocal         = traefikconfig.Group
	traefikExtendedVersionTag = "v3.5.0"
)

func TestOfflineE2EExtendedHTTPTCPUDP(t *testing.T) {
	ctx := context.Background()
	// Fake clients
	sch := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		{Group: traefikGroupLocal, Version: "v1alpha1", Resource: "middlewares"}:          "MiddlewareList",
		{Group: traefikGroupLocal, Version: "v1alpha1", Resource: "ingressroutes"}:        "IngressRouteList",
		{Group: traefikGroupLocal, Version: "v1alpha1", Resource: "traefikservices"}:      "TraefikServiceList",
		{Group: traefikGroupLocal, Version: "v1alpha1", Resource: "serverstransports"}:    "ServersTransportList",
		{Group: traefikGroupLocal, Version: "v1alpha1", Resource: "ingressroutetcps"}:     "IngressRouteTCPList",
		{Group: traefikGroupLocal, Version: "v1alpha1", Resource: "ingressrouteudps"}:     "IngressRouteUDPList",
		{Group: traefikGroupLocal, Version: "v1alpha1", Resource: "serverstransporttcps"}: "ServersTransportTCPList",
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(sch, listKinds)
	tst.EnableSSAUpsert(dyn)
	kube := fakekube.NewClientset()

	cfg := config.LoadFromEnv()
	cfg.Namespace = tst.TestNamespace
	cfg.IngressClass = tst.DefaultIngressClass
	cfg.ManagedLabelKey = tst.ManagedLabelKey
	cfg.ManagedLabelValue = tst.ManagedLabelValue
	cfg.ManagedAnnoKey = tst.ManagedAnnoKey
	cfg.ManagedAnnoValue = tst.ManagedAnnoValue
	cfg.ReadOnly = false

	c := controller.NewController(cfg, dyn, kube, prometheus.NewCollector())

	b, err := os.ReadFile(testschema.TestDataPath("traefik-configs", traefikExtendedVersionTag, "extended.json"))
	require.NoError(t, err)

	var tc traefikconfig.Config
	require.NoError(t, json.Unmarshal(b, &tc))

	// Apply through controller path
	require.NoError(t, c.ApplyConfigForTest(ctx, &tc))

	// Validate CRDs against schemas
	crds, err := testschema.LoadTraefikCRDs(TraefikVersion(traefikExtendedVersionTag))
	require.NoError(t, err)
	crdMap := testschema.MapCRDByKind(crds)

	// Collect and validate all Traefik CRD kinds we manage
	ns := cfg.Namespace

	// IngressRoute
	gvrIR := schema.GroupVersionResource{Group: traefikGroupLocal, Version: tst.TraefikVersion, Resource: "ingressroutes"}
	irList, err := dyn.Resource(gvrIR).Namespace(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	for i := range irList.Items {
		obj := &irList.Items[i]
		errs := testschema.Validate(obj, crdMap)
		require.Len(t, errs, 0)
	}
	// IngressRouteTCP
	gvrIRTCP := schema.GroupVersionResource{Group: traefikGroupLocal, Version: tst.TraefikVersion, Resource: "ingressroutetcps"}
	irtcp, err := dyn.Resource(gvrIRTCP).Namespace(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	for i := range irtcp.Items {
		require.Len(t, testschema.Validate(&irtcp.Items[i], crdMap), 0)
	}
	// IngressRouteUDP
	gvrIRUDP := schema.GroupVersionResource{Group: traefikGroupLocal, Version: tst.TraefikVersion, Resource: "ingressrouteudps"}
	irudp, err := dyn.Resource(gvrIRUDP).Namespace(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	for i := range irudp.Items {
		require.Len(t, testschema.Validate(&irudp.Items[i], crdMap), 0)
	}
	// Middleware
	gvrMW := schema.GroupVersionResource{Group: traefikGroupLocal, Version: tst.TraefikVersion, Resource: "middlewares"}
	mws, err := dyn.Resource(gvrMW).Namespace(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	for i := range mws.Items {
		require.Len(t, testschema.Validate(&mws.Items[i], crdMap), 0)
	}
	// TraefikService
	gvrTS := schema.GroupVersionResource{Group: traefikGroupLocal, Version: tst.TraefikVersion, Resource: "traefikservices"}
	ts, err := dyn.Resource(gvrTS).Namespace(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	for i := range ts.Items {
		require.Len(t, testschema.Validate(&ts.Items[i], crdMap), 0)
	}
	// ServersTransport
	gvrST := schema.GroupVersionResource{Group: traefikGroupLocal, Version: tst.TraefikVersion, Resource: "serverstransports"}
	st, err := dyn.Resource(gvrST).Namespace(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	for i := range st.Items {
		require.Len(t, testschema.Validate(&st.Items[i], crdMap), 0)
	}

	// Optionally, build a deterministic multi-doc YAML of CRDs for future golden compare
	var docs []map[string]interface{}
	appendList := func(list *unstructured.UnstructuredList) {
		for i := range list.Items {
			m := list.Items[i].UnstructuredContent()
			m = testschema.ScrubObjectMeta(m)
			docs = append(docs, m)
		}
	}
	appendList(irList)
	appendList(irtcp)
	appendList(irudp)
	appendList(mws)
	appendList(ts)
	appendList(st)
	yml, err := testschema.DeterministicYAML(docs)
	require.NoError(t, err)
	golden := GoldenPath(traefikExtendedVersionTag)
	if UpdateGoldenEnabled() {
		require.NoError(t, os.MkdirAll(filepath.Dir(golden), 0o750))
		require.NoError(t, os.WriteFile(golden, yml, 0o644))
		return
	}
	if _, err := os.Stat(golden); os.IsNotExist(err) {
		t.Skipf("golden %s not found; set UPDATE_GOLDEN=1 to create", golden)
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err)
	require.Equal(t, string(want), string(yml))
}

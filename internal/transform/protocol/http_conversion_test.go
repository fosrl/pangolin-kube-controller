package protocol

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	testschema "pangolin-kube-controller/internal/testschema"
	tst "pangolin-kube-controller/internal/testutil"
	traefikconfig "pangolin-kube-controller/internal/transform/config"
	"pangolin-kube-controller/internal/transform/routing"
	"pangolin-kube-controller/internal/transform/sanitize"
	"pangolin-kube-controller/internal/transform/testutil"
)

// TestHTTPConversion_Extended_Basic validates a subset of the extended JSON
// is transformed into valid Traefik CRDs (Middleware, TraefikService, IngressRoute)
// and passes offline CRD schema validation (traefik.io/v1alpha1).
func TestHTTPConversionExtendedBasic(t *testing.T) {
	b, err := os.ReadFile(testschema.TestDataPath("traefik-configs", "v3.5.0", "extended.json"))
	require.NoError(t, err)

	var cfg traefikconfig.Config
	require.NoError(t, json.Unmarshal(b, &cfg))

	sanitized, err := sanitize.SanitizeTraefikConfig(&cfg)
	require.NoError(t, err)

	// Load embedded CRDs for schema validation (default: the real Traefik v3.5.6 CRDs).
	version := os.Getenv("TRAEFIK_CRD_VERSION")
	if version == "" {
		version = "v3.5.6"
	}
	crds, err := testschema.LoadTraefikCRDs(version)
	require.NoError(t, err, "load CRDs for "+version)
	crdMap := testschema.MapCRDByKind(crds)

	// Pick specific resources to validate.
	mwName := "redirect-to-https"
	req, ok := sanitized.HTTP.Middlewares[mwName]
	require.True(t, ok, "expected middleware %s", mwName)
	namespace := tst.TestNamespace
	mwObj, err := testutil.BuildTraefikObject("Middleware", mwName, namespace, req)
	require.NoError(t, err)
	require.Len(t, testschema.Validate(mwObj, crdMap), 0)

	// Pangolin's loadBalancer services are not valid TraefikServices; they
	// must go through TransformHTTPServices first.
	tsName := "1-test-1-service"
	art := TransformHTTPServices(nil, namespace, sanitized.HTTP.Services)
	require.Empty(t, art.Failed)
	tsRaw, ok := art.TraefikServices[tsName]
	require.True(t, ok, "expected service %s", tsName)
	tsObj, err := testutil.BuildTraefikObject("TraefikService", tsName, namespace, tsRaw)
	require.NoError(t, err)
	require.Len(t, testschema.Validate(tsObj, crdMap), 0)

	rName := "1-test-1-router"
	rRaw, ok := sanitized.HTTP.Routers[rName]
	require.True(t, ok, "expected router %s", rName)
	u, err := routing.TransformRouterToIngressRoute(rName, rRaw, routing.RouterConfig{
		Namespace:         tst.TestNamespace,
		ManagedLabelKey:   tst.ManagedLabelKey,
		ManagedLabelValue: tst.ManagedLabelValue,
		ManagedAnnoKey:    tst.ManagedAnnoKey,
		ManagedAnnoValue:  tst.ManagedAnnoValue,
		IngressClass:      tst.DefaultIngressClass,
	})
	require.NoError(t, err)
	ir, err := testutil.ToUnstructuredFromMap(u)
	require.NoError(t, err)
	require.Len(t, testschema.Validate(ir, crdMap), 0)

	// Spot-check route contents
	content := ir.UnstructuredContent()
	specRaw, ok := content["spec"]
	require.True(t, ok, "spec should be present")
	spec, ok := specRaw.(map[string]interface{})
	require.True(t, ok, "spec should be a map")
	routes, ok := spec["routes"].([]interface{})
	require.True(t, ok, "routes should be a slice")
	require.NotEmpty(t, routes)
	r0, ok := routes[0].(map[string]interface{})
	require.True(t, ok, "route should be a map")
	require.Equal(t, "Rule", r0["kind"])                     // kind set to Rule
	require.Equal(t, "Host(`whoami.fosrl.io`)", r0["match"]) // match comes from rule
}

// build helpers replaced by test_obj_helpers.go

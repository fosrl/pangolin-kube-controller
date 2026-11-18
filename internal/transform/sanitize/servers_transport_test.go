package sanitize

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"pangolin-kube-controller/internal/testschema"
	tst "pangolin-kube-controller/internal/testutil"
	traefikconfig "pangolin-kube-controller/internal/transform/config"
)

func TestHTTPServersTransportMappingAndValidation(t *testing.T) {
	b, err := os.ReadFile(testschema.TestDataPath("traefik-configs", "v3.5.0", "extended.json"))
	require.NoError(t, err)

	var cfg traefikconfig.Config
	require.NoError(t, json.Unmarshal(b, &cfg))

	sanitized, err := SanitizeTraefikConfig(&cfg)
	require.NoError(t, err)

	// Ensure transports present and referenced by services are rewritten to sanitized names
	require.GreaterOrEqual(t, len(sanitized.HTTP.ServersTransports), 1)

	// Pick a service that references a serversTransport in the fixture
	svcName := "3-sdfasdf-prefix-asdf-prefix-rt3asdf-service"
	svcRaw := sanitized.HTTP.Services[svcName]
	var spec map[string]interface{}
	require.NoError(t, json.Unmarshal(svcRaw, &spec))
	lbAny, ok := spec["loadBalancer"]
	require.True(t, ok, "expected spec.loadBalancer to be present")
	lb, ok := lbAny.(map[string]interface{})
	require.True(t, ok, "expected spec.loadBalancer to be an object")
	stAny, ok := lb["serversTransport"]
	require.True(t, ok, "expected spec.loadBalancer.serversTransport to be present")
	st, ok := stAny.(string)
	require.True(t, ok, "expected spec.loadBalancer.serversTransport to be a string")
	require.NotEmpty(t, st)
	// The sanitized transports map should contain the rewritten key
	if _, ok := sanitized.HTTP.ServersTransports[st]; !ok {
		t.Fatalf("expected sanitized serversTransport %q to exist", st)
	}

	// Validate one ServersTransport object against CRD schema
	version := os.Getenv("TRAEFIK_CRD_VERSION")
	if version == "" {
		version = "v3.5.6"
	}
	crds, err := testschema.LoadTraefikCRDs(version)
	require.NoError(t, err)
	crdMap := testschema.MapCRDByKind(crds)

	// Build unstructured for the selected serversTransport
	stSpec := sanitized.HTTP.ServersTransports[st]
	var stObj map[string]interface{}
	require.NoError(t, json.Unmarshal(stSpec, &stObj))
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": traefikconfig.GroupVersion,
		"kind":       "ServersTransport",
		"metadata":   map[string]interface{}{"name": st, "namespace": tst.TestNamespace},
		"spec":       stObj,
	}}
	// Schema is permissive but presence of the CRD must be enforced
	require.Len(t, testschema.Validate(u, crdMap), 0)
}

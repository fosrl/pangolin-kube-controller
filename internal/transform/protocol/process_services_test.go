package protocol

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	discoveryv1 "k8s.io/api/discovery/v1"

	"pangolin-kube-controller/internal/config"
)

// ---- ProcessServices --------------------------------------------------------

func TestProcessServicesNilInput(t *testing.T) {
	t.Parallel()

	result := ProcessServices(&config.Config{}, nil)
	require.Nil(t, result, "nil input should produce nil output")
}

func TestProcessServicesEmptyMap(t *testing.T) {
	t.Parallel()

	result := ProcessServices(&config.Config{}, map[string]json.RawMessage{})
	require.NotNil(t, result)
	require.Empty(t, result)
}

func TestProcessServicesNonEmptySpec(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"loadBalancer":{"servers":[{"url":"http://backend.svc:80"}]}}`)
	services := map[string]json.RawMessage{"my-service": raw}
	result := ProcessServices(&config.Config{}, services)
	require.Contains(t, result, "my-service")
	require.NotNil(t, result["my-service"])
}

func TestProcessServicesEmptySpecWithNoURL(t *testing.T) {
	t.Parallel()

	// Empty spec and no configured LB URL → raw returned unchanged.
	raw := json.RawMessage(`{}`)
	services := map[string]json.RawMessage{"empty-svc": raw}
	result := ProcessServices(&config.Config{}, services)
	require.Contains(t, result, "empty-svc")
}

func TestProcessServicesEmptySpecWithLBURL(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		TraefikLBURL: "http://traefik.traefik.svc:80",
	}
	raw := json.RawMessage(`{}`)
	services := map[string]json.RawMessage{"auto-svc": raw}
	result := ProcessServices(cfg, services)
	require.Contains(t, result, "auto-svc")
	require.NotNil(t, result["auto-svc"])
	// The spec should have been rewritten with a loadBalancer.servers entry.
	var spec map[string]interface{}
	require.NoError(t, json.Unmarshal(result["auto-svc"], &spec))
	require.Contains(t, spec, "loadBalancer")
}

func TestProcessServicesMultipleEntries(t *testing.T) {
	t.Parallel()

	services := map[string]json.RawMessage{
		"svc-a": json.RawMessage(`{"loadBalancer":{"servers":[{"url":"http://a.default.svc"}]}}`),
		"svc-b": json.RawMessage(`{}`),
	}
	result := ProcessServices(&config.Config{}, services)
	require.Len(t, result, 2)
	require.Contains(t, result, "svc-a")
	require.Contains(t, result, "svc-b")
}

// ---- kubeServiceTarget.equals -----------------------------------------------

func TestKubeServiceTargetEqualsIdentical(t *testing.T) {
	t.Parallel()

	a := &kubeServiceTarget{name: "svc", namespace: "ns", port: 80, scheme: "http"}
	b := &kubeServiceTarget{name: "svc", namespace: "ns", port: 80, scheme: "http"}
	require.True(t, a.equals(b))
}

func TestKubeServiceTargetEqualsDifferentPort(t *testing.T) {
	t.Parallel()

	a := &kubeServiceTarget{name: "svc", namespace: "ns", port: 80, scheme: "http"}
	b := &kubeServiceTarget{name: "svc", namespace: "ns", port: 443, scheme: "http"}
	require.False(t, a.equals(b))
}

func TestKubeServiceTargetEqualsDifferentScheme(t *testing.T) {
	t.Parallel()

	a := &kubeServiceTarget{name: "svc", namespace: "ns", port: 443, scheme: "http"}
	b := &kubeServiceTarget{name: "svc", namespace: "ns", port: 443, scheme: "https"}
	require.False(t, a.equals(b))
}

func TestKubeServiceTargetEqualsDifferentNamespace(t *testing.T) {
	t.Parallel()

	a := &kubeServiceTarget{name: "svc", namespace: "ns-a", port: 80, scheme: "http"}
	b := &kubeServiceTarget{name: "svc", namespace: "ns-b", port: 80, scheme: "http"}
	require.False(t, a.equals(b))
}

func TestKubeServiceTargetEqualsNilReceiver(t *testing.T) {
	t.Parallel()

	var a *kubeServiceTarget
	b := &kubeServiceTarget{name: "svc"}
	require.False(t, a.equals(b))
}

func TestKubeServiceTargetEqualsNilOther(t *testing.T) {
	t.Parallel()

	a := &kubeServiceTarget{name: "svc"}
	require.False(t, a.equals(nil))
}

func TestKubeServiceTargetEqualsBothNil(t *testing.T) {
	t.Parallel()

	var a *kubeServiceTarget
	require.False(t, a.equals(nil))
}

// ---- processEmptyService (LB URL building from IP/scheme/port) --------------

func TestProcessServicesLBFromIPAndPort(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		TraefikLBScheme: "http",
		TraefikLBIP:     "10.0.0.1",
		TraefikLBPort:   "9090",
	}
	raw := json.RawMessage(`{}`)
	services := map[string]json.RawMessage{"ip-svc": raw}
	result := ProcessServices(cfg, services)
	require.Contains(t, result, "ip-svc")
	// Spec should contain the constructed URL.
	var spec map[string]interface{}
	require.NoError(t, json.Unmarshal(result["ip-svc"], &spec))
	require.Contains(t, spec, "loadBalancer")
}

func TestProcessServicesLBInvalidURL(t *testing.T) {
	t.Parallel()

	// A non-http/https scheme should be rejected by processEmptyService.
	cfg := &config.Config{
		TraefikLBURL: "ftp://10.0.0.1:21",
	}
	raw := json.RawMessage(`{}`)
	services := map[string]json.RawMessage{"ftp-svc": raw}
	result := ProcessServices(cfg, services)
	// The raw should be returned unchanged (warning logged, no rewrite).
	require.Contains(t, result, "ftp-svc")
	require.Equal(t, string(raw), string(result["ftp-svc"]))
}

// ---- processNonEmptyService (LoadBalancer conversion path) ------------------

func TestProcessServicesKubeServiceConversion(t *testing.T) {
	t.Parallel()

	// An HTTP service pointing to a Kubernetes-style svc FQDN should be
	// converted to a weighted service spec.
	raw := json.RawMessage(`{
		"loadBalancer": {
			"servers": [
				{"url": "http://my-svc.my-ns.svc:8080"}
			]
		}
	}`)
	services := map[string]json.RawMessage{"k8s-svc": raw}
	result := ProcessServices(&config.Config{}, services)
	require.Contains(t, result, "k8s-svc")

	var spec map[string]interface{}
	require.NoError(t, json.Unmarshal(result["k8s-svc"], &spec))
	// After conversion, loadBalancer should be replaced by weighted.
	require.Contains(t, spec, "weighted")
	require.NotContains(t, spec, "loadBalancer")
}

func TestProcessHTTPServicesExternalTunnelTarget(t *testing.T) {
	t.Parallel()

	const (
		serviceName = "svc"
		port        = 8443
		kubeName    = "svc-8443"
	)
	services := map[string]json.RawMessage{
		serviceName: json.RawMessage(`{"loadBalancer":{"servers":[{"url":"https://192.0.2.1:8443"}],"serversTransport":"transport","sticky":{"cookie":{"name":"sticky"}}}}`),
	}

	processed, kubeServices, endpointSlices, err := ProcessHTTPServices(&config.Config{}, services, "traefik")
	require.NoError(t, err)
	require.Len(t, kubeServices, 1)
	require.Len(t, endpointSlices, 1)

	service := kubeServices[0]
	require.Equal(t, kubeName, service.Name)
	require.Equal(t, "traefik", service.Namespace)
	require.Equal(t, "None", service.Spec.ClusterIP)
	require.EqualValues(t, port, service.Spec.Ports[0].Port)
	require.Equal(t, "http", service.Labels[ArtifactProtocolLabel])

	endpointSlice := endpointSlices[0]
	require.Equal(t, discoveryv1.AddressTypeIPv4, endpointSlice.AddressType)
	require.Equal(t, []string{"192.0.2.1"}, endpointSlice.Endpoints[0].Addresses)
	require.Equal(t, kubeName, endpointSlice.Labels[discoveryv1.LabelServiceName])

	var spec map[string]interface{}
	require.NoError(t, json.Unmarshal(processed[serviceName], &spec))
	require.NotContains(t, spec, "loadBalancer")
	weighted := spec["weighted"].(map[string]interface{})
	entries := weighted["services"].([]interface{})
	entry := entries[0].(map[string]interface{})
	require.Equal(t, kubeName, entry["name"])
	require.Equal(t, "traefik", entry["namespace"])
	require.Equal(t, "Service", entry["kind"])
	require.Equal(t, "https", entry["scheme"])
	require.Equal(t, "transport", entry["serversTransport"])
	require.Contains(t, entry, "sticky")
	require.EqualValues(t, port, entry["port"])
}

func TestProcessServicesPreservesNonConvertible(t *testing.T) {
	t.Parallel()

	// A service with multiple different targets (non-uniform) should NOT be
	// converted; the raw spec is returned as-is.
	raw := json.RawMessage(`{
		"loadBalancer": {
			"servers": [
				{"url": "http://svc-a.ns.svc:80"},
				{"url": "http://svc-b.ns.svc:80"}
			]
		}
	}`)
	services := map[string]json.RawMessage{"multi-svc": raw}
	result := ProcessServices(&config.Config{}, services)
	require.Contains(t, result, "multi-svc")
	// Should be returned unmodified.
	var spec map[string]interface{}
	require.NoError(t, json.Unmarshal(result["multi-svc"], &spec))
	require.NotContains(t, spec, "weighted", "non-uniform targets must not be converted")
}

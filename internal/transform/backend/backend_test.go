package backend

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

func TestParseURL(t *testing.T) {
	s, err := ParseURL("http://100.89.128.4:44602")
	require.NoError(t, err)
	require.Equal(t, Server{Scheme: "http", Host: "100.89.128.4", Port: 44602}, s)

	s, err = ParseURL("https://Example.LAN")
	require.NoError(t, err)
	require.Equal(t, Server{Scheme: "https", Host: "example.lan", Port: 443}, s)

	s, err = ParseURL("h2c://[fd00::5]:8080/")
	require.NoError(t, err)
	require.Equal(t, Server{Scheme: "h2c", Host: "fd00::5", Port: 8080}, s)

	for _, bad := range []string{"ftp://x:1", "http://:80", "http://x:0", "http://x:1/path"} {
		_, err := ParseURL(bad)
		require.Error(t, err, bad)
	}
}

func TestParseAddress(t *testing.T) {
	s, err := ParseAddress("[fd00::1]:53")
	require.NoError(t, err)
	require.Equal(t, Server{Host: "fd00::1", Port: 53}, s)
	_, err = ParseAddress("10.0.0.1")
	require.Error(t, err)
	_, err = ParseAddress("10.0.0.1:70000")
	require.Error(t, err)
}

func TestServiceNameIsDNS1035(t *testing.T) {
	for _, base := range []string{"1-app-service", strings.Repeat("9-very-long-name-", 10), "x"} {
		n := ServiceName(base)
		require.Empty(t, validation.IsDNS1035Label(n), n)
	}
	long := strings.Repeat("a", 80)
	require.NotEqual(t, ServiceName(long+"1"), ServiceName(long+"2"))
}

func TestBuildSplitsSharedAddresses(t *testing.T) {
	buckets := Build(Spec{
		Namespace: "ns", Base: "1-svc", PortName: "web", Protocol: corev1.ProtocolTCP,
		Servers: []Server{
			{Scheme: "http", Host: "100.89.0.4", Port: 1000},
			{Scheme: "http", Host: "100.89.0.4", Port: 1001},
			{Scheme: "http", Host: "100.89.0.5", Port: 1000},
			{Scheme: "http", Host: "100.89.0.4", Port: 1000}, // duplicate
		},
	})
	require.Len(t, buckets, 2)
	require.Equal(t, "pkc-1-svc-0", buckets[0].Service.Name)
	require.Equal(t, 2, buckets[0].Weight)
	require.Len(t, buckets[0].Slices, 1, "same family and port share a slice")
	require.Len(t, buckets[0].Slices[0].Endpoints, 2)
	require.Equal(t, 1, buckets[1].Weight)

	for _, b := range buckets {
		require.Equal(t, corev1.ClusterIPNone, b.Service.Spec.ClusterIP)
		require.Nil(t, b.Service.Spec.Selector)
		for _, es := range b.Slices {
			require.Equal(t, b.Service.Name, es.Labels[discoveryv1.LabelServiceName])
			require.Equal(t, EndpointSliceManagedBy, es.Labels[discoveryv1.LabelManagedBy])
			require.Equal(t, "web", *es.Ports[0].Name, "Traefik skips slices whose port has no name")
		}
	}
}

func TestBuildSeparatesSchemesAndFamilies(t *testing.T) {
	buckets := Build(Spec{
		Namespace: "ns", Base: "svc", PortName: "web", Protocol: corev1.ProtocolTCP,
		Servers: []Server{
			{Scheme: "https", Host: "10.0.0.1", Port: 443},
			{Scheme: "http", Host: "10.0.0.1", Port: 80},
			{Scheme: "http", Host: "fd00::1", Port: 80},
			{Scheme: "http", Host: "app.lan", Port: 8080},
		},
	})
	require.Len(t, buckets, 2)
	require.Equal(t, "https", buckets[0].Scheme)
	require.Equal(t, "http", buckets[1].Scheme)
	types := map[discoveryv1.AddressType]bool{}
	for _, es := range buckets[1].Slices {
		types[es.AddressType] = true
	}
	require.Equal(t, map[discoveryv1.AddressType]bool{discoveryv1.AddressTypeIPv4: true, discoveryv1.AddressTypeIPv6: true, discoveryv1.AddressTypeFQDN: true}, types)
}

func TestBuildWithoutServersKeepsService(t *testing.T) {
	buckets := Build(Spec{Namespace: "ns", Base: "svc", PortName: "web", Protocol: corev1.ProtocolTCP})
	require.Len(t, buckets, 1)
	require.Empty(t, buckets[0].Slices)
	require.Equal(t, "pkc-svc", buckets[0].Service.Name)
}

func TestIsClusterServiceHost(t *testing.T) {
	name, ns, ok := IsClusterServiceHost("pangolin.prod.svc.cluster.local")
	require.True(t, ok)
	require.Equal(t, "pangolin", name)
	require.Equal(t, "prod", ns)
	_, _, ok = IsClusterServiceHost("pangolin.prod.example.com")
	require.False(t, ok)
}

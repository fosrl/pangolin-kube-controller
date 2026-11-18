// Package backend turns the server lists of Pangolin's Traefik dynamic
// configuration into Kubernetes Services and EndpointSlices that Traefik's
// kubernetescrd provider can resolve.
//
// Traefik never dials a loadBalancer server list from a CRD: an IngressRoute,
// IngressRouteTCP or IngressRouteUDP can only reference Kubernetes Services
// (or TraefikServices built on top of them). Pangolin's backends are raw IP
// addresses or hostnames, so every one of them needs a selectorless Service
// whose EndpointSlices carry the addresses.
//
// Two details of Traefik's resolver shape the output:
//
//   - It matches EndpointSlice ports to the Service port by name and skips a
//     slice whose port has no name, so every port here is named.
//   - It de-duplicates endpoints by address alone, ignoring the port. Two
//     targets on the same Newt site share the site's address and differ only
//     in port, so they cannot live in one Service. Servers are split into
//     buckets in which every address is unique, and each bucket becomes its
//     own Service.
package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"pangolin-kube-controller/internal/transform/sanitize"
)

const (
	// EndpointSliceManagedBy is the endpointslice.kubernetes.io/managed-by value
	// set on generated slices, so the in-tree EndpointSlice controllers leave
	// them alone.
	EndpointSliceManagedBy = "pangolin-kube-controller"

	// ServiceNamePrefix starts every generated Service name. Service names must
	// be DNS-1035 labels, which may not start with a digit, and Pangolin's
	// service names start with the numeric resource id.
	ServiceNamePrefix = "pkc-"

	maxServiceNameLength = validation.DNS1035LabelMaxLength
	nameHashLength       = 10
)

// Server is one parsed loadBalancer server.
type Server struct {
	Scheme string // http, https or h2c for HTTP servers; empty for TCP/UDP
	Host   string // IP literal or hostname, without brackets
	Port   int32
}

// IsIP reports whether the server address is an IP literal.
func (s Server) IsIP() bool { return net.ParseIP(s.Host) != nil }

func (s Server) addressType() discoveryv1.AddressType {
	ip := net.ParseIP(s.Host)
	switch {
	case ip == nil:
		return discoveryv1.AddressTypeFQDN
	case ip.To4() != nil:
		return discoveryv1.AddressTypeIPv4
	default:
		return discoveryv1.AddressTypeIPv6
	}
}

// ParseURL parses an HTTP loadBalancer server URL such as
// "http://100.89.128.4:44602". A missing port defaults from the scheme.
func ParseURL(raw string) (Server, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return Server{}, fmt.Errorf("parse server url %q: %w", raw, err)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https", "h2c":
	default:
		return Server{}, fmt.Errorf("server url %q: unsupported scheme %q", raw, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return Server{}, fmt.Errorf("server url %q: missing host", raw)
	}
	if u.Path != "" && u.Path != "/" {
		return Server{}, fmt.Errorf("server url %q: paths are not supported on Kubernetes backends", raw)
	}
	port := int32(80)
	if scheme == "https" {
		port = 443
	}
	if p := u.Port(); p != "" {
		v, err := ParsePort(p)
		if err != nil {
			return Server{}, fmt.Errorf("server url %q: %w", raw, err)
		}
		port = v
	}
	return Server{Scheme: scheme, Host: strings.ToLower(host), Port: port}, nil
}

// ParseAddress parses a TCP/UDP loadBalancer server address ("host:port").
func ParseAddress(raw string) (Server, error) {
	host, p, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil {
		return Server{}, fmt.Errorf("parse server address %q: %w", raw, err)
	}
	if host == "" {
		return Server{}, fmt.Errorf("server address %q: missing host", raw)
	}
	port, err := ParsePort(p)
	if err != nil {
		return Server{}, fmt.Errorf("server address %q: %w", raw, err)
	}
	return Server{Host: strings.ToLower(host), Port: port}, nil
}

// ParsePort parses a port number in 1..65535.
func ParsePort(s string) (int32, error) {
	v, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	if v <= 0 || v > 65535 {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	return int32(v), nil
}

// ServiceName derives a DNS-1035 Service name from an arbitrary base name.
// Names that would exceed 63 characters are trimmed and suffixed with a short
// hash of the full base, so distinct bases stay distinct.
func ServiceName(base string) string {
	name := ServiceNamePrefix + sanitize.SanitizeResourceName(base)
	if len(name) <= maxServiceNameLength {
		return name
	}
	sum := sha256.Sum256([]byte(base))
	suffix := hex.EncodeToString(sum[:])[:nameHashLength]
	trimmed := strings.TrimRight(name[:maxServiceNameLength-nameHashLength-1], "-")
	return trimmed + "-" + suffix
}

// Bucket is one generated Service with its EndpointSlices. Weight is the
// number of servers it carries, so a TraefikService or IngressRouteTCP that
// spreads traffic across several buckets keeps the per-server distribution
// Pangolin asked for.
type Bucket struct {
	Service *corev1.Service
	Slices  []*discoveryv1.EndpointSlice
	Weight  int
	Scheme  string
}

// Spec describes the Services to build for one Pangolin service.
type Spec struct {
	Namespace string
	// Base is the Pangolin (sanitized) service name the Services derive from.
	Base string
	// PortName names the Service and EndpointSlice port.
	PortName string
	Protocol corev1.Protocol
	Servers  []Server
}

// Build splits the servers into buckets with unique addresses and returns one
// Service per bucket. Servers with different schemes always land in different
// buckets, because Traefik applies a single scheme per Service reference.
// Servers are de-duplicated on scheme, host and port first.
func Build(spec Spec) []Bucket {
	servers := dedupe(spec.Servers)
	type bucketState struct {
		scheme  string
		seen    map[string]struct{}
		servers []Server
	}
	var states []*bucketState
	for _, s := range servers {
		var target *bucketState
		for _, st := range states {
			if st.scheme != s.Scheme {
				continue
			}
			if _, dup := st.seen[s.Host]; dup {
				continue
			}
			target = st
			break
		}
		if target == nil {
			target = &bucketState{scheme: s.Scheme, seen: map[string]struct{}{}}
			states = append(states, target)
		}
		target.seen[s.Host] = struct{}{}
		target.servers = append(target.servers, s)
	}

	buckets := make([]Bucket, 0, len(states))
	for i, st := range states {
		base := spec.Base
		if len(states) > 1 {
			base = fmt.Sprintf("%s-%d", spec.Base, i)
		}
		name := ServiceName(base)
		buckets = append(buckets, Bucket{
			Service: buildService(spec, name, st.servers[0].Port),
			Slices:  buildSlices(spec, name, st.servers),
			Weight:  len(st.servers),
			Scheme:  st.scheme,
		})
	}
	if len(buckets) == 0 {
		// Keep a Service even without servers (for example, every target is
		// offline) so references stay valid; Traefik reports it as having
		// no servers instead of failing the whole router on a missing object.
		buckets = append(buckets, Bucket{
			Service: buildService(spec, ServiceName(spec.Base), 1),
			Weight:  1,
		})
	}
	return buckets
}

func dedupe(in []Server) []Server {
	seen := make(map[Server]struct{}, len(in))
	out := make([]Server, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func buildService(spec Spec, name string, port int32) *corev1.Service {
	svc := &corev1.Service{}
	svc.APIVersion = "v1"
	svc.Kind = "Service"
	svc.Name = name
	svc.Namespace = spec.Namespace
	svc.Spec.ClusterIP = corev1.ClusterIPNone
	svc.Spec.Ports = []corev1.ServicePort{{Name: spec.PortName, Port: port, Protocol: spec.Protocol}}
	return svc
}

// buildSlices groups a bucket's servers by address type and port. Every slice
// carries the kubernetes.io/service-name label Traefik selects slices by.
func buildSlices(spec Spec, svcName string, servers []Server) []*discoveryv1.EndpointSlice {
	type key struct {
		at   discoveryv1.AddressType
		port int32
	}
	groups := map[key][]string{}
	for _, s := range servers {
		k := key{at: s.addressType(), port: s.Port}
		groups[k] = append(groups[k], s.Host)
	}
	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].at != keys[j].at {
			return keys[i].at < keys[j].at
		}
		return keys[i].port < keys[j].port
	})

	slices := make([]*discoveryv1.EndpointSlice, 0, len(keys))
	for _, k := range keys {
		hosts := groups[k]
		sort.Strings(hosts)
		es := &discoveryv1.EndpointSlice{}
		es.APIVersion = "discovery.k8s.io/v1"
		es.Kind = "EndpointSlice"
		es.Namespace = spec.Namespace
		es.Name = sliceName(svcName, k.at, k.port)
		es.Labels = map[string]string{
			discoveryv1.LabelServiceName: svcName,
			discoveryv1.LabelManagedBy:   EndpointSliceManagedBy,
		}
		es.AddressType = k.at
		portName := spec.PortName
		proto := spec.Protocol
		port := k.port
		es.Ports = []discoveryv1.EndpointPort{{Name: &portName, Port: &port, Protocol: &proto}}
		ready := true
		for _, h := range hosts {
			es.Endpoints = append(es.Endpoints, discoveryv1.Endpoint{
				Addresses:  []string{h},
				Conditions: discoveryv1.EndpointConditions{Ready: &ready, Serving: &ready},
			})
		}
		slices = append(slices, es)
	}
	return slices
}

func sliceName(svcName string, at discoveryv1.AddressType, port int32) string {
	family := map[discoveryv1.AddressType]string{
		discoveryv1.AddressTypeIPv4: "v4",
		discoveryv1.AddressTypeIPv6: "v6",
		discoveryv1.AddressTypeFQDN: "fqdn",
	}[at]
	return fmt.Sprintf("%s-%s-%d", svcName, family, port)
}

// IsClusterServiceHost reports whether host names a Kubernetes Service
// ("<name>.<namespace>.svc[.<cluster-domain>]") and returns its parts.
func IsClusterServiceHost(host string) (name, namespace string, ok bool) {
	parts := strings.Split(strings.TrimSuffix(host, "."), ".")
	if len(parts) < 3 || parts[2] != "svc" || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

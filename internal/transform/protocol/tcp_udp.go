package protocol

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"

	"pangolin-kube-controller/internal/transform/backend"
	traefikconfig "pangolin-kube-controller/internal/transform/config"
	"pangolin-kube-controller/internal/transform/sanitize"
)

// Port names used for generated TCP/UDP Services.
const (
	TCPPortName = "tcp"
	UDPPortName = "udp"
)

// StreamArtifacts is the Kubernetes representation of Pangolin's TCP or UDP
// configuration.
type StreamArtifacts struct {
	// Routes are complete IngressRouteTCP/IngressRouteUDP objects keyed by name.
	Routes   map[string]map[string]interface{}
	Services []*corev1.Service
	Slices   []*discoveryv1.EndpointSlice
	// Transports are ServersTransportTCP specs keyed by name, including the
	// PROXY protocol transports synthesised for "<prefix>v<N>@file" references.
	Transports map[string]json.RawMessage
	// Failed lists routers that could not be converted; their existing objects
	// are kept rather than garbage-collected.
	Failed map[string]error
}

type streamKind struct {
	routeKind string
	portName  string
	protocol  corev1.Protocol
	prefix    string
}

var (
	tcpKind = streamKind{routeKind: "IngressRouteTCP", portName: TCPPortName, protocol: corev1.ProtocolTCP, prefix: "tcp-"}
	udpKind = streamKind{routeKind: "IngressRouteUDP", portName: UDPPortName, protocol: corev1.ProtocolUDP, prefix: "udp-"}
)

// TransformTCP converts sanitized cfg.TCP into IngressRouteTCPs, Services,
// EndpointSlices and ServersTransportTCPs.
func TransformTCP(cfg *traefikconfig.Config, namespace string) StreamArtifacts {
	if cfg == nil || cfg.TCP == nil {
		return StreamArtifacts{}
	}
	return transformStream(tcpKind, cfg.TCP, namespace)
}

// TransformUDP converts sanitized cfg.UDP into IngressRouteUDPs, Services and
// EndpointSlices.
func TransformUDP(cfg *traefikconfig.Config, namespace string) StreamArtifacts {
	if cfg == nil || cfg.UDP == nil {
		return StreamArtifacts{}
	}
	return transformStream(udpKind, cfg.UDP, namespace)
}

type streamService struct {
	children []map[string]interface{}
	svcs     []*corev1.Service
	slices   []*discoveryv1.EndpointSlice
}

func transformStream(kind streamKind, section *traefikconfig.TCPUDPConfig, namespace string) StreamArtifacts {
	out := StreamArtifacts{
		Routes:     map[string]map[string]interface{}{},
		Transports: map[string]json.RawMessage{},
		Failed:     map[string]error{},
	}
	for name, raw := range section.ServersTransports {
		out.Transports[name] = raw
	}

	built := map[string]*streamService{}
	svcErr := map[string]error{}
	for _, name := range sortedKeys(section.Services) {
		ss, err := buildStreamService(kind, namespace, name, section.Services[name], out.Transports)
		if err != nil {
			svcErr[name] = err
			continue
		}
		built[name] = ss
	}

	used := map[string]bool{}
	for _, name := range sortedKeys(section.Routers) {
		route, svcName, err := buildStreamRoute(kind, namespace, name, section.Routers[name], built, svcErr)
		if err != nil {
			out.Failed[name] = err
			continue
		}
		out.Routes[name] = route
		used[svcName] = true
	}
	for _, name := range sortedKeys(built) {
		if !used[name] {
			continue
		}
		out.Services = append(out.Services, built[name].svcs...)
		out.Slices = append(out.Slices, built[name].slices...)
	}
	return out
}

// ppTransportRef matches Pangolin's PROXY protocol transport references, for
// example "pp-transport-v1@file". The file provider that defines them in a
// Docker deployment does not exist in Kubernetes, so the controller creates an
// equivalent ServersTransportTCP.
var ppTransportRef = regexp.MustCompile(`^(.+v([12]))@file$`)

func buildStreamService(kind streamKind, namespace, name string, raw json.RawMessage, transports map[string]json.RawMessage) (*streamService, error) {
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("service %s: %w", name, err)
	}
	lb, ok := obj["loadBalancer"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("service %s: only loadBalancer services are supported for %s", name, kind.routeKind)
	}
	rawServers, _ := lb["servers"].([]interface{})
	servers := make([]backend.Server, 0, len(rawServers))
	for i, rs := range rawServers {
		m, ok := rs.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("service %s: server %d is not an object", name, i)
		}
		addr, _ := m["address"].(string)
		if addr == "" {
			return nil, fmt.Errorf("service %s: server %d has no address", name, i)
		}
		srv, err := backend.ParseAddress(addr)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", name, err)
		}
		servers = append(servers, srv)
	}

	transport := ""
	if kind.routeKind == tcpKind.routeKind {
		if st, ok := lb["serversTransport"].(string); ok && st != "" {
			transport = resolveTCPTransport(st, transports)
		}
	}

	buckets := backend.Build(backend.Spec{
		Namespace: namespace,
		Base:      kind.prefix + name,
		PortName:  kind.portName,
		Protocol:  kind.protocol,
		Servers:   servers,
	})
	ss := &streamService{}
	for _, b := range buckets {
		ss.svcs = append(ss.svcs, b.Service)
		ss.slices = append(ss.slices, b.Slices...)
		child := map[string]interface{}{"name": b.Service.Name, "port": kind.portName, "weight": b.Weight}
		if transport != "" {
			child["serversTransport"] = transport
		}
		ss.children = append(ss.children, child)
	}
	return ss, nil
}

func resolveTCPTransport(ref string, transports map[string]json.RawMessage) string {
	m := ppTransportRef.FindStringSubmatch(ref)
	if m == nil {
		return ref
	}
	name := sanitize.SanitizeResourceName(m[1])
	if _, defined := transports[name]; !defined {
		version, _ := strconv.Atoi(m[2])
		b, _ := json.Marshal(map[string]interface{}{"proxyProtocol": map[string]interface{}{"version": version}})
		transports[name] = b
	}
	return name
}

func buildStreamRoute(kind streamKind, namespace, name string, raw json.RawMessage, built map[string]*streamService, svcErr map[string]error) (map[string]interface{}, string, error) {
	var r map[string]interface{}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, "", fmt.Errorf("router %s: %w", name, err)
	}
	svcName, _ := r["service"].(string)
	if svcName == "" {
		return nil, "", fmt.Errorf("router %s: missing service", name)
	}
	ss, ok := built[svcName]
	if !ok {
		if err, failed := svcErr[svcName]; failed {
			return nil, "", fmt.Errorf("router %s: %w", name, err)
		}
		return nil, "", fmt.Errorf("router %s: unknown service %s", name, svcName)
	}
	route := map[string]interface{}{"services": deepCopyJSON(toInterfaces(ss.children))}
	spec := map[string]interface{}{"entryPoints": stringList(r["entryPoints"]), "routes": []interface{}{route}}

	if kind.routeKind == tcpKind.routeKind {
		rule, _ := r["rule"].(string)
		if rule == "" {
			return nil, "", fmt.Errorf("router %s: missing rule", name)
		}
		route["match"] = rule
		if p, ok := r["priority"].(float64); ok && p != 0 {
			route["priority"] = int(p)
		}
		if tls, ok := r["tls"].(map[string]interface{}); ok && len(tls) > 0 {
			spec["tls"] = ConvertRouterTLS(tls)
		}
	}
	return map[string]interface{}{
		"apiVersion": traefikconfig.GroupVersion,
		"kind":       kind.routeKind,
		"metadata":   map[string]interface{}{"name": name, "namespace": namespace},
		"spec":       spec,
	}, svcName, nil
}

// ConvertRouterTLS converts a dynamic-config router TLS block to the CRD form:
// `options` is a TLSOption reference object there instead of a bare name.
func ConvertRouterTLS(tls map[string]interface{}) map[string]interface{} {
	out, _ := deepCopyJSON(tls).(map[string]interface{})
	if opt, ok := out["options"].(string); ok {
		if opt == "" {
			delete(out, "options")
		} else {
			out["options"] = map[string]interface{}{"name": opt}
		}
	}
	return out
}

func stringList(v interface{}) []interface{} {
	list, _ := v.([]interface{})
	out := make([]interface{}, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

// sortedKeys returns deterministic ordering of map keys.
func sortedKeys[T any](m map[string]T) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

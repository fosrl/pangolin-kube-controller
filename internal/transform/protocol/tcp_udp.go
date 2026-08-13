package protocol

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"

	traefikconfig "pangolin-kube-controller/internal/transform/config"
	"pangolin-kube-controller/internal/transform/sanitize"
)

// TransformTCP converts cfg.TCP into IngressRouteTCP objects and K8s Services/EndpointSlices.
// This is a pure transformation helper for unit tests (no API calls).
func TransformTCP(cfg *traefikconfig.Config, namespace string) (routes []map[string]interface{}, svcs []*corev1.Service, slices []*discoveryv1.EndpointSlice, err error) {
	if cfg == nil || cfg.TCP == nil {
		return nil, nil, nil, nil
	}
	tcpInfo, err := buildTCPServiceIndex(cfg.TCP.Services)
	if err != nil {
		return nil, nil, nil, err
	}
	// Deterministic ordering of dynamic service names
	keys := sortedKeys(tcpInfo)
	nameMapLocal, svcs, slices, err := buildTCPKubeArtifacts(namespace, keys, tcpInfo)
	if err != nil {
		return nil, nil, nil, err
	}
	routes, err = buildTCPRoutes(namespace, cfg.TCP.Routers, nameMapLocal, tcpInfo)
	if err != nil {
		return nil, nil, nil, err
	}
	return routes, svcs, slices, nil
}

// tcpSvcInfo holds extracted service data for TCP dynamic services.
type tcpSvcInfo struct {
	port      int32
	hosts     []string
	transport string
}

// buildTCPServiceIndex parses raw TCP services into a map of name -> tcpSvcInfo.
func buildTCPServiceIndex(services map[string]json.RawMessage) (map[string]tcpSvcInfo, error) {
	out := make(map[string]tcpSvcInfo, len(services))
	for name, raw := range services {
		hosts, port, err := extractLBAddressesPort(raw)
		if err != nil {
			return nil, err
		}
		out[name] = tcpSvcInfo{port: port, hosts: hosts, transport: extractServersTransportName(raw)}
	}
	return out, nil
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

// buildTCPKubeArtifacts constructs Services and EndpointSlices from tcp service info.
func buildTCPKubeArtifacts(namespace string, keys []string, info map[string]tcpSvcInfo) (nameMap map[string]string, svcs []*corev1.Service, slices []*discoveryv1.EndpointSlice, err error) {
	nameMap = make(map[string]string, len(keys))
	for _, dyn := range keys {
		meta := info[dyn]
		kubeName := sanitize.SanitizeResourceName(fmt.Sprintf("%s-%d", dyn, meta.port))
		nameMap[dyn] = kubeName
		svc := buildHeadlessService(namespace, kubeName, meta.port, corev1.ProtocolTCP)
		svc.Labels = map[string]string{ArtifactProtocolLabel: "tcp"}
		svcs = append(svcs, svc)
		eps, e := buildEndpointSlice(namespace, kubeName, meta.port, meta.hosts, corev1.ProtocolTCP)
		if e != nil {
			return nil, nil, nil, e
		}
		eps.Labels = map[string]string{ArtifactProtocolLabel: "tcp", discoveryv1.LabelServiceName: kubeName}
		slices = append(slices, eps)
	}
	return nameMap, svcs, slices, nil
}

// buildTCPRoutes builds IngressRouteTCP objects (with optional serversTransport injection).
func buildTCPRoutes(namespace string, routers map[string]json.RawMessage, nameMap map[string]string, svcInfo map[string]tcpSvcInfo) ([]map[string]interface{}, error) {
	var routes []map[string]interface{}
	for name, raw := range routers {
		u, svcName, err := transformRouterTCPToIngressRouteTCP(namespace, name, raw, nameMap)
		if err != nil {
			return nil, fmt.Errorf("transform TCP router %s: %w", name, err)
		}
		meta, ok := svcInfo[svcName]
		if !ok || meta.transport == "" {
			routes = append(routes, u)
			continue
		}
		injectServersTransport(u, meta.transport)
		routes = append(routes, u)
	}
	return routes, nil
}

// injectServersTransport mutates IngressRouteTCP/UDP structure to add serversTransport.
func injectServersTransport(u map[string]interface{}, transport string) {
	spec, ok := u["spec"].(map[string]interface{})
	if !ok {
		return
	}
	rts, ok := spec["routes"].([]interface{})
	if !ok || len(rts) == 0 {
		return
	}
	r0, ok := rts[0].(map[string]interface{})
	if !ok {
		return
	}
	ss, ok := r0["services"].([]interface{})
	if !ok || len(ss) == 0 {
		return
	}
	entry, ok := ss[0].(map[string]interface{})
	if !ok {
		return
	}
	entry["serversTransport"] = sanitize.SanitizeResourceName(transport)
	ss[0] = entry
	r0["services"] = ss
	rts[0] = r0
	spec["routes"] = rts
	u["spec"] = spec
}

func transformRouterTCPToIngressRouteTCP(namespace, name string, raw []byte, dynToKube map[string]string) (map[string]interface{}, string, error) {
	var r map[string]interface{}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, "", err
	}
	entryPoints, _ := r["entryPoints"].([]interface{})
	var epsIfc []interface{}
	for _, ep := range entryPoints {
		if s, ok := ep.(string); ok {
			epsIfc = append(epsIfc, s)
		}
	}
	rule, _ := r["rule"].(string)
	svcName, _ := r["service"].(string)
	if rule == "" || svcName == "" {
		return nil, "", fmt.Errorf("missing rule/service")
	}
	kubeName := dynToKube[svcName]
	if kubeName == "" {
		kubeName = sanitize.SanitizeResourceName(svcName)
	}
	svcPort, err := derivePortFromName(kubeName)
	if err != nil {
		return nil, "", fmt.Errorf("derive port for %s: %w", kubeName, err)
	}
	svcEntry := map[string]interface{}{"name": kubeName, "port": float64(svcPort)}
	route := map[string]interface{}{
		"match":    rule,
		"services": []interface{}{svcEntry},
	}
	spec := map[string]interface{}{"entryPoints": epsIfc, "routes": []interface{}{route}}
	u := map[string]interface{}{
		"apiVersion": traefikconfig.GroupVersion,
		"kind":       "IngressRouteTCP",
		"metadata":   map[string]interface{}{"name": sanitize.SanitizeResourceName(name), "namespace": namespace},
		"spec":       spec,
	}
	return u, svcName, nil
}

// TransformUDP mirrors TransformTCP but produces IngressRouteUDP and UDP services.
func TransformUDP(cfg *traefikconfig.Config, namespace string) (routes []map[string]interface{}, svcs []*corev1.Service, slices []*discoveryv1.EndpointSlice, err error) {
	if cfg == nil || cfg.UDP == nil {
		return nil, nil, nil, nil
	}
	udpInfo, err := buildUDPServiceIndex(cfg.UDP.Services)
	if err != nil {
		return nil, nil, nil, err
	}
	keys := sortedKeys(udpInfo)
	nameMap, svcs, slices, err := buildUDPKubeArtifacts(namespace, keys, udpInfo)
	if err != nil {
		return nil, nil, nil, err
	}
	routes, err = buildUDPRoutes(cfg.UDP.Routers, nameMap, namespace)
	if err != nil {
		return nil, nil, nil, err
	}
	return routes, svcs, slices, nil
}

type udpSvcInfo struct {
	port  int32
	hosts []string
}

func buildUDPServiceIndex(services map[string]json.RawMessage) (map[string]udpSvcInfo, error) {
	out := make(map[string]udpSvcInfo, len(services))
	for name, raw := range services {
		hosts, port, err := extractLBAddressesPort(raw)
		if err != nil {
			return nil, err
		}
		out[name] = udpSvcInfo{port: port, hosts: hosts}
	}
	return out, nil
}

func buildUDPKubeArtifacts(namespace string, keys []string, info map[string]udpSvcInfo) (nameMap map[string]string, svcs []*corev1.Service, slices []*discoveryv1.EndpointSlice, err error) {
	nameMap = make(map[string]string, len(keys))
	for _, dyn := range keys {
		meta := info[dyn]
		kubeName := sanitize.SanitizeResourceName(fmt.Sprintf("%s-%d", dyn, meta.port))
		nameMap[dyn] = kubeName
		svc := buildHeadlessService(namespace, kubeName, meta.port, corev1.ProtocolUDP)
		svc.Labels = map[string]string{ArtifactProtocolLabel: "udp"}
		svcs = append(svcs, svc)
		eps, e := buildEndpointSlice(namespace, kubeName, meta.port, meta.hosts, corev1.ProtocolUDP)
		if e != nil {
			return nil, nil, nil, e
		}
		eps.Labels = map[string]string{ArtifactProtocolLabel: "udp", discoveryv1.LabelServiceName: kubeName}
		slices = append(slices, eps)
	}
	return nameMap, svcs, slices, nil
}

func buildUDPRoutes(routers map[string]json.RawMessage, nameMap map[string]string, namespace string) ([]map[string]interface{}, error) {
	var out []map[string]interface{}
	for name, raw := range routers {
		var r map[string]interface{}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		entryPoints, _ := r["entryPoints"].([]interface{})
		var epsIfc []interface{}
		for _, ep := range entryPoints {
			if s, ok := ep.(string); ok {
				epsIfc = append(epsIfc, s)
			}
		}
		svcName, _ := r["service"].(string)
		if svcName == "" {
			return nil, fmt.Errorf("router %s: missing service in config", name)
		}
		kubeName := nameMap[svcName]
		if kubeName == "" {
			return nil, fmt.Errorf("router %s: unknown service %s", name, svcName)
		}
		svcPort, err := derivePortFromName(kubeName)
		if err != nil {
			return nil, fmt.Errorf("derive port for %s: %w", kubeName, err)
		}
		route := map[string]interface{}{"services": []interface{}{map[string]interface{}{"name": kubeName, "port": float64(svcPort)}}}
		spec := map[string]interface{}{"entryPoints": epsIfc, "routes": []interface{}{route}}
		u := map[string]interface{}{
			"apiVersion": traefikconfig.GroupVersion,
			"kind":       "IngressRouteUDP",
			"metadata":   map[string]interface{}{"name": sanitize.SanitizeResourceName(name), "namespace": namespace},
			"spec":       spec,
		}
		out = append(out, u)
	}
	return out, nil
}

// Helper constructors shared by TCP & UDP
func buildHeadlessService(ns, name string, port int32, proto corev1.Protocol) *corev1.Service {
	svc := &corev1.Service{}
	svc.Namespace = ns
	svc.Name = name
	svc.Spec.ClusterIP = "None"
	// Port is already validated and stored as int32 to avoid narrowing conversions.
	svc.Spec.Ports = []corev1.ServicePort{{Port: port, Protocol: proto}}
	return svc
}

func buildEndpointSlice(ns, base string, port int32, hosts []string, proto corev1.Protocol) (*discoveryv1.EndpointSlice, error) {
	slice := &discoveryv1.EndpointSlice{}
	slice.Namespace = ns
	slice.Name = base + "-eps"
	at, err := chooseAddressType(hosts)
	if err != nil {
		return nil, err
	}
	slice.AddressType = at
	// Each endpoint must use its distinct host; previously a bug reused hosts[0] for all entries.
	for _, host := range hosts {
		slice.Endpoints = append(slice.Endpoints, discoveryv1.Endpoint{Addresses: []string{host}})
	}
	p := port
	slice.Ports = []discoveryv1.EndpointPort{{Port: &p, Protocol: &proto}}
	return slice, nil
}

func chooseAddressType(hosts []string) (discoveryv1.AddressType, error) {
	if len(hosts) == 0 {
		return discoveryv1.AddressTypeFQDN, nil
	}
	// Determine whether all entries are IPv4, all IPv6, or mixed/contain FQDNs
	seenIPv4 := false
	seenIPv6 := false
	for _, h := range hosts {
		ip := net.ParseIP(h)
		if ip == nil {
			// Treat as FQDN
			return discoveryv1.AddressTypeFQDN, nil
		}
		if ip.To4() != nil {
			seenIPv4 = true
		} else {
			seenIPv6 = true
		}
		if seenIPv4 && seenIPv6 {
			// Mixed IP versions -> report error so callers can decide how to split
			return "", fmt.Errorf("mixed IP address families in hosts: %v", hosts)
		}
	}
	if seenIPv4 {
		return discoveryv1.AddressTypeIPv4, nil
	}
	return discoveryv1.AddressTypeIPv6, nil
}

func derivePortFromName(kubeName string) (int32, error) {
	idx := strings.LastIndex(kubeName, "-")
	if idx <= 0 || idx == len(kubeName)-1 {
		return 0, fmt.Errorf("no port suffix in name: %s", kubeName)
	}
	v, err := parsePort32(kubeName[idx+1:])
	if err != nil {
		return 0, err
	}
	return v, nil
}

func extractLBAddressesPort(raw json.RawMessage) (hosts []string, port int32, err error) {
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, 0, err
	}
	srvs, err := loadBalancerServers(obj)
	if err != nil {
		return nil, 0, err
	}
	for _, s := range srvs {
		m, ok := s.(map[string]interface{})
		if !ok {
			return nil, 0, fmt.Errorf("invalid loadBalancer server")
		}
		addr := pickServerAddress(m)
		if addr == "" {
			return nil, 0, fmt.Errorf("missing server address")
		}
		h, p, err := splitHostPortFlexible(addr)
		if err != nil {
			return nil, 0, err
		}
		hosts = append(hosts, h)
		if err := setOrValidatePort(&port, p); err != nil {
			return nil, 0, err
		}
	}
	return hosts, port, nil
}

func loadBalancerServers(obj map[string]interface{}) ([]interface{}, error) {
	lb, ok := obj["loadBalancer"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("missing loadBalancer")
	}
	srvs, ok := lb["servers"].([]interface{})
	if !ok || len(srvs) == 0 {
		return nil, fmt.Errorf("no servers")
	}
	return srvs, nil
}

func pickServerAddress(server map[string]interface{}) string {
	if addr, _ := server["address"].(string); addr != "" {
		return addr
	}
	if u, _ := server["url"].(string); u != "" {
		return u
	}
	return ""
}

func setOrValidatePort(existing *int32, candidate int32) error {
	if *existing == 0 {
		*existing = candidate
		return nil
	}
	if candidate != *existing {
		return fmt.Errorf("inconsistent ports in loadBalancer servers: %d vs %d", *existing, candidate)
	}
	return nil
}

func splitHostPortFlexible(s string) (host string, port int32, err error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		u, uerr := url.Parse(s)
		if uerr != nil {
			return "", 0, uerr
		}
		rawPort := u.Port()
		if rawPort == "" { // explicit empty port invalid
			return "", 0, fmt.Errorf("missing port in address: %s", s)
		}
		p, perr := parsePort32(rawPort)
		if perr != nil {
			return "", 0, perr
		}
		return u.Hostname(), p, nil
	}
	h, pStr, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, err
	}
	if pStr == "" {
		return "", 0, fmt.Errorf("missing port in address: %s", s)
	}
	p, perr := parsePort32(pStr)
	if perr != nil {
		return "", 0, perr
	}
	return h, p, nil
}

// parsePort32 parses a port string into int32 with strict bounds (1..65535).
func parsePort32(s string) (int32, error) {
	v, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return 0, err
	}
	if v <= 0 || v > 65535 { // enforce valid TCP/UDP port range
		return 0, fmt.Errorf("invalid port %s", s)
	}
	return int32(v), nil
}

func extractServersTransportName(raw json.RawMessage) string {
	var obj map[string]interface{}
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	if v, ok := obj["serversTransport"].(string); ok {
		return v
	}
	return ""
}

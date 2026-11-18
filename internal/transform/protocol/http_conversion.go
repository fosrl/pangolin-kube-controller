package protocol

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	logrus "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"

	"pangolin-kube-controller/internal/config"
	"pangolin-kube-controller/internal/transform/backend"
	"pangolin-kube-controller/internal/transform/sanitize"
)

// HTTPPortName is the Service port name used for HTTP backends.
const HTTPPortName = "web"

// lbChildOptions are the loadBalancer options Traefik accepts on a service
// reference (TraefikService weighted child / IngressRoute service). They are
// copied from Pangolin's loadBalancer onto every generated reference.
var lbChildOptions = []string{"sticky", "strategy", "passHostHeader", "serversTransport", "responseForwarding", "passiveHealthCheck"}

// HTTPArtifacts is the Kubernetes representation of Pangolin's HTTP services.
type HTTPArtifacts struct {
	// TraefikServices maps the sanitized Pangolin service name to a
	// TraefikService spec. IngressRoutes keep referencing services by that name.
	TraefikServices map[string]json.RawMessage
	Services        []*corev1.Service
	Slices          []*discoveryv1.EndpointSlice
	// Failed lists services that could not be converted. Their existing
	// objects must be kept rather than garbage-collected.
	Failed map[string]error
}

// TransformHTTPServices converts sanitized HTTP services into TraefikServices
// plus the selectorless Services/EndpointSlices that carry their servers.
//
// A loadBalancer becomes a `weighted` TraefikService whose children reference
// Kubernetes Services: TraefikService has no loadBalancer field, and the API
// server prunes one silently, which left every tunnel-backed resource without
// servers. A server that is already a Service in the target namespace
// ("<name>.<ns>.svc...") is referenced directly; everything else (WireGuard
// peer addresses, LAN addresses, hostnames) goes through generated Services.
func TransformHTTPServices(cfg *config.Config, namespace string, services map[string]json.RawMessage) HTTPArtifacts {
	out := HTTPArtifacts{
		TraefikServices: make(map[string]json.RawMessage, len(services)),
		Failed:          map[string]error{},
	}
	for _, name := range sortedKeys(services) {
		spec, svcs, slices, err := transformHTTPService(cfg, namespace, name, services[name], services)
		if err != nil {
			out.Failed[name] = err
			continue
		}
		out.TraefikServices[name] = spec
		out.Services = append(out.Services, svcs...)
		out.Slices = append(out.Slices, slices...)
	}
	return out
}

func transformHTTPService(cfg *config.Config, namespace, name string, raw json.RawMessage, all map[string]json.RawMessage) (json.RawMessage, []*corev1.Service, []*discoveryv1.EndpointSlice, error) {
	trimmed := strings.TrimSpace(string(raw))
	var spec map[string]interface{}
	if trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal(raw, &spec); err != nil {
			return nil, nil, nil, fmt.Errorf("service %s: %w", name, err)
		}
	}
	if len(spec) == 0 {
		// Legacy behaviour: an empty service is pointed at TRAEFIK_LB_URL.
		fallback := getTraefikEnvURL(cfg)
		if fallback == "" {
			return nil, nil, nil, fmt.Errorf("service %s has an empty spec and TRAEFIK_LB_URL/TRAEFIK_LB_IP are not set", name)
		}
		spec = map[string]interface{}{"loadBalancer": map[string]interface{}{"servers": []interface{}{map[string]interface{}{"url": fallback}}}}
		logrus.Infof("TraefikService %s: empty spec, using fallback server %s", name, fallback)
	}

	if lb, ok := spec["loadBalancer"].(map[string]interface{}); ok {
		return buildLoadBalancerService(namespace, name, lb)
	}
	for _, kind := range []string{"weighted", "mirroring", "failover", "highestRandomWeight"} {
		if _, ok := spec[kind]; ok {
			markTraefikServiceRefs(spec, all)
			b, err := json.Marshal(spec)
			return b, nil, nil, err
		}
	}
	return nil, nil, nil, fmt.Errorf("service %s: no loadBalancer, weighted, mirroring, failover or highestRandomWeight section", name)
}

func buildLoadBalancerService(namespace, name string, lb map[string]interface{}) (json.RawMessage, []*corev1.Service, []*discoveryv1.EndpointSlice, error) {
	rawServers, _ := lb["servers"].([]interface{})
	type clusterRef struct {
		name, scheme string
		port         int32
	}
	clusterRefs := map[clusterRef]int{}
	var others []backend.Server
	for i, rs := range rawServers {
		m, ok := rs.(map[string]interface{})
		if !ok {
			return nil, nil, nil, fmt.Errorf("service %s: server %d is not an object", name, i)
		}
		u, _ := m["url"].(string)
		if u == "" {
			return nil, nil, nil, fmt.Errorf("service %s: server %d has no url", name, i)
		}
		srv, err := backend.ParseURL(u)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("service %s: %w", name, err)
		}
		if svcName, svcNs, ok := backend.IsClusterServiceHost(srv.Host); ok && svcNs == namespace {
			clusterRefs[clusterRef{name: svcName, scheme: srv.Scheme, port: srv.Port}]++
			continue
		}
		others = append(others, srv)
	}

	opts := map[string]interface{}{}
	for _, k := range lbChildOptions {
		if v, ok := lb[k]; ok {
			opts[k] = v
		}
	}
	if _, ok := lb["healthCheck"]; ok {
		// Traefik only honours active health checks on ExternalName Services
		// and rejects the reference otherwise.
		logrus.Warnf("TraefikService %s: dropping loadBalancer.healthCheck, Traefik does not support it for Kubernetes Service backends", name)
	}

	var children []map[string]interface{}
	refKeys := make([]clusterRef, 0, len(clusterRefs))
	for k := range clusterRefs {
		refKeys = append(refKeys, k)
	}
	sort.Slice(refKeys, func(i, j int) bool {
		a, b := refKeys[i], refKeys[j]
		if a.name != b.name {
			return a.name < b.name
		}
		if a.port != b.port {
			return a.port < b.port
		}
		return a.scheme < b.scheme
	})
	for _, k := range refKeys {
		children = append(children, childRef(k.name, int64(k.port), k.scheme, clusterRefs[k], opts))
	}

	var svcs []*corev1.Service
	var slices []*discoveryv1.EndpointSlice
	if len(others) > 0 || len(children) == 0 {
		buckets := backend.Build(backend.Spec{
			Namespace: namespace,
			Base:      name,
			PortName:  HTTPPortName,
			Protocol:  corev1.ProtocolTCP,
			Servers:   others,
		})
		for _, b := range buckets {
			svcs = append(svcs, b.Service)
			slices = append(slices, b.Slices...)
			children = append(children, childRef(b.Service.Name, HTTPPortName, b.Scheme, b.Weight, opts))
		}
	}

	weighted := map[string]interface{}{"services": toInterfaces(children)}
	if sticky, ok := lb["sticky"].(map[string]interface{}); ok && len(children) > 1 {
		weighted["sticky"] = outerSticky(sticky)
	}
	b, err := json.Marshal(map[string]interface{}{"weighted": weighted})
	if err != nil {
		return nil, nil, nil, err
	}
	return b, svcs, slices, nil
}

func childRef(name string, port interface{}, scheme string, weight int, opts map[string]interface{}) map[string]interface{} {
	c := map[string]interface{}{
		"name":   name,
		"kind":   "Service",
		"port":   port,
		"weight": weight,
	}
	if scheme != "" {
		c["scheme"] = scheme
	}
	for k, v := range opts {
		c[k] = deepCopyJSON(v)
	}
	return c
}

// outerSticky derives the cookie that pins a client to one weighted child.
// It must differ from the per-child cookie that pins the server inside it.
func outerSticky(sticky map[string]interface{}) map[string]interface{} {
	out, _ := deepCopyJSON(sticky).(map[string]interface{})
	if cookie, ok := out["cookie"].(map[string]interface{}); ok {
		if n, ok := cookie["name"].(string); ok && n != "" {
			cookie["name"] = n + "_pool"
		}
	}
	return out
}

// markTraefikServiceRefs gives children of native TraefikService specs that
// name another Pangolin service the TraefikService kind; the CRD default is a
// Kubernetes Service.
func markTraefikServiceRefs(spec map[string]interface{}, all map[string]json.RawMessage) {
	mark := func(ref map[string]interface{}) {
		n, _ := ref["name"].(string)
		if _, known := all[n]; known || sanitize.IsCrossProviderRef(n) {
			if _, set := ref["kind"]; !set {
				ref["kind"] = "TraefikService"
			}
		}
	}
	for _, section := range []string{"weighted", "highestRandomWeight"} {
		if w, ok := spec[section].(map[string]interface{}); ok {
			if list, ok := w["services"].([]interface{}); ok {
				for _, s := range list {
					if m, ok := s.(map[string]interface{}); ok {
						mark(m)
					}
				}
			}
		}
	}
	if mir, ok := spec["mirroring"].(map[string]interface{}); ok {
		if n, ok := mir["service"].(string); ok {
			mir["name"] = n
			delete(mir, "service")
			mark(mir)
		}
		if list, ok := mir["mirrors"].([]interface{}); ok {
			for _, s := range list {
				if m, ok := s.(map[string]interface{}); ok {
					mark(m)
				}
			}
		}
	}
}

func toInterfaces(in []map[string]interface{}) []interface{} {
	out := make([]interface{}, len(in))
	for i := range in {
		out[i] = in[i]
	}
	return out
}

func deepCopyJSON(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		m := make(map[string]interface{}, len(t))
		for k, val := range t {
			m[k] = deepCopyJSON(val)
		}
		return m
	case []interface{}:
		s := make([]interface{}, len(t))
		for i, val := range t {
			s[i] = deepCopyJSON(val)
		}
		return s
	default:
		return t
	}
}

func getTraefikEnvURL(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if cfg.TraefikLBURL != "" {
		return cfg.TraefikLBURL
	}
	if cfg.TraefikLBIP == "" {
		return ""
	}
	url := cfg.TraefikLBScheme + "://" + cfg.TraefikLBIP
	if cfg.TraefikLBPort != "" {
		url += ":" + cfg.TraefikLBPort
	}
	return url
}

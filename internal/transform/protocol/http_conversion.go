package protocol

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	logrus "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"

	"pangolin-kube-controller/internal/config"
	"pangolin-kube-controller/internal/transform/sanitize"
)

// ArtifactProtocolLabel scopes generated Kubernetes resources by protocol.
const ArtifactProtocolLabel = "pangolin-kube-controller/protocol"

type kubeServiceTarget struct {
	name      string
	namespace string
	port      int
	scheme    string
}

// ProcessServices rewrites TraefikService specs for load balancer conversions and logging.
func ProcessServices(cfg *config.Config, services map[string]json.RawMessage) map[string]json.RawMessage {
	processed, _, _, _ := ProcessHTTPServices(cfg, services, "")
	return processed
}

func processSingleService(cfg *config.Config, name string, raw json.RawMessage) json.RawMessage {
	processed, _, _, _ := processSingleHTTPService(cfg, "", name, raw)
	return processed
}

// ProcessHTTPServices rewrites HTTP service specs and builds Kubernetes
// artifacts for load balancers whose servers are not Kubernetes Services.
func ProcessHTTPServices(cfg *config.Config, services map[string]json.RawMessage, namespace string) (map[string]json.RawMessage, []*corev1.Service, []*discoveryv1.EndpointSlice, error) {
	if services == nil {
		return nil, nil, nil, nil
	}
	out := make(map[string]json.RawMessage, len(services))
	var kubeServices []*corev1.Service
	var endpointSlices []*discoveryv1.EndpointSlice
	for name, raw := range services {
		processed, svc, endpointSlice, err := processSingleHTTPService(cfg, namespace, name, raw)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("process HTTP service %s: %w", name, err)
		}
		out[name] = processed
		if svc != nil {
			kubeServices = append(kubeServices, svc)
			endpointSlices = append(endpointSlices, endpointSlice)
		}
	}
	return out, kubeServices, endpointSlices, nil
}

func processSingleHTTPService(cfg *config.Config, namespace, name string, raw json.RawMessage) (json.RawMessage, *corev1.Service, *discoveryv1.EndpointSlice, error) {
	trim := strings.TrimSpace(string(raw))
	if trim != "" && trim != "{}" {
		return processNonEmptyHTTPService(namespace, name, raw)
	}
	return processEmptyService(cfg, name, raw), nil, nil, nil
}

func processNonEmptyService(name string, raw json.RawMessage) json.RawMessage {
	processed, _, _, _ := processNonEmptyHTTPService("", name, raw)
	return processed
}

func processNonEmptyHTTPService(namespace, name string, raw json.RawMessage) (json.RawMessage, *corev1.Service, *discoveryv1.EndpointSlice, error) {
	var spec map[string]interface{}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return raw, nil, nil, nil
	}
	if target, ok := convertLoadBalancerToK8sService(spec); ok {
		converted, err := json.Marshal(spec)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("marshal Kubernetes Service reference: %w", err)
		}
		logrus.Infof("TraefikService %s converted to Kubernetes Service %s/%s port=%d scheme=%s", name, target.namespace, target.name, target.port, target.scheme)
		return converted, nil, nil, nil
	}
	target, ok := parseExternalServiceTargets(spec)
	if !ok || namespace == "" {
		if urls := extractServiceURLs(spec); len(urls) > 0 {
			logrus.Infof("TraefikService %s servers=%v", name, urls)
		}
		return raw, nil, nil, nil
	}
	kubeName := sanitize.SanitizeResourceName(fmt.Sprintf("%s-%d", name, target.port))
	rewriteAsKubeService(spec, kubeName, namespace, target.port, target.scheme, target.serviceOptions)
	converted, err := json.Marshal(spec)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal external Service reference: %w", err)
	}
	svc := buildHeadlessService(namespace, kubeName, int32(target.port), corev1.ProtocolTCP)
	svc.Labels = map[string]string{ArtifactProtocolLabel: "http"}
	endpointSlice, err := buildEndpointSlice(namespace, kubeName, int32(target.port), target.hosts, corev1.ProtocolTCP)
	if err != nil {
		return nil, nil, nil, err
	}
	endpointSlice.Labels = map[string]string{
		ArtifactProtocolLabel:        "http",
		discoveryv1.LabelServiceName: kubeName,
	}
	return converted, svc, endpointSlice, nil
}

func processEmptyService(cfg *config.Config, name string, raw json.RawMessage) json.RawMessage {
	urlStr := getTraefikEnvURL(cfg)
	if urlStr == "" {
		logrus.Warnf("TraefikService %s has empty spec and no derivable LB URL; resource will be invalid until populated", name)
		return raw
	}
	parsed, err := netURLParse(urlStr)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		logrus.Warnf("TraefikService %s: invalid derived LB URL '%s': %v", name, urlStr, err)
		return raw
	}
	return buildTraefikServiceSpec(urlStr)
}

func extractServiceURLs(spec map[string]interface{}) []string {
	lb, ok := spec["loadBalancer"].(map[string]interface{})
	if !ok {
		return nil
	}
	servers, ok := lb["servers"].([]interface{})
	if !ok {
		return nil
	}
	var urls []string
	for _, s := range servers {
		if m, ok := s.(map[string]interface{}); ok {
			if u, ok := m["url"].(string); ok {
				urls = append(urls, u)
			}
		}
	}
	return urls
}

func netURLParse(u string) (*url.URL, error) {
	return url.ParseRequestURI(u)
}

func buildTraefikServiceSpec(url string) json.RawMessage {
	built := map[string]interface{}{
		"loadBalancer": map[string]interface{}{
			"servers": []interface{}{map[string]interface{}{"url": url}},
		},
	}
	b, err := json.Marshal(built)
	if err != nil {
		logrus.Warnf("failed to marshal Traefik service for %s: %v", url, err)
		return []byte("{}")
	}
	logrus.Infof("Filled empty TraefikService with server %s", url)
	return b
}

func convertLoadBalancerToK8sService(spec map[string]interface{}) (*kubeServiceTarget, bool) {
	lbServers, ok := extractLBServers(spec)
	if !ok {
		return nil, false
	}
	target, ok := parseUniformServiceTargets(lbServers)
	if !ok || target == nil {
		return nil, false
	}
	rewriteAsKubeService(spec, target.name, target.namespace, target.port, target.scheme, nil)
	return target, true
}

type externalServiceTarget struct {
	hosts          []string
	port           int
	scheme         string
	serviceOptions map[string]interface{}
}

func parseExternalServiceTargets(spec map[string]interface{}) (*externalServiceTarget, bool) {
	lb, ok := spec["loadBalancer"].(map[string]interface{})
	if !ok {
		return nil, false
	}
	servers, ok := lb["servers"].([]interface{})
	if !ok {
		return nil, false
	}
	serviceOptions := make(map[string]interface{}, len(lb)-1)
	for key, value := range lb {
		if key != "servers" {
			serviceOptions[key] = value
		}
	}
	var target *externalServiceTarget
	for _, server := range servers {
		serverMap, ok := server.(map[string]interface{})
		if !ok || len(serverMap) != 1 {
			return nil, false
		}
		rawURL, ok := serverMap["url"].(string)
		if !ok {
			return nil, false
		}
		parsed, err := netURLParse(rawURL)
		if err != nil || validateExternalServiceURL(parsed, rawURL) != nil {
			return nil, false
		}
		port := derivePort(parsed)
		if target == nil {
			target = &externalServiceTarget{port: port, scheme: parsed.Scheme, serviceOptions: serviceOptions}
		} else if target.port != port || target.scheme != parsed.Scheme {
			return nil, false
		}
		target.hosts = append(target.hosts, parsed.Hostname())
	}
	return target, target != nil
}

func validateExternalServiceURL(parsed *url.URL, raw string) error {
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %s", parsed.Scheme)
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("missing host")
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" {
		return fmt.Errorf("unsupported path or query components in %s", raw)
	}
	if strings.Contains(parsed.Hostname(), ".svc.") || strings.HasSuffix(parsed.Hostname(), ".svc") {
		return fmt.Errorf("target is a Kubernetes Service")
	}
	return nil
}

func rewriteAsKubeService(spec map[string]interface{}, name, namespace string, port int, scheme string, options map[string]interface{}) {
	serviceEntry := map[string]interface{}{
		"name":      name,
		"namespace": namespace,
		"kind":      "Service",
		"port":      port,
	}
	if scheme == "https" {
		serviceEntry["scheme"] = "https"
	}
	for key, value := range options {
		serviceEntry[key] = value
	}
	spec["weighted"] = map[string]interface{}{"services": []interface{}{serviceEntry}}
	delete(spec, "loadBalancer")
}

// extractLBServers validates top-level loadBalancer shape & returns servers list.
func extractLBServers(spec map[string]interface{}) ([]interface{}, bool) {
	if len(spec) != 1 {
		return nil, false
	}
	lb, ok := spec["loadBalancer"].(map[string]interface{})
	if !ok {
		return nil, false
	}
	servers, ok := lb["servers"].([]interface{})
	if !ok || len(servers) == 0 {
		return nil, false
	}
	for k := range lb { // ensure only 'servers'
		if k != "servers" {
			return nil, false
		}
	}
	return servers, true
}

// parseUniformServiceTargets ensures all server URLs point to the same k8s service target.
func parseUniformServiceTargets(servers []interface{}) (*kubeServiceTarget, bool) {
	var target *kubeServiceTarget
	for _, srv := range servers {
		srvMap, ok := srv.(map[string]interface{})
		if !ok || len(srvMap) != 1 {
			return nil, false
		}
		rawURL, ok := srvMap["url"].(string)
		if !ok || rawURL == "" {
			return nil, false
		}
		parsed, err := parseKubeServiceURL(rawURL)
		if err != nil {
			return nil, false
		}
		if target == nil {
			target = parsed
			continue
		}
		if !target.equals(parsed) {
			return nil, false
		}
	}
	return target, target != nil
}

func parseKubeServiceURL(rawURL string) (*kubeServiceTarget, error) {
	parsed, err := netURLParse(rawURL)
	if err != nil {
		return nil, err
	}
	if err := validateParsedServiceURL(parsed, rawURL); err != nil {
		return nil, err
	}
	host := parsed.Hostname()
	parts := strings.Split(host, ".")
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid service reference %q: expected <name>.<namespace>.svc", host)
	}
	if parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("invalid service reference %q: missing name or namespace", host)
	}
	if parts[2] != "svc" {
		return nil, fmt.Errorf("invalid service reference %q: expected .svc segment", host)
	}
	name, namespace := parts[0], parts[1]
	port := derivePort(parsed)
	return &kubeServiceTarget{name: name, namespace: namespace, port: port, scheme: parsed.Scheme}, nil
}

// validateParsedServiceURL performs structural validation for a service style URL.
func validateParsedServiceURL(parsed *url.URL, raw string) error {
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %s", parsed.Scheme)
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" {
		return fmt.Errorf("unsupported path or query components in %s", raw)
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("missing host")
	}
	if !strings.Contains(host, ".svc.") && !strings.HasSuffix(host, ".svc") {
		return fmt.Errorf("host %s is not a Kubernetes service FQDN", host)
	}
	return nil
}

// derivePort returns a port for a parsed service URL.
func derivePort(parsed *url.URL) int {
	if p := parsed.Port(); p != "" {
		if val, err := strconv.Atoi(p); err == nil && val > 0 {
			return val
		}
	}
	if parsed.Scheme == "https" {
		return 443
	}
	return 80
}

func (t *kubeServiceTarget) equals(other *kubeServiceTarget) bool {
	if t == nil || other == nil {
		return false
	}
	return t.name == other.name && t.namespace == other.namespace && t.port == other.port && t.scheme == other.scheme
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

package routing

import (
	"encoding/json"
	"fmt"
	"strings"

	traefikconfig "pangolin-kube-controller/internal/transform/config"
)

const (
	// IngressClassAnnotation selects the Traefik instance that serves a route.
	// Traefik's kubernetescrd provider reads it on IngressRoute,
	// IngressRouteTCP and IngressRouteUDP (spec.ingressClassName only exists
	// from Traefik 3.7 on).
	IngressClassAnnotation = "kubernetes.io/ingress.class"
	// RouterEntryPointsAnnotation mirrors spec.entryPoints for visibility.
	RouterEntryPointsAnnotation = "traefikconfig.ingress.kubernetes.io/router.entrypoints"
)

// RouterConfig captures the metadata needed to build router resources.
type RouterConfig struct {
	Namespace         string
	ManagedLabelKey   string
	ManagedLabelValue string
	ManagedAnnoKey    string
	ManagedAnnoValue  string
	IngressClass      string
}

// AnnotateRouterEntryPointsIfPresent inspects u["spec"].entryPoints and, if present, writes
// traefikconfig.ingress.kubernetes.io/router.entrypoints annotation with a comma-separated list.
func AnnotateRouterEntryPointsIfPresent(u map[string]interface{}, meta map[string]interface{}) {
	if u == nil || meta == nil {
		return
	}
	spec, _ := u["spec"].(map[string]interface{})
	if spec == nil {
		return
	}
	eps := extractEntryPoints(spec)
	if len(eps) == 0 {
		return
	}
	anns, _ := meta["annotations"].(map[string]interface{})
	if anns == nil {
		anns = map[string]interface{}{}
	}
	anns[RouterEntryPointsAnnotation] = strings.Join(eps, ",")
	meta["annotations"] = anns
}

// TransformRouterToIngressRoute converts a raw router payload into an IngressRoute object.
func TransformRouterToIngressRoute(name string, raw json.RawMessage, cfg RouterConfig) (map[string]interface{}, error) {
	meta := map[string]interface{}{"name": name, "namespace": cfg.Namespace}
	labelKey := strings.TrimSpace(cfg.ManagedLabelKey)
	if labelKey != "" {
		meta["labels"] = map[string]interface{}{labelKey: cfg.ManagedLabelValue}
	}
	ann := map[string]interface{}{}
	annoKey := strings.TrimSpace(cfg.ManagedAnnoKey)
	if annoKey != "" {
		ann[annoKey] = cfg.ManagedAnnoValue
	}
	if cfg.IngressClass != "" {
		ann[IngressClassAnnotation] = cfg.IngressClass
	}
	if len(ann) > 0 {
		meta["annotations"] = ann
	}

	var r map[string]interface{}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("router %s unmarshal: %w", name, err)
	}

	eps := extractEntryPoints(r)
	rule, _ := r["rule"].(string)
	serviceName, _ := r["service"].(string)
	priorityVal, _ := r["priority"].(float64)
	mws := parseMiddlewares(r)
	tlsObj, _ := r["tls"].(map[string]interface{})

	if rule == "" || serviceName == "" {
		switch {
		case rule == "" && serviceName == "":
			return nil, fmt.Errorf("router %s: missing rule and service", name)
		case rule == "":
			return nil, fmt.Errorf("router %s: missing rule", name)
		default:
			return nil, fmt.Errorf("router %s: missing service", name)
		}
	}

	route := buildRoute(rule, serviceName, mws, priorityVal)

	spec := map[string]interface{}{
		"routes": []interface{}{route},
	}
	// A nil slice would be written as `entryPoints: null`, which the CRD
	// rejects. Without entry points Traefik uses its default ones.
	if len(eps) > 0 {
		spec["entryPoints"] = eps
	}
	if len(tlsObj) > 0 {
		spec["tls"] = convertTLS(tlsObj)
	}
	u := map[string]interface{}{
		"apiVersion": traefikconfig.GroupVersion,
		"kind":       "IngressRoute",
		"metadata":   meta,
		"spec":       spec,
	}
	// Ensure the entryPoints annotation mirrors spec.entryPoints for visibility and downstream tooling.
	AnnotateRouterEntryPointsIfPresent(u, meta)
	return u, nil
}

// extractEntryPoints normalizes spec["entryPoints"] into a []string handling possible types.
func extractEntryPoints(spec map[string]interface{}) []string {
	if spec == nil {
		return nil
	}
	if v, ok := spec["entryPoints"].([]interface{}); ok {
		return parseEntryPointsFromInterface(v)
	}
	if v, ok := spec["entryPoints"].([]string); ok {
		return parseEntryPointsFromStrings(v)
	}
	return nil
}

// parseEntryPointsFromInterface handles []interface{} entryPoints values.
func parseEntryPointsFromInterface(vals []interface{}) []string {
	out := make([]string, 0, len(vals))
	for _, raw := range vals {
		if s, ok := raw.(string); ok {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// parseEntryPointsFromStrings handles []string entryPoints values.
func parseEntryPointsFromStrings(vals []string) []string {
	out := make([]string, 0, len(vals))
	for _, s := range vals {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// parseMiddlewares extracts middleware names from the router raw map.
func parseMiddlewares(r map[string]interface{}) []string {
	if r == nil {
		return nil
	}
	middlewaresIfc, _ := r["middlewares"].([]interface{})
	var mws []string
	for _, m := range middlewaresIfc {
		if s, ok := m.(string); ok {
			mws = append(mws, s)
		}
	}
	return mws
}

// buildRoute constructs the route map used in the IngressRoute spec.
func buildRoute(rule, serviceName string, mws []string, priorityVal float64) map[string]interface{} {
	route := map[string]interface{}{
		"match":    rule,
		"kind":     "Rule",
		"services": []interface{}{map[string]interface{}{"name": serviceName, "kind": "TraefikService"}},
	}
	if len(mws) > 0 {
		var mwRefs []interface{}
		for _, mw := range mws {
			mwRefs = append(mwRefs, map[string]interface{}{"name": mw})
		}
		route["middlewares"] = mwRefs
	}
	if priorityVal != 0 {
		route["priority"] = int(priorityVal)
	}
	return route
}

// convertTLS turns a dynamic-config router TLS block into the IngressRoute
// form, where `options` is a TLSOption reference object instead of a name.
// Passing the bare string through made the API server reject (or Traefik
// ignore) the mTLS options Pangolin attaches to a resource.
func convertTLS(tls map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(tls))
	for k, v := range tls {
		out[k] = v
	}
	if opt, ok := out["options"].(string); ok {
		if opt == "" {
			delete(out, "options")
		} else {
			out["options"] = map[string]interface{}{"name": opt}
		}
	}
	return out
}

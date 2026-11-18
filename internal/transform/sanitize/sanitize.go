package sanitize

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	traefikconfig "pangolin-kube-controller/internal/transform/config"
)

const (
	maxK8sNameLength = 253
	nameHashLength   = 10
	errFmtUnmarshal  = "unmarshal: %w"
	errFmtMarshal    = "marshal: %w"
)

type nameMappings struct {
	middlewares   map[string]string
	services      map[string]string
	transports    map[string]string
	tcpServices   map[string]string
	tcpTransports map[string]string
	udpServices   map[string]string
	tlsOptions    map[string]string
}

// ProviderSeparator separates a resource name from its provider in a Traefik
// cross-provider reference such as "noop@internal" or "pp-transport-v1@file".
const ProviderSeparator = "@"

// IsCrossProviderRef reports whether name references an object of another
// Traefik provider. Those names are resolved by Traefik itself and must reach
// the CRD unchanged.
func IsCrossProviderRef(name string) bool { return strings.Contains(name, ProviderSeparator) }

func sortKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func shortHash(input string) string {
	sum := sha256.Sum256([]byte(input))
	hexed := hex.EncodeToString(sum[:])
	return hexed[:nameHashLength]
}

// SanitizeResourceName normalizes an arbitrary input string into a DNS-1123 like
// name suitable for use as a Kubernetes resource name. It preserves a-z and 0-9,
// collapses groups of non-alphanumeric characters into single '-', trims leading
// and trailing '-', and applies length trimming with a stable hash suffix when
// necessary. Behavior matches the prior implementation but is decomposed for
// reduced cyclomatic complexity.
func SanitizeResourceName(name string) string {
	candidate := buildBaseName(strings.ToLower(name))
	sanitized := strings.Trim(candidate, "-")
	return finalizeSanitizedName(sanitized, name)
}

// buildBaseName converts the lowered input into a base sanitized form (may be empty or too long).
func buildBaseName(lowered string) string {
	var b strings.Builder
	b.Grow(len(lowered))
	lastDash := true // treat start as dash to avoid leading '-'
	for _, r := range lowered {
		if isAlphaNum(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		// any non-alphanumeric (including '.' which original treated as '-') becomes a single '-'
		if lastDash { // skip consecutive or leading replacers
			continue
		}
		b.WriteByte('-')
		lastDash = true
	}
	return b.String()
}

// isAlphaNum reports whether r is an ASCII lowercase letter (a–z) or digit (0–9).
// Input is assumed to be already in lowercase.
func isAlphaNum(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') }

// finalizeSanitizedName returns a Kubernetes-safe name by applying an empty-name fallback,
// trimming to the maximum allowed length, and appending a stable short-hash suffix when needed.
// If `sanitized` is empty, the result is "x-<hash>" where `<hash>` is the fixed-length short hash
// produced by shortHash(original), which returns the first nameHashLength characters of the
// SHA-256 hex digest of `original`. If `sanitized` fits within maxK8sNameLength it is returned
// unchanged. When trimming is required, trailing '-' characters are removed from the base portion,
// room is reserved for a '-' separator and the fixed-length hash suffix, and a single "x" base is
// used if trimming yields an empty base. In degenerate cases where no room exists for a base plus
// separator, the function returns just this fixed-length short hash. The result is guaranteed not
// to exceed maxK8sNameLength.
func finalizeSanitizedName(sanitized, original string) string {
	if sanitized == "" { // empty becomes x-<hash>
		return "x-" + shortHash(original)
	}
	if len(sanitized) <= maxK8sNameLength {
		return sanitized
	}
	// Need to trim & append hash suffix.
	suffix := shortHash(original)
	maxBaseLen := maxK8sNameLength - len(suffix) - 1 // room for '-' separator
	if maxBaseLen < 1 {                              // degenerate: return hash only
		return suffix
	}
	maxTrimLen := maxK8sNameLength
	if maxTrimLen > len(sanitized) {
		maxTrimLen = len(sanitized)
	}
	trimmed := strings.TrimRight(sanitized[:maxTrimLen], "-")
	if len(trimmed) > maxBaseLen {
		trimmed = strings.TrimRight(trimmed[:maxBaseLen], "-")
	}
	if trimmed == "" {
		trimmed = "x"
	}
	return trimmed + "-" + suffix
}

// SanitizeTraefikConfig rewrites config names and references to Kubernetes-safe
// values. Every section the controller reconciles (HTTP, TCP, UDP and TLS
// options) is carried over. References to objects of another Traefik provider
// ("name@provider") are left untouched.
func SanitizeTraefikConfig(cfg *traefikconfig.Config) (*traefikconfig.Config, error) {
	if cfg == nil {
		return nil, nil
	}
	m := &nameMappings{
		middlewares:   mapNames(cfg.HTTP.Middlewares),
		services:      mapNames(cfg.HTTP.Services),
		transports:    mapNames(cfg.HTTP.ServersTransports),
		tcpServices:   map[string]string{},
		tcpTransports: map[string]string{},
		udpServices:   map[string]string{},
		tlsOptions:    map[string]string{},
	}
	if cfg.TCP != nil {
		m.tcpServices = mapNames(cfg.TCP.Services)
		m.tcpTransports = mapNames(cfg.TCP.ServersTransports)
	}
	if cfg.UDP != nil {
		m.udpServices = mapNames(cfg.UDP.Services)
	}
	if cfg.TLS != nil {
		m.tlsOptions = mapNames(cfg.TLS.Options)
	}

	out := &traefikconfig.Config{}
	var err error
	if out.HTTP.Middlewares, err = rewriteSection(cfg.HTTP.Middlewares, m.middlewares, "middleware", func(o map[string]interface{}) {
		sanitizeMiddleware(o, m)
	}); err != nil {
		return nil, err
	}
	if out.HTTP.Services, err = rewriteSection(cfg.HTTP.Services, m.services, "service", func(o map[string]interface{}) {
		sanitizeHTTPService(o, m)
	}); err != nil {
		return nil, err
	}
	if out.HTTP.ServersTransports, err = rewriteSection(cfg.HTTP.ServersTransports, m.transports, "serversTransport", nil); err != nil {
		return nil, err
	}
	if out.HTTP.Routers, err = rewriteSection(cfg.HTTP.Routers, mapNames(cfg.HTTP.Routers), "router", func(o map[string]interface{}) {
		sanitizeRouter(o, m.services, m.middlewares, m.tlsOptions)
	}); err != nil {
		return nil, err
	}

	if cfg.TCP != nil {
		out.TCP = &traefikconfig.TCPUDPConfig{}
		if out.TCP.Services, err = rewriteSection(cfg.TCP.Services, m.tcpServices, "tcp service", func(o map[string]interface{}) {
			rewriteLBTransport(o, m.tcpTransports)
		}); err != nil {
			return nil, err
		}
		if out.TCP.ServersTransports, err = rewriteSection(cfg.TCP.ServersTransports, m.tcpTransports, "tcp serversTransport", nil); err != nil {
			return nil, err
		}
		if out.TCP.Routers, err = rewriteSection(cfg.TCP.Routers, mapNames(cfg.TCP.Routers), "tcp router", func(o map[string]interface{}) {
			sanitizeRouter(o, m.tcpServices, nil, m.tlsOptions)
		}); err != nil {
			return nil, err
		}
	}
	if cfg.UDP != nil {
		out.UDP = &traefikconfig.TCPUDPConfig{}
		if out.UDP.Services, err = rewriteSection(cfg.UDP.Services, m.udpServices, "udp service", nil); err != nil {
			return nil, err
		}
		if out.UDP.Routers, err = rewriteSection(cfg.UDP.Routers, mapNames(cfg.UDP.Routers), "udp router", func(o map[string]interface{}) {
			sanitizeRouter(o, m.udpServices, nil, nil)
		}); err != nil {
			return nil, err
		}
	}
	if cfg.TLS != nil {
		out.TLS = &traefikconfig.TLSConfig{}
		if out.TLS.Options, err = rewriteSection(cfg.TLS.Options, m.tlsOptions, "tls option", nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func mapNames[T any](section map[string]T) map[string]string {
	out := make(map[string]string, len(section))
	for _, name := range sortKeys(section) {
		out[name] = SanitizeResourceName(name)
	}
	return out
}

// rewriteSection renames every entry of a section and applies fix to its body.
// Two source names that sanitize to the same Kubernetes name are an error:
// silently keeping one would drop the other.
func rewriteSection(section map[string]json.RawMessage, names map[string]string, what string, fix func(map[string]interface{})) (map[string]json.RawMessage, error) {
	if section == nil {
		return nil, nil
	}
	out := make(map[string]json.RawMessage, len(section))
	owner := make(map[string]string, len(section))
	for _, name := range sortKeys(section) {
		newName := names[name]
		if prev, dup := owner[newName]; dup {
			return nil, fmt.Errorf("%s %q and %q both map to Kubernetes name %q", what, prev, name, newName)
		}
		owner[newName] = name
		raw := section[name]
		if fix != nil && len(raw) > 0 {
			var obj map[string]interface{}
			if err := json.Unmarshal(raw, &obj); err != nil {
				return nil, fmt.Errorf("%s %s sanitize: "+errFmtUnmarshal, what, name, err)
			}
			if obj != nil {
				fix(obj)
			}
			b, err := json.Marshal(obj)
			if err != nil {
				return nil, fmt.Errorf("%s %s sanitize: "+errFmtMarshal, what, name, err)
			}
			raw = b
		}
		out[newName] = raw
	}
	return out, nil
}

func sanitizeRouter(obj map[string]interface{}, services, middlewares, tlsOptions map[string]string) {
	if svc, ok := obj["service"].(string); ok {
		obj["service"] = sanitizeReference(svc, services)
	}
	if mws, ok := obj["middlewares"].([]interface{}); ok {
		for i, mw := range mws {
			if name, ok := mw.(string); ok {
				mws[i] = sanitizeReference(name, middlewares)
			}
		}
	}
	if tls, ok := obj["tls"].(map[string]interface{}); ok {
		if opt, ok := tls["options"].(string); ok && opt != "" {
			tls["options"] = sanitizeReference(opt, tlsOptions)
		}
	}
}

// sanitizeMiddleware renames chain members and turns them into the
// MiddlewareRef objects the Middleware CRD requires (the file provider uses
// bare strings).
func sanitizeMiddleware(obj map[string]interface{}, m *nameMappings) {
	chain, ok := obj["chain"].(map[string]interface{})
	if !ok {
		return
	}
	mids, ok := chain["middlewares"].([]interface{})
	if !ok {
		return
	}
	refs := make([]interface{}, 0, len(mids))
	for _, mw := range mids {
		switch v := mw.(type) {
		case string:
			refs = append(refs, map[string]interface{}{"name": sanitizeReference(v, m.middlewares)})
		case map[string]interface{}:
			if name, ok := v["name"].(string); ok {
				v["name"] = sanitizeReference(name, m.middlewares)
			}
			refs = append(refs, v)
		}
	}
	chain["middlewares"] = refs
}

func sanitizeHTTPService(obj map[string]interface{}, m *nameMappings) {
	sanitizeWeightedSection(obj, m)
	sanitizeMirroringSection(obj, m)
	sanitizeFailoverSection(obj, m)
	rewriteLBTransport(obj, m.transports)
}

// rewriteLBTransport rewrites loadBalancer.serversTransport to the sanitized
// transport name.
func rewriteLBTransport(obj map[string]interface{}, transports map[string]string) {
	if lb, ok := obj["loadBalancer"].(map[string]interface{}); ok {
		if st, ok := lb["serversTransport"].(string); ok && st != "" {
			lb["serversTransport"] = sanitizeReference(st, transports)
		}
	}
}

// sanitizeWeightedSection rewrites service names in the weighted.services list.
func sanitizeWeightedSection(obj map[string]interface{}, mappings *nameMappings) {
	if weighted, ok := obj["weighted"].(map[string]interface{}); ok {
		if services, ok := weighted["services"].([]interface{}); ok {
			for _, s := range services {
				if svcMap, ok := s.(map[string]interface{}); ok {
					if name, ok := svcMap["name"].(string); ok {
						svcMap["name"] = sanitizeReference(name, mappings.services)
					}
				}
			}
		}
	}
}

// sanitizeMirroringSection rewrites the primary service and mirrors list names.
func sanitizeMirroringSection(obj map[string]interface{}, mappings *nameMappings) {
	mirroring, ok := obj["mirroring"].(map[string]interface{})
	if !ok {
		return
	}
	if service, ok := mirroring["service"].(string); ok {
		mirroring["service"] = sanitizeReference(service, mappings.services)
	}
	if mirrors, ok := mirroring["mirrors"].([]interface{}); ok {
		for _, mirror := range mirrors {
			if mirrorMap, ok := mirror.(map[string]interface{}); ok {
				if name, ok := mirrorMap["name"].(string); ok {
					mirrorMap["name"] = sanitizeReference(name, mappings.services)
				}
			}
		}
	}
}

// sanitizeFailoverSection rewrites failover service & fallback names.
func sanitizeFailoverSection(obj map[string]interface{}, mappings *nameMappings) {
	if failover, ok := obj["failover"].(map[string]interface{}); ok {
		if service, ok := failover["service"].(string); ok {
			failover["service"] = sanitizeReference(service, mappings.services)
		}
		if fallback, ok := failover["fallback"].(string); ok {
			failover["fallback"] = sanitizeReference(fallback, mappings.services)
		}
	}
}

// sanitizeReference maps a reference to the Kubernetes name of the object it
// points at. A name defined in the same config always wins; an undefined name
// containing "@" is a cross-provider reference and is kept as-is.
func sanitizeReference(name string, mapping map[string]string) string {
	if sanitized, exists := mapping[name]; exists {
		return sanitized
	}
	if IsCrossProviderRef(name) {
		return name
	}
	return SanitizeResourceName(name)
}

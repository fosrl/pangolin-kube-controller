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
	middlewares map[string]string
	services    map[string]string
	transports  map[string]string
}

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

// SanitizeTraefikConfig rewrites config names and references to Kubernetes-safe values.
func SanitizeTraefikConfig(cfg *traefikconfig.Config) (*traefikconfig.Config, error) {
	if cfg == nil {
		return nil, nil
	}
	sanitized := &traefikconfig.Config{
		HTTP: traefikconfig.HTTPConfig{
			Middlewares:       make(map[string]json.RawMessage, len(cfg.HTTP.Middlewares)),
			Routers:           make(map[string]json.RawMessage, len(cfg.HTTP.Routers)),
			Services:          make(map[string]json.RawMessage, len(cfg.HTTP.Services)),
			ServersTransports: make(map[string]json.RawMessage, len(cfg.HTTP.ServersTransports)),
		},
		TCP: cfg.TCP,
		UDP: cfg.UDP,
	}
	mappings := &nameMappings{
		middlewares: make(map[string]string, len(cfg.HTTP.Middlewares)),
		services:    make(map[string]string, len(cfg.HTTP.Services)),
		transports:  make(map[string]string, len(cfg.HTTP.ServersTransports)),
	}
	// build mappings and populate sanitized config using small helpers
	buildNameMappings(cfg, mappings)
	if err := populateSanitizedMiddlewares(cfg, sanitized, mappings); err != nil {
		return nil, err
	}
	if err := populateSanitizedServices(cfg, sanitized, mappings); err != nil {
		return nil, err
	}
	populateSanitizedTransports(cfg, sanitized, mappings)
	if err := populateSanitizedRouters(cfg, sanitized, mappings); err != nil {
		return nil, err
	}
	return sanitized, nil
}

func buildNameMappings(cfg *traefikconfig.Config, mappings *nameMappings) {
	for _, name := range sortKeys(cfg.HTTP.Middlewares) {
		mappings.middlewares[name] = SanitizeResourceName(name)
	}
	for _, name := range sortKeys(cfg.HTTP.Services) {
		mappings.services[name] = SanitizeResourceName(name)
	}
	for _, name := range sortKeys(cfg.HTTP.ServersTransports) {
		mappings.transports[name] = SanitizeResourceName(name)
	}
}

func populateSanitizedMiddlewares(cfg *traefikconfig.Config, sanitized *traefikconfig.Config, mappings *nameMappings) error {
	for _, name := range sortKeys(cfg.HTTP.Middlewares) {
		sanitizedName := mappings.middlewares[name]
		sanitizedRaw, err := sanitizeMiddlewareRaw(cfg.HTTP.Middlewares[name], mappings)
		if err != nil {
			return fmt.Errorf("middleware %s sanitize: %w", name, err)
		}
		sanitized.HTTP.Middlewares[sanitizedName] = sanitizedRaw
	}
	return nil
}

func populateSanitizedServices(cfg *traefikconfig.Config, sanitized *traefikconfig.Config, mappings *nameMappings) error {
	for _, name := range sortKeys(cfg.HTTP.Services) {
		sanitizedName := mappings.services[name]
		sanitizedRaw, err := sanitizeServiceRaw(cfg.HTTP.Services[name], mappings)
		if err != nil {
			return fmt.Errorf("service %s sanitize: %w", name, err)
		}
		sanitized.HTTP.Services[sanitizedName] = sanitizedRaw
	}
	return nil
}

func populateSanitizedTransports(cfg *traefikconfig.Config, sanitized *traefikconfig.Config, mappings *nameMappings) {
	for _, name := range sortKeys(cfg.HTTP.ServersTransports) {
		sanitizedName := mappings.transports[name]
		// serversTransport objects are pass-through specs
		sanitized.HTTP.ServersTransports[sanitizedName] = cfg.HTTP.ServersTransports[name]
	}
}

func populateSanitizedRouters(cfg *traefikconfig.Config, sanitized *traefikconfig.Config, mappings *nameMappings) error {
	for _, name := range sortKeys(cfg.HTTP.Routers) {
		sanitizedName := SanitizeResourceName(name)
		sanitizedRaw, err := sanitizeRouterRaw(cfg.HTTP.Routers[name], mappings)
		if err != nil {
			return fmt.Errorf("router %s sanitize: %w", name, err)
		}
		sanitized.HTTP.Routers[sanitizedName] = sanitizedRaw
	}
	return nil
}

func sanitizeRouterRaw(raw json.RawMessage, mappings *nameMappings) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf(errFmtUnmarshal, err)
	}
	if svc, ok := obj["service"].(string); ok {
		obj["service"] = sanitizeReference(svc, mappings.services)
	}
	if mws, ok := obj["middlewares"].([]interface{}); ok {
		for i, mw := range mws {
			if mwName, ok := mw.(string); ok {
				mws[i] = sanitizeReference(mwName, mappings.middlewares)
			}
		}
		obj["middlewares"] = mws
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf(errFmtMarshal, err)
	}
	return json.RawMessage(b), nil
}

func sanitizeMiddlewareRaw(raw json.RawMessage, mappings *nameMappings) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf(errFmtUnmarshal, err)
	}
	if chain, ok := obj["chain"].(map[string]interface{}); ok {
		if mids, ok := chain["middlewares"].([]interface{}); ok {
			for i, mw := range mids {
				if name, ok := mw.(string); ok {
					mids[i] = sanitizeReference(name, mappings.middlewares)
				}
			}
			chain["middlewares"] = mids
		}
		obj["chain"] = chain
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf(errFmtMarshal, err)
	}
	return json.RawMessage(b), nil
}

func sanitizeServiceRaw(raw json.RawMessage, mappings *nameMappings) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf(errFmtUnmarshal, err)
	}
	sanitizeWeightedSection(obj, mappings)
	sanitizeMirroringSection(obj, mappings)
	sanitizeFailoverSection(obj, mappings)
	// Rewrite loadBalancer.serversTransport to sanitized transport name
	if lb, ok := obj["loadBalancer"].(map[string]interface{}); ok {
		if st, ok2 := lb["serversTransport"].(string); ok2 {
			lb["serversTransport"] = sanitizeReference(st, mappings.transports)
			obj["loadBalancer"] = lb
		}
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf(errFmtMarshal, err)
	}
	return json.RawMessage(b), nil
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
			weighted["services"] = services
		}
		obj["weighted"] = weighted
	}
}

// sanitizeMirroringSection rewrites the primary service and mirrors list names.
func sanitizeMirroringSection(obj map[string]interface{}, mappings *nameMappings) {
	mirroring, ok := obj["mirroring"].(map[string]interface{})
	if !ok {
		return
	}
	sanitizeMirroringPrimaryService(mirroring, mappings)
	sanitizeMirroringMirrors(mirroring, mappings)
	obj["mirroring"] = mirroring
}

func sanitizeMirroringPrimaryService(mirroring map[string]interface{}, mappings *nameMappings) {
	service, ok := mirroring["service"].(string)
	if !ok {
		return
	}
	mirroring["service"] = sanitizeReference(service, mappings.services)
}

func sanitizeMirroringMirrors(mirroring map[string]interface{}, mappings *nameMappings) {
	mirrors, ok := mirroring["mirrors"].([]interface{})
	if !ok {
		return
	}
	for _, mirror := range mirrors {
		mirrorMap, ok := mirror.(map[string]interface{})
		if !ok {
			continue
		}
		name, ok := mirrorMap["name"].(string)
		if !ok {
			continue
		}
		mirrorMap["name"] = sanitizeReference(name, mappings.services)
	}
	mirroring["mirrors"] = mirrors
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
		obj["failover"] = failover
	}
}

func sanitizeReference(name string, mapping map[string]string) string {
	if sanitized, exists := mapping[name]; exists {
		return sanitized
	}
	return SanitizeResourceName(name)
}

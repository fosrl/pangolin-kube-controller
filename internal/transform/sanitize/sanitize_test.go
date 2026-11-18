package sanitize

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	traefikconfig "pangolin-kube-controller/internal/transform/config"
)

// Tests from controller_sanitize_test.go

const (
	sanitizedRouterName = "2-echoserver-local-router"
	sanitizedRedirectMw = "redirect-to-https"
	sanitizedK8sBackend = "k8s-backend"
)

func TestSanitizeResourceName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"2-Echoserver-Local-router": "2-echoserver-local-router",
		"BAD VALUE":                 "bad-value",
	}
	for input, expected := range cases {
		input, expected := input, expected
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, expected, SanitizeResourceName(input))
		})
	}

	t.Run("empty fallback", func(t *testing.T) {
		got := SanitizeResourceName("!!!")
		require.True(t, strings.HasPrefix(got, "x-"))
		require.LessOrEqual(t, len(got), maxK8sNameLength)
	})

	t.Run("long value trimmed", func(t *testing.T) {
		longName := strings.Repeat("a", maxK8sNameLength+50)
		got := SanitizeResourceName(longName)
		require.LessOrEqual(t, len(got), maxK8sNameLength)
		require.True(t, strings.HasPrefix(got, strings.Repeat("a", maxK8sNameLength-nameHashLength-1)))
	})
}

func TestSanitizeTraefikConfig(t *testing.T) {
	cfg := &traefikconfig.Config{
		HTTP: traefikconfig.HTTPConfig{
			Middlewares: map[string]json.RawMessage{
				"Redirect-To-HTTPS": json.RawMessage(`{"foo":"bar"}`),
			},
			Services: map[string]json.RawMessage{
				"2-Echoserver-Local-service": json.RawMessage(`{"loadBalancer":{}}`),
			},
			Routers: map[string]json.RawMessage{
				"2-Echoserver-Local-router": json.RawMessage(`{"service":"2-Echoserver-Local-service","middlewares":["Redirect-To-HTTPS"]}`),
			},
		},
	}

	sanitized, err := SanitizeTraefikConfig(cfg)
	require.NoError(t, err)
	require.NotNil(t, sanitized)

	require.Contains(t, sanitized.HTTP.Services, "2-echoserver-local-service")
	require.Contains(t, sanitized.HTTP.Middlewares, sanitizedRedirectMw)
	require.Contains(t, sanitized.HTTP.Routers, sanitizedRouterName)

	var router map[string]interface{}
	require.NoError(t, json.Unmarshal(sanitized.HTTP.Routers[sanitizedRouterName], &router))
	require.Equal(t, "2-echoserver-local-service", router["service"])

	rawMws, ok := router["middlewares"].([]interface{})
	require.True(t, ok)
	require.Contains(t, rawMws, sanitizedRedirectMw)
}

func TestSanitizeTraefikConfigPreservesTCPAndUDP(t *testing.T) {
	tcp := &traefikconfig.TCPUDPConfig{
		Routers:  map[string]json.RawMessage{"tcp-router": json.RawMessage(`{}`)},
		Services: map[string]json.RawMessage{"tcp-service": json.RawMessage(`{}`)},
	}
	udp := &traefikconfig.TCPUDPConfig{
		Routers:  map[string]json.RawMessage{"udp-router": json.RawMessage(`{}`)},
		Services: map[string]json.RawMessage{"udp-service": json.RawMessage(`{}`)},
	}

	sanitized, err := SanitizeTraefikConfig(&traefikconfig.Config{TCP: tcp, UDP: udp})
	require.NoError(t, err)
	require.Same(t, tcp, sanitized.TCP)
	require.Same(t, udp, sanitized.UDP)
}

func TestSanitizeTraefikConfigRewritesNestedReferences(t *testing.T) {
	cfg := &traefikconfig.Config{
		HTTP: traefikconfig.HTTPConfig{
			Middlewares: map[string]json.RawMessage{
				"Chain Middleware":  json.RawMessage(`{"chain":{"middlewares":["Redirect-To-HTTPS","Other_Middleware"]}}`),
				"Redirect-To-HTTPS": json.RawMessage(`{"headers":{}}`),
				"Other_Middleware":  json.RawMessage(`{"plugin":{}}`),
			},
			Services: map[string]json.RawMessage{
				"Primary Service":      json.RawMessage(`{"weighted":{"services":[{"name":"Mirror Service"},{"name":"Failover Service"}]}}`),
				"Mirror Service":       json.RawMessage(`{"mirroring":{"service":"LoadBalancer_Service","mirrors":[{"name":"Fallback Service"}]}}`),
				"Failover Service":     json.RawMessage(`{"failover":{"service":"LoadBalancer_Service","fallback":"Extra Service"}}`),
				"LoadBalancer_Service": json.RawMessage(`{"loadBalancer":{}}`),
				"Fallback Service":     json.RawMessage(`{"loadBalancer":{}}`),
				"Extra Service":        json.RawMessage(`{"loadBalancer":{}}`),
			},
			Routers: map[string]json.RawMessage{
				"Example Router": json.RawMessage(`{"service":"Primary Service","middlewares":["Chain Middleware"]}`),
			},
		},
	}

	sanitized, err := SanitizeTraefikConfig(cfg)
	require.NoError(t, err)

	var weightedSvc map[string]interface{}
	require.NoError(t, json.Unmarshal(sanitized.HTTP.Services["primary-service"], &weightedSvc))
	weighted, ok := weightedSvc["weighted"].(map[string]interface{})
	require.True(t, ok)
	services, ok := weighted["services"].([]interface{})
	require.True(t, ok)
	require.Len(t, services, 2)
	firstSvc, ok := services[0].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "mirror-service", firstSvc["name"])
	secondSvc, ok := services[1].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "failover-service", secondSvc["name"])

	var mirrorSvc map[string]interface{}
	require.NoError(t, json.Unmarshal(sanitized.HTTP.Services["mirror-service"], &mirrorSvc))
	mirroring, ok := mirrorSvc["mirroring"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "loadbalancer-service", mirroring["service"])
	mirrors, ok := mirroring["mirrors"].([]interface{})
	require.True(t, ok)
	require.Len(t, mirrors, 1)
	mirrorRef, ok := mirrors[0].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "fallback-service", mirrorRef["name"])

	var failoverSvc map[string]interface{}
	require.NoError(t, json.Unmarshal(sanitized.HTTP.Services["failover-service"], &failoverSvc))
	failover, ok := failoverSvc["failover"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "loadbalancer-service", failover["service"])
	require.Equal(t, "extra-service", failover["fallback"])

	var chainMw map[string]interface{}
	require.NoError(t, json.Unmarshal(sanitized.HTTP.Middlewares["chain-middleware"], &chainMw))
	chain, ok := chainMw["chain"].(map[string]interface{})
	require.True(t, ok)
	mwRefs, ok := chain["middlewares"].([]interface{})
	require.True(t, ok)
	require.Contains(t, mwRefs, sanitizedRedirectMw)
	require.Contains(t, mwRefs, "other-middleware")

	var router map[string]interface{}
	require.NoError(t, json.Unmarshal(sanitized.HTTP.Routers["example-router"], &router))
	require.Equal(t, "primary-service", router["service"])
	routerMws, ok := router["middlewares"].([]interface{})
	require.True(t, ok)
	require.Contains(t, routerMws, "chain-middleware")
}

func TestSanitizeTraefikConfigPreservesKubernetesServiceRefs(t *testing.T) {
	cfg := &traefikconfig.Config{
		HTTP: traefikconfig.HTTPConfig{
			Services: map[string]json.RawMessage{
				"k8s-backend": json.RawMessage(`{"weighted":{"services":[{"name":"echoserver","namespace":"dummyservices","kind":"Service","port":80}]}}`),
			},
		},
	}

	sanitized, err := SanitizeTraefikConfig(cfg)
	require.NoError(t, err)
	require.Contains(t, sanitized.HTTP.Services, sanitizedK8sBackend)

	var spec map[string]interface{}
	require.NoError(t, json.Unmarshal(sanitized.HTTP.Services[sanitizedK8sBackend], &spec))
	weighted, ok := spec["weighted"].(map[string]interface{})
	require.True(t, ok)
	services, ok := weighted["services"].([]interface{})
	require.True(t, ok)
	require.Len(t, services, 1)
	entry, ok := services[0].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "echoserver", entry["name"])
	require.Equal(t, "dummyservices", entry["namespace"])
	require.Equal(t, "Service", entry["kind"])
}

// Tests from controller_sanitize_config_test.go

// shared setup for sanitize tests
func sanitizeTestSetup(t *testing.T) (sanitized *traefikconfig.Config, orig *traefikconfig.Config, routerMap map[string]interface{}) {
	t.Helper()
	// Build a config with unsanitized names and references.
	midRaw := json.RawMessage(`{"chain":{"middlewares":["My$Middleware"]}}`)
	svcRaw := json.RawMessage(`{"weighted":{"services":[{"name":"Svc@One","kind":"TraefikService"}]},"loadBalancer":{"servers":[{"address":"h.example:8081"}],"serversTransport":"Trans!Port"}}`)
	routerRaw := json.RawMessage(`{"service":"Svc@One","middlewares":["My$Middleware"],"rule":"Host(\"ex.com\")"}`)
	stRaw := json.RawMessage(`{"foo":"bar"}`)

	cfg := &traefikconfig.Config{HTTP: traefikconfig.HTTPConfig{
		Middlewares:       map[string]json.RawMessage{"My$Middleware": midRaw},
		Services:          map[string]json.RawMessage{"Svc@One": svcRaw},
		Routers:           map[string]json.RawMessage{"Router#1": routerRaw},
		ServersTransports: map[string]json.RawMessage{"Trans!Port": stRaw},
	}}

	sanitized, err := SanitizeTraefikConfig(cfg)
	require.NoError(t, err)

	// Unmarshal the single router into a map for later checks.
	routerMap = map[string]interface{}{}
	for _, raw := range sanitized.HTTP.Routers { // single router
		require.NoError(t, json.Unmarshal(raw, &routerMap))
	}

	return sanitized, cfg, routerMap
}

func TestSanitizeTraefikConfigKeysSanitized(t *testing.T) {
	sanitized, _, _ := sanitizeTestSetup(t)
	// Ensure keys were sanitized (no '$', '@', '!' or '#').
	for k := range sanitized.HTTP.Middlewares {
		require.Falsef(t, strings.ContainsAny(k, "$@!#"), "middleware key not sanitized: %s", k)
	}
	for k := range sanitized.HTTP.Services {
		require.Falsef(t, strings.ContainsAny(k, "$@!#"), "service key not sanitized: %s", k)
	}
	for k := range sanitized.HTTP.Routers {
		require.Falsef(t, strings.ContainsAny(k, "$@!#"), "router key not sanitized: %s", k)
	}
}

func TestSanitizeServersTransportsPreserved(t *testing.T) {
	sanitized, cfg, _ := sanitizeTestSetup(t)
	// Validate serversTransport keys sanitized and values preserved.
	for origKey, rawVal := range cfg.HTTP.ServersTransports {
		expectedKey := SanitizeResourceName(origKey)
		val, ok := sanitized.HTTP.ServersTransports[expectedKey]
		require.Truef(t, ok, "sanitized serversTransport key missing: %s", expectedKey)
		require.Falsef(t, strings.ContainsAny(expectedKey, "$@!#"), "serversTransport key not sanitized: %s", expectedKey)
		require.Equalf(t, string(rawVal), string(val), "serversTransport spec mutated unexpectedly for %s", expectedKey)
	}
}

func TestSanitizeRouterReferencesRewritten(t *testing.T) {
	_, _, r := sanitizeTestSetup(t)
	svcNameIfc, ok := r["service"]
	require.True(t, ok, "router missing service field")
	svcName, ok := svcNameIfc.(string)
	require.Truef(t, ok, "router service field not a string: %#v", svcNameIfc)
	require.Falsef(t, strings.ContainsAny(svcName, "$@!#"), "service reference not sanitized: %s", svcName)
	mwsIfc, ok := r["middlewares"]
	require.True(t, ok, "router missing middlewares field")
	mws, ok := mwsIfc.([]interface{})
	require.Truef(t, ok, "middlewares field wrong type: %#v", mwsIfc)
	require.NotEmpty(t, mws, "expected middleware reference rewritten")
	mwName, ok := mws[0].(string)
	require.Truef(t, ok, "middleware element not string: %#v", mws[0])
	require.Falsef(t, strings.ContainsAny(mwName, "$@!#"), "middleware reference not sanitized: %s", mwName)
}

// Tests from controller_sanitize_fuzz_test.go

// FuzzSanitizeResourceName fuzzes the SanitizeResourceName function to ensure
// it handles arbitrary input strings without panicking and always produces
// valid Kubernetes resource names.
func FuzzSanitizeResourceName(f *testing.F) {
	// Seed with some initial test cases
	for _, seed := range []string{
		"valid-name",
		"UPPERCASE",
		"With Spaces",
		"special!@#$%chars",
		"",
		"a",
		strings.Repeat("x", 300),
		"123-start-with-number",
		"end-with-dash-",
		"-start-with-dash",
		"multiple---dashes",
		"dots.in.name",
		"unicode-\u00e9\u00fc",
		"emoji-🚀-test",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		validateSanitizedName(t, input, SanitizeResourceName(input))
	})
}

// validateSanitizedName contains the assertions for a sanitized resource name.
// Keeping it separate reduces the cognitive complexity of the fuzz driver itself.
func validateSanitizedName(t *testing.T, input, result string) {
	if result == "" {
		t.Errorf("SanitizeResourceName returned empty string for input %q", input)
		return
	}
	if len(result) > maxK8sNameLength {
		t.Errorf("SanitizeResourceName returned name longer than %d: got %d for input %q", maxK8sNameLength, len(result), input)
	}
	if !utf8.ValidString(result) {
		t.Errorf("SanitizeResourceName returned invalid UTF-8 for input %q", input)
	}
	if strings.HasPrefix(result, "-") || strings.HasSuffix(result, "-") {
		t.Errorf("SanitizeResourceName returned name starting/ending with dash: %q for input %q", result, input)
	}
	if result != strings.ToLower(result) {
		t.Errorf("SanitizeResourceName returned non-lowercase name: %q for input %q", result, input)
	}
	for _, r := range result {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		t.Errorf("SanitizeResourceName returned name with invalid character %q: %q for input %q", r, result, input)
		break
	}
}

// Tests from controller_sanitize_name_test.go

func TestSanitizeResourceNameEmptyAndNormalization(t *testing.T) {
	if got := SanitizeResourceName(""); !strings.HasPrefix(got, "x-") || len(got) < 3 {
		t.Fatalf("empty name fallback unexpected: %q", got)
	}
	if SanitizeResourceName("A..B__C") != "a-b-c" {
		t.Fatalf("normalization failed")
	}
}

func TestSanitizeResourceNameLongNameTrimmed(t *testing.T) {
	long := strings.Repeat("a", 300)
	got := SanitizeResourceName(long)
	if len(got) > maxK8sNameLength {
		t.Fatalf("sanitized name too long: %d", len(got))
	}
}

// Additional tests for finalizeSanitizedName edge cases

func TestFinalizeSanitizedNameEmptyWithVariousOriginals(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		original string
	}{
		{"symbols only", "!!!@@@###"},
		{"whitespace", "   "},
		{"unicode", "你好世界"},
		{"mixed invalid", "!@# $%^"},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := finalizeSanitizedName("", tc.original)
			require.True(t, strings.HasPrefix(result, "x-"), "should have x- prefix")
			require.LessOrEqual(t, len(result), maxK8sNameLength, "should not exceed max length")
			require.Greater(t, len(result), 2, "should have hash suffix")
		})
	}
}

func TestFinalizeSanitizedNameHashConsistency(t *testing.T) {
	t.Parallel()
	original := "test-original-name-that-will-be-hashed"

	// Call multiple times with same input
	result1 := finalizeSanitizedName("", original)
	result2 := finalizeSanitizedName("", original)
	result3 := finalizeSanitizedName("", original)

	require.Equal(t, result1, result2, "hash should be consistent")
	require.Equal(t, result2, result3, "hash should be consistent")
}

func TestFinalizeSanitizedNameDifferentOriginalsProduceDifferentHashes(t *testing.T) {
	t.Parallel()
	result1 := finalizeSanitizedName("", "original1")
	result2 := finalizeSanitizedName("", "original2")

	require.NotEqual(t, result1, result2, "different originals should produce different hashes")
}

func TestFinalizeSanitizedNameLongNameTrimming(t *testing.T) {
	t.Parallel()
	// Create a sanitized name that's exactly at the limit
	atLimit := strings.Repeat("a", maxK8sNameLength)
	resultAtLimit := finalizeSanitizedName(atLimit, "original")
	require.Equal(t, atLimit, resultAtLimit, "name at limit should not be trimmed")

	// Create one that's over the limit
	overLimit := strings.Repeat("b", maxK8sNameLength+10)
	resultOverLimit := finalizeSanitizedName(overLimit, "original")
	require.LessOrEqual(t, len(resultOverLimit), maxK8sNameLength, "over-limit should be trimmed")
	require.True(t, strings.HasPrefix(resultOverLimit, strings.Repeat("b", maxK8sNameLength-nameHashLength-1)), "should preserve prefix")
}

func TestFinalizeSanitizedNameValidInputPassthrough(t *testing.T) {
	t.Parallel()
	validInputs := []string{
		"short",
		"medium-length-name",
		"with-numbers-123",
		strings.Repeat("x", 50), // Within limit
	}

	for _, input := range validInputs {
		input := input
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			result := finalizeSanitizedName(input, "original")
			require.Equal(t, input, result, "valid input should pass through unchanged")
		})
	}
}

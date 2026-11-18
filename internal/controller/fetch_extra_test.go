package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"pangolin-kube-controller/internal/config"
)

// ---- envBool ----------------------------------------------------------------

func TestEnvBoolTrueVariants(t *testing.T) {
	for _, val := range []string{"true", "1", "yes", "TRUE", "YES", "True"} {
		val := val
		t.Run(val, func(t *testing.T) {
			key := "__ENVBOOL_TEST_" + val + "__"
			t.Setenv(key, val)
			require.True(t, envBool(key), "expected true for %q", val)
		})
	}
}

func TestEnvBoolFalseVariants(t *testing.T) {
	for _, val := range []string{"false", "0", "no", "maybe", ""} {
		val := val
		t.Run("val="+val, func(t *testing.T) {
			key := "__ENVBOOL_FALSE_TEST_" + val + "__"
			t.Setenv(key, val)
			require.False(t, envBool(key), "expected false for %q", val)
		})
	}
}

func TestEnvBoolMissingKey(t *testing.T) {
	// An unset env var should return false.
	require.False(t, envBool("__ENVBOOL_UNSET_KEY_PANGOLIN_TEST__"))
}

// ---- loadCustomCA -----------------------------------------------------------

func TestLoadCustomCAMissingFile(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{CAFile: "/nonexistent/pangolin-test-ca.pem"}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	// Should log a warning but not panic and not set RootCAs.
	loadCustomCA(cfg, tlsCfg)
	require.Nil(t, tlsCfg.RootCAs, "RootCAs must not be set when file is missing")
}

func TestLoadCustomCAEmptyPEM(t *testing.T) {
	t.Parallel()

	f, err := os.CreateTemp(t.TempDir(), "ca-empty*.pem")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	cfg := &config.Config{CAFile: f.Name()}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	loadCustomCA(cfg, tlsCfg)
	// Pool is created even when no certs are appended (AppendCertsFromPEM == false).
	require.NotNil(t, tlsCfg.RootCAs, "RootCAs pool should be created even for empty PEM file")
}

func TestLoadCustomCAValidSelfSignedPEM(t *testing.T) {
	t.Parallel()

	certFile := writeSelfSignedCert(t, "test-ca")

	cfg := &config.Config{CAFile: certFile}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	loadCustomCA(cfg, tlsCfg)
	require.NotNil(t, tlsCfg.RootCAs, "RootCAs must be populated with valid PEM cert")
}

// ---- loadClientCert ---------------------------------------------------------

func TestLoadClientCertBothEmpty(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	loadClientCert(cfg, tlsCfg)
	require.Empty(t, tlsCfg.Certificates, "no cert should be loaded when paths are empty")
}

func TestLoadClientCertOnlyKeySet(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{ClientKeyFile: "/tmp/key.pem"}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	// Only one of the pair is set – should be a no-op.
	loadClientCert(cfg, tlsCfg)
	require.Empty(t, tlsCfg.Certificates)
}

func TestLoadClientCertInvalidFiles(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		ClientCertFile: "/nonexistent/cert.pem",
		ClientKeyFile:  "/nonexistent/key.pem",
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	// Should log an error and not set any cert.
	loadClientCert(cfg, tlsCfg)
	require.Empty(t, tlsCfg.Certificates)
}

func TestLoadClientCertValidPair(t *testing.T) {
	t.Parallel()

	certFile, keyFile := writeSelfSignedClientCertPair(t)

	cfg := &config.Config{
		ClientCertFile: certFile,
		ClientKeyFile:  keyFile,
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	loadClientCert(cfg, tlsCfg)
	require.Len(t, tlsCfg.Certificates, 1, "expected exactly one TLS certificate loaded")
}

// ---- fetchConfigOnce --------------------------------------------------------

// roundTripFn is a helper to create an http.RoundTripper from a function.
type roundTripFn func(*http.Request) (*http.Response, error)

func (f roundTripFn) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFetchConfigOnceInsecureHTTPBlocked(t *testing.T) {
	t.Parallel()

	c := NewController(&config.Config{
		Endpoint:          "http://127.0.0.1:9999/config",
		AllowInsecureHTTP: false,
	}, nil, nil, nil)

	_, _, _, err := c.fetchConfigOnce(context.Background(), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "plaintext HTTP")
}

func TestFetchConfigOnceInsecureHTTPAllowedViaConfig(t *testing.T) {
	t.Parallel()

	called := false
	c := NewController(&config.Config{
		Endpoint:          "http://127.0.0.1:0/config",
		AllowInsecureHTTP: true,
	}, nil, nil, nil)
	c.httpClient = &http.Client{
		Transport: roundTripFn(func(_ *http.Request) (*http.Response, error) {
			called = true
			return nil, fmt.Errorf("connection refused (test)")
		}),
	}

	_, _, _, err := c.fetchConfigOnce(context.Background(), "")
	require.Error(t, err)
	require.True(t, called, "HTTP transport should have been invoked past the HTTP-allowed check")
}

func TestFetchConfigOnceInsecureHTTPAllowedViaEnv(t *testing.T) {
	t.Setenv("CONFIG_ALLOW_INSECURE_HTTP", "true")

	called := false
	c := NewController(&config.Config{
		Endpoint:          "http://127.0.0.1:0/config",
		AllowInsecureHTTP: false, // falls back to env var
	}, nil, nil, nil)
	c.httpClient = &http.Client{
		Transport: roundTripFn(func(_ *http.Request) (*http.Response, error) {
			called = true
			return nil, fmt.Errorf("connection refused (test)")
		}),
	}

	_, _, _, err := c.fetchConfigOnce(context.Background(), "")
	require.Error(t, err)
	require.True(t, called, "env var override should allow past HTTP check")
}

// ---- helpers ----------------------------------------------------------------

// writeSelfSignedCert creates a self-signed CA cert PEM file in t.TempDir()
// and returns its path.
func writeSelfSignedCert(t *testing.T, cn string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	f, err := os.CreateTemp(t.TempDir(), "cert*.pem")
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	require.NoError(t, pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return f.Name()
}

// writeSelfSignedClientCertPair returns paths to a self-signed cert and key PEM
// pair suitable for mTLS client auth.
func writeSelfSignedClientCertPair(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	dir := t.TempDir()
	certFile := dir + "/client.pem"
	keyFile := dir + "/client.key"

	cf, err := os.Create(certFile)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: der}))
	require.NoError(t, cf.Close())

	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	kf, err := os.Create(keyFile)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(kf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	require.NoError(t, kf.Close())

	return certFile, keyFile
}

package controller

import (
	"context"
	"net/http"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	fakekube "k8s.io/client-go/kubernetes/fake"

	"pangolin-kube-controller/internal/config"
	prometheus "pangolin-kube-controller/internal/observability/metrics_prometheus"
	applytest "pangolin-kube-controller/internal/testutil"
	"pangolin-kube-controller/internal/transform/testutil"
)

const testETagV1 = "\"v1\""

const testEndpoint = "https://example"

type rt304Signal struct{ ch chan struct{} }

func (rt rt304Signal) RoundTrip(*http.Request) (*http.Response, error) {
	select {
	case rt.ch <- struct{}{}:
	default:
	}
	resp := &http.Response{StatusCode: http.StatusNotModified, Body: http.NoBody, Header: make(http.Header)}
	resp.Header.Set("ETag", "\"v1\"")
	return resp, nil
}

func TestRunLoopNotModifiedCancels(t *testing.T) {
	cfg := &config.Config{Endpoint: testEndpoint, PollInterval: 10 * time.Millisecond}
	c := NewController(cfg, nil, nil, prometheus.NewCollector())
	requestMade := make(chan struct{}, 1)
	c.httpClient = &http.Client{Transport: rt304Signal{ch: requestMade}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 1)
	go func() { c.runLoop(ctx); done <- struct{}{} }()
	select {
	case <-requestMade:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatalf("did not perform initial request in time")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("runLoop did not exit in time")
	}
}

type rt200Signal struct{ ch chan struct{} }

func (rt rt200Signal) RoundTrip(*http.Request) (*http.Response, error) {
	select {
	case rt.ch <- struct{}{}:
	default:
	}
	resp := &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}
	resp.Header.Set("ETag", "\"v2\"")
	return resp, nil
}

func TestRunLoopSuccess200Cancels(t *testing.T) {
	mc := prometheus.NewCollector()
	cfg := &config.Config{Endpoint: testEndpoint, PollInterval: 10 * time.Millisecond}
	listKinds := map[schema.GroupVersionResource]string{
		{Group: testutil.TestTraefikGroup, Version: "v1alpha1", Resource: "middlewares"}:          "MiddlewareList",
		{Group: testutil.TestTraefikGroup, Version: "v1alpha1", Resource: "ingressroutes"}:        "IngressRouteList",
		{Group: testutil.TestTraefikGroup, Version: "v1alpha1", Resource: "traefikservices"}:      "TraefikServiceList",
		{Group: testutil.TestTraefikGroup, Version: "v1alpha1", Resource: "serverstransports"}:    "ServersTransportList",
		{Group: testutil.TestTraefikGroup, Version: "v1alpha1", Resource: "ingressroutetcps"}:     "IngressRouteTCPList",
		{Group: testutil.TestTraefikGroup, Version: "v1alpha1", Resource: "ingressrouteudps"}:     "IngressRouteUDPList",
		{Group: testutil.TestTraefikGroup, Version: "v1alpha1", Resource: "serverstransporttcps"}: "ServersTransportTCPList",
		{Group: testutil.TestTraefikGroup, Version: "v1alpha1", Resource: "tlsoptions"}:           "TLSOptionList",
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	applytest.InstallApplyReactor(dyn)
	kube := fakekube.NewClientset()
	c := NewController(cfg, dyn, kube, mc)

	requestMade := make(chan struct{}, 1)
	c.httpClient = &http.Client{Transport: rt200Signal{ch: requestMade}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{}, 1)
	go func() { c.runLoop(ctx); done <- struct{}{} }()

	select {
	case <-requestMade:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatalf("did not perform request in time")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("runLoop did not exit in time after cancel")
	}
}

func TestComputeIfNoneMatch(t *testing.T) {
	c := newCtrlForTest()
	if v := c.computeIfNoneMatch(false, "etag"); v != "" {
		t.Fatalf("expected empty If-None-Match when last etag not from header; got %q", v)
	}
	if v := c.computeIfNoneMatch(true, "etag"); v != "etag" {
		t.Fatalf("expected passthrough etag; got %q", v)
	}
}

func TestUpdateSignaturesAfterSuccess(t *testing.T) {
	// Pre-computed SHA-256 hex digests for the fixed test inputs below.
	// sha256("body")  = 230d8358dc8e8890b4c58deeb62912ee2f20357ae92a5cc861b98e68fe31acb5
	// sha256("body2") = 9392684867789a5a45bce5851e1270ef1a26a35c4b5ea37e91d6d714a95653b0
	const hashOfBody = "230d8358dc8e8890b4c58deeb62912ee2f20357ae92a5cc861b98e68fe31acb5"
	const hashOfBody2 = "9392684867789a5a45bce5851e1270ef1a26a35c4b5ea37e91d6d714a95653b0"

	c := newCtrlForTest()
	var lastETag string
	var lastIsHeader bool
	var lastHash string

	// First call: ETag provided — should be recorded as a header ETag.
	c.updateSignaturesAfterSuccess(testETagV1, []byte("body"), &lastETag, &lastIsHeader, &lastHash)
	if !lastIsHeader || lastETag != testETagV1 {
		t.Fatalf("etag not recorded correctly: %v %q", lastIsHeader, lastETag)
	}
	if lastHash != hashOfBody {
		t.Fatalf("hash mismatch after first call: got %q, want %q", lastHash, hashOfBody)
	}

	// Second call: empty ETag — should clear the header ETag flags and update hash.
	c.updateSignaturesAfterSuccess("", []byte("body2"), &lastETag, &lastIsHeader, &lastHash)
	if lastIsHeader || lastETag != "" {
		t.Fatalf("etag header flags not cleared: %v %q", lastIsHeader, lastETag)
	}
	if lastHash != hashOfBody2 {
		t.Fatalf("hash mismatch after second call: got %q, want %q", lastHash, hashOfBody2)
	}
}

func TestShouldProceedAfterStatusMatrix(t *testing.T) {
	c := newCtrlForTest()
	_, _, proceed, ok, _ := c.shouldProceedAfterStatus(nil, testETagV1, 304, testETagV1, "hash", true)
	if !ok || proceed {
		t.Fatalf("304 should be ok and not proceed")
	}
	b, etag, proceed, ok, _ := c.shouldProceedAfterStatus(nil, testETagV1, 404, "", "", false)
	if !ok || !proceed || etag != "" || string(b) != "{}" {
		t.Fatalf("404 handling unexpected: ok=%v proceed=%v etag=%q body=%q", ok, proceed, etag, string(b))
	}
	_, _, _, ok, _ = c.shouldProceedAfterStatus(nil, "", 401, "", "", false)
	if ok {
		t.Fatalf("401 should not be ok")
	}
	_, _, _, ok, _ = c.shouldProceedAfterStatus(nil, "", 500, "", "", false)
	if ok {
		t.Fatalf("500 should not be ok")
	}
	_, _, _, ok, _ = c.shouldProceedAfterStatus(nil, "", 418, "", "", false)
	if ok {
		t.Fatalf("unexpected status should not be ok")
	}
}

func newCtrlForTest() *Controller {
	cfg := &config.Config{
		Endpoint: testEndpoint,
	}
	return NewController(cfg, nil, nil, nil)
}

func TestOrNone(t *testing.T) {
	c := newCtrlForTest()
	if got := c.orNone(""); got != "<none>" {
		t.Errorf("orNone(\"\") = %q, want \"<none>\"", got)
	}
	if got := c.orNone("etag"); got != "etag" {
		t.Errorf("orNone(\"etag\") = %q, want \"etag\"", got)
	}
}

func TestShouldLogFetchInfo(t *testing.T) {
	c := newCtrlForTest()
	now := time.Now()

	// zero interval: only log on first call (last is zero)
	c.cfg.FetchLogInterval = 0
	if !c.shouldLogFetchInfo(now, time.Time{}) {
		t.Error("expected true when last is zero and interval is 0")
	}
	if c.shouldLogFetchInfo(now, now) {
		t.Error("expected false when last is not zero and interval is 0")
	}

	// positive interval: log when enough time has elapsed
	c.cfg.FetchLogInterval = time.Minute
	if !c.shouldLogFetchInfo(now, time.Time{}) {
		t.Error("expected true when last is zero")
	}
	if c.shouldLogFetchInfo(now, now) {
		t.Error("expected false when last == now")
	}
	longAgo := now.Add(-2 * time.Minute)
	if !c.shouldLogFetchInfo(now, longAgo) {
		t.Error("expected true when elapsed >= interval")
	}
}

// smoke test: ensures no panic when logging is disabled
func TestLogFetchedBodyIfEnabledDisabled(_ *testing.T) {
	c := newCtrlForTest()
	c.cfg.LogConfigPreview = false
	c.cfg.LogTraefikConfig = false
	c.logFetchedBodyIfEnabled([]byte(`{"key":"value"}`), "etag1")
}

// smoke test: ensures no panic when logging enabled with unlimited preview
func TestLogFetchedBodyIfEnabledEnabled(_ *testing.T) {
	c := newCtrlForTest()
	c.cfg.LogConfigPreview = true
	c.cfg.MaxConfigLogBytes = 0
	c.logFetchedBodyIfEnabled([]byte(`{"key":"value"}`), "etag1")
}

// smoke test: ensures no panic when logging truncation occurs
func TestLogFetchedBodyIfEnabledTruncated(_ *testing.T) {
	c := newCtrlForTest()
	c.cfg.LogConfigPreview = true
	c.cfg.MaxConfigLogBytes = 5
	c.logFetchedBodyIfEnabled([]byte(`{"key":"value-that-is-long"}`), "etag2")
}

// smoke test: ensures no panic when fetched body is non-JSON
func TestLogFetchedBodyIfEnabledNonJSON(_ *testing.T) {
	c := newCtrlForTest()
	c.cfg.LogConfigPreview = true
	c.cfg.MaxConfigLogBytes = 0
	c.logFetchedBodyIfEnabled([]byte(`not-json`), "etag3")
}

// smoke test: ensures no panic when fetched body is empty
func TestLogFetchedBodyIfEnabledEmptyBody(_ *testing.T) {
	c := newCtrlForTest()
	c.cfg.LogConfigPreview = true
	c.cfg.MaxConfigLogBytes = 100
	c.logFetchedBodyIfEnabled([]byte{}, "")
}

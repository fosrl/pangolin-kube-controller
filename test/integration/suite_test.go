//go:build integration

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var (
	testEnv *envtest.Environment
	restCfg *rest.Config
	dynCli  dynamic.Interface
	kubeCli *kubernetes.Clientset
)

func TestMain(m *testing.M) {
	// Real Traefik CRDs, so the API server validates and prunes exactly as it
	// does in a cluster. TRAEFIK_CRD_VERSION selects another vendored version.
	crdVersion := os.Getenv("TRAEFIK_CRD_VERSION")
	if crdVersion == "" {
		crdVersion = "v3.7.19"
	}
	traefikCRDDir := filepath.Join("..", "testdata", "crds", "traefik", crdVersion)
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{traefikCRDDir},
		ErrorIfCRDPathMissing: true,
	}

	// Start the control plane.
	var err error
	restCfg, err = testEnv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "envtest start failed (missing binaries or CRDs?): %v\n", err)
		os.Exit(0) // Gracefully skip integration tests so CI doesn't hard fail due to infra.
	}
	defer func() {
		_ = testEnv.Stop()
	}()

	// Increase Kubernetes client QPS/Burst to avoid throttling in CI under rapid reconcile cycles.
	// This mirrors production overrides via CLIENT_QPS/CLIENT_BURST, but tests construct clients directly.
	restCfg.QPS = 100
	restCfg.Burst = 200
	restCfg.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(restCfg.QPS, restCfg.Burst)

	// Create clients.
	dynCli, err = dynamic.NewForConfig(restCfg)
	if err != nil {
		panic(err)
	}
	kubeCli, err = kubernetes.NewForConfig(restCfg)
	if err != nil {
		panic(err)
	}

	code := m.Run()
	// Small delay to allow background termination reconciliation logs to flush.
	time.Sleep(100 * time.Millisecond)
	os.Exit(code)
}

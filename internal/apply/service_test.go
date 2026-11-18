package apply

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakekube "k8s.io/client-go/kubernetes/fake"
)

func TestBoolPtr(t *testing.T) {
	if got := BoolPtr(true); *got != true {
		t.Errorf("BoolPtr(true) = %v, want true", *got)
	}
	if got := BoolPtr(false); *got != false {
		t.Errorf("BoolPtr(false) = %v, want false", *got)
	}
}

func TestKindFor(t *testing.T) {
	tests := []struct {
		resource string
		want     string
	}{
		{"ingressroutes", "IngressRoute"},
		{"middlewares", "Middleware"},
		{"traefikservices", "TraefikService"},
		{"serverstransports", "ServersTransport"},
		{"ingressroutetcps", "IngressRouteTCP"},
		{"ingressrouteudps", "IngressRouteUDP"},
		{"serverstransporttcps", "ServersTransportTCP"},
		{"unknown", "unknown"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.resource, func(t *testing.T) {
			if got := KindFor(tt.resource); got != tt.want {
				t.Errorf("KindFor(%q) = %v, want %v", tt.resource, got, tt.want)
			}
		})
	}
}

func TestIsTransientError(t *testing.T) {
	if isTransientError(nil) {
		t.Error("isTransientError(nil) = true, want false")
	}
	if !isTransientError(apierrors.NewTimeoutError("request timeout", 0)) {
		t.Error("isTransientError(TimeoutError) = false, want true")
	}
}

// --- ServiceOps tests (service.go) ---

func newServiceOps(kube *fakekube.Clientset, readOnly bool) *ServiceOps {
	return &ServiceOps{
		Kube:                      kube,
		Namespace:                 TestNS,
		ManagedLabelKey:           ManagedLabelKeyFull,
		ManagedLabelValue:         ManagedLabelValueController,
		ManagedAnnoKey:            ManagedAnnoKeyPangolin,
		ManagedAnnoValue:          ManagedLabelValueController,
		TraefikInstanceLabelKey:   InstanceLabelKeyFull,
		TraefikInstanceLabelValue: InstanceLabelValueMyInstance,
		ReadOnly:                  readOnly,
	}
}

func TestServiceOpsApplyReadOnly(t *testing.T) {
	t.Parallel()

	cli := fakekube.NewClientset()
	ops := newServiceOps(cli, true)

	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "new-svc"}}
	err := ops.Apply(context.Background(), svc)
	require.NoError(t, err, "ReadOnly mode should return no error")

	// Service must NOT have been created in the fake client.
	_, getErr := cli.CoreV1().Services(TestNS).Get(context.Background(), "new-svc", metav1.GetOptions{})
	require.Error(t, getErr, "service should not exist in ReadOnly mode")
}

func TestServiceOpsApplyCreate(t *testing.T) {
	t.Parallel()

	cli := fakekube.NewClientset()
	ops := newServiceOps(cli, false)

	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "new-svc"}}
	err := ops.Apply(context.Background(), svc)
	require.NoError(t, err, "Create should succeed")

	created, getErr := cli.CoreV1().Services(TestNS).Get(context.Background(), "new-svc", metav1.GetOptions{})
	require.NoError(t, getErr, "created service should be retrievable")
	require.Equal(t, ManagedLabelValueController, created.Labels[ManagedLabelKeyFull], "managed label should be set")
	require.Equal(t, InstanceLabelValueMyInstance, created.Labels[InstanceLabelKeyFull], "instance label should be set")
	require.Equal(t, ManagedLabelValueController, created.Annotations[ManagedAnnoKeyPangolin], "managed annotation should be set")
}

func TestServiceOpsApplyUpdate(t *testing.T) {
	t.Parallel()

	existing := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "existing-svc",
			Namespace: TestNS,
		},
		Spec: corev1.ServiceSpec{ClusterIP: "10.0.0.1"},
	}
	cli := fakekube.NewClientset(existing)
	ops := newServiceOps(cli, false)

	updated := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "existing-svc"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Port: 8080}},
		},
	}
	err := ops.Apply(context.Background(), updated)
	require.NoError(t, err, "Update should succeed")

	got, getErr := cli.CoreV1().Services(TestNS).Get(context.Background(), "existing-svc", metav1.GetOptions{})
	require.NoError(t, getErr)
	require.Len(t, got.Spec.Ports, 1, "ports should be updated")
	require.Equal(t, int32(8080), got.Spec.Ports[0].Port)
	require.Equal(t, ManagedLabelValueController, got.Labels[ManagedLabelKeyFull], "managed label should be set after update")
}

func TestServiceOpsEnsureManagedMeta(t *testing.T) {
	t.Parallel()

	ops := &ServiceOps{
		ManagedLabelKey:           ManagedLabelKeyFull,
		ManagedLabelValue:         ManagedLabelValueController,
		ManagedAnnoKey:            ManagedAnnoKeyPangolin,
		ManagedAnnoValue:          ManagedLabelValueController,
		TraefikInstanceLabelKey:   InstanceLabelKeyFull,
		TraefikInstanceLabelValue: InstanceLabelValueMyInstance,
	}

	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{}}
	ops.ensureManagedMeta(svc)

	require.NotNil(t, svc.Labels, "labels should be initialized")
	require.Equal(t, ManagedLabelValueController, svc.Labels[ManagedLabelKeyFull])
	require.Equal(t, InstanceLabelValueMyInstance, svc.Labels[InstanceLabelKeyFull])
	require.NotNil(t, svc.Annotations, "annotations should be initialized")
	require.Equal(t, ManagedLabelValueController, svc.Annotations[ManagedAnnoKeyPangolin])
}

func TestServiceOpsEnsureManagedMetaEmptyKeys(t *testing.T) {
	t.Parallel()

	ops := &ServiceOps{}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{}}
	ops.ensureManagedMeta(svc)

	// Empty keys must not produce map entries.
	require.Empty(t, svc.Labels, "no labels expected when keys are empty")
	require.Empty(t, svc.Annotations, "no annotations expected when keys are empty")
}

func TestServiceOpsEnsureManagedMetaPreservesExistingLabels(t *testing.T) {
	t.Parallel()

	ops := &ServiceOps{
		ManagedLabelKey:   ManagedLabelKeyFull,
		ManagedLabelValue: ManagedLabelValueController,
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{"existing": "label"},
		},
	}
	ops.ensureManagedMeta(svc)

	require.Equal(t, "label", svc.Labels["existing"], "pre-existing label must be preserved")
	require.Equal(t, ManagedLabelValueController, svc.Labels[ManagedLabelKeyFull])
}

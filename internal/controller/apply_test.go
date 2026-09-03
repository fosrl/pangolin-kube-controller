package controller

import (
	"context"
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"

	"pangolin-kube-controller/internal/config"
	"pangolin-kube-controller/internal/testutil"
	traefikconfig "pangolin-kube-controller/internal/transform/config"
)

func TestBuildDesiredSet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		objects map[string]json.RawMessage
		wantLen int
	}{
		{
			name:    "empty map",
			objects: map[string]json.RawMessage{},
			wantLen: 0,
		},
		{
			name:    "nil map",
			objects: nil,
			wantLen: 0,
		},
		{
			name: "single item",
			objects: map[string]json.RawMessage{
				"svc1": json.RawMessage("{}"),
			},
			wantLen: 1,
		},
		{
			name: "multiple items",
			objects: map[string]json.RawMessage{
				"svc1": json.RawMessage("{}"),
				"svc2": json.RawMessage(`{"foo":"bar"}`),
			},
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := buildDesiredSet(tt.objects)
			if len(got) != tt.wantLen {
				t.Errorf("buildDesiredSet() len = %d, want %d", len(got), tt.wantLen)
			}
			for name := range tt.objects {
				if _, ok := got[name]; !ok {
					t.Errorf("buildDesiredSet() missing key %q", name)
				}
			}
		})
	}
}

func TestApplyDesiredObjectsPropagatesConfiguredTraefikIdentity(t *testing.T) {
	t.Parallel()

	const (
		namespace     = "blue-ocean"
		identityKey   = "routing.example.com/traefik-instance"
		identityValue = "blue-ocean-edge"
	)
	resources := []struct {
		resource string
		kind     string
	}{
		{resource: "middlewares", kind: "MiddlewareList"},
		{resource: "ingressroutes", kind: "IngressRouteList"},
		{resource: "traefikservices", kind: "TraefikServiceList"},
		{resource: "serverstransports", kind: "ServersTransportList"},
		{resource: "ingressroutetcps", kind: "IngressRouteTCPList"},
		{resource: "ingressrouteudps", kind: "IngressRouteUDPList"},
		{resource: "serverstransporttcps", kind: "ServersTransportTCPList"},
	}
	listKinds := make(map[schema.GroupVersionResource]string, len(resources))
	for _, item := range resources {
		listKinds[schema.GroupVersionResource{
			Group: traefikconfig.Group, Version: traefikconfig.Version, Resource: item.resource,
		}] = item.kind
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	testutil.EnableSSAUpsert(dyn)
	c := NewController(&config.Config{
		Namespace:                 namespace,
		ManagedLabelKey:           "app.kubernetes.io/managed-by",
		ManagedLabelValue:         "pangolin-kube-controller",
		ManagedAnnoKey:            "pangolin.io/managed-by",
		ManagedAnnoValue:          "pangolin-kube-controller",
		TraefikInstanceLabelKey:   identityKey,
		TraefikInstanceLabelValue: identityValue,
	}, dyn, nil, nil)

	for _, item := range resources {
		t.Run(item.resource, func(t *testing.T) {
			gvr := schema.GroupVersionResource{
				Group: traefikconfig.Group, Version: traefikconfig.Version, Resource: item.resource,
			}
			name := "generated-" + item.resource
			err := c.applyDesiredObjects(context.Background(), dyn, gvr, map[string]json.RawMessage{
				name: json.RawMessage(`{}`),
			})
			if err != nil {
				t.Fatalf("applyDesiredObjects() error = %v", err)
			}
			got, err := dyn.Resource(gvr).Namespace(namespace).Get(context.Background(), name, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("get generated %s: %v", item.resource, err)
			}
			if got.GetLabels()[identityKey] != identityValue {
				t.Fatalf("instance label = %q, want %q", got.GetLabels()[identityKey], identityValue)
			}
		})
	}
}

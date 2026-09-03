package labels

import (
	"context"
	"strings"
	"testing"

	v1 "k8s.io/api/networking/v1"

	"pangolin-kube-controller/internal/config"
)

func TestValidateKV(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		value   string
		wantErr bool
	}{
		{
			name:    "valid app key and value",
			key:     "app",
			value:   "myapp",
			wantErr: false,
		},
		{
			name:    "valid kubernetes io key and value",
			key:     "app.kubernetes.io/name",
			value:   "myapp",
			wantErr: false,
		},
		{
			name:    "empty key",
			key:     "",
			value:   "myapp",
			wantErr: true,
		},
		{
			name:    "empty value",
			key:     "app",
			value:   "",
			wantErr: true,
		},
		{
			name:    "whitespace key is trimmed and fails",
			key:     "  ",
			value:   "myapp",
			wantErr: true,
		},
		{
			name:    "whitespace value is trimmed and fails",
			key:     "app",
			value:   "  ",
			wantErr: true,
		},
		{
			name:    "key with slash is valid",
			key:     "app/name",
			value:   "myapp",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateKV(tt.key, tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateKV() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPickPreferredLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		labels    map[string]string
		wantKey   string
		wantValue string
		wantErr   bool
	}{
		{
			name:      "prefers app over instance label",
			labels:    map[string]string{"app": "myapp", InstanceLabelKey: "other"},
			wantKey:   "app",
			wantValue: "myapp",
			wantErr:   false,
		},
		{
			name:      "uses app label only",
			labels:    map[string]string{"app": "myapp"},
			wantKey:   "app",
			wantValue: "myapp",
			wantErr:   false,
		},
		{
			name:      "uses instance label when no app",
			labels:    map[string]string{InstanceLabelKey: "myinstance"},
			wantKey:   InstanceLabelKey,
			wantValue: "myinstance",
			wantErr:   false,
		},
		{
			name:      "prefers trimmed app value",
			labels:    map[string]string{"app": "  myapp  ", InstanceLabelKey: "other"},
			wantKey:   "app",
			wantValue: "myapp",
			wantErr:   false,
		},
		{
			name:      "prefers trimmed instance value",
			labels:    map[string]string{InstanceLabelKey: "  myinstance  "},
			wantKey:   InstanceLabelKey,
			wantValue: "myinstance",
			wantErr:   false,
		},
		{
			name:      "app with only whitespace value falls through and returns error",
			labels:    map[string]string{"app": "   "},
			wantKey:   "",
			wantValue: "",
			wantErr:   true,
		},
		{
			name:      "app with whitespace falls through to instance label",
			labels:    map[string]string{"app": "   ", InstanceLabelKey: "myinstance"},
			wantKey:   InstanceLabelKey,
			wantValue: "myinstance",
			wantErr:   false,
		},
		{
			name:      "nil labels returns error",
			labels:    nil,
			wantKey:   "",
			wantValue: "",
			wantErr:   true,
		},
		{
			name:      "empty labels returns error",
			labels:    map[string]string{},
			wantKey:   "",
			wantValue: "",
			wantErr:   true,
		},
		{
			name:      "neither app nor instance returns error",
			labels:    map[string]string{"other": "value"},
			wantKey:   "",
			wantValue: "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ic := &v1.IngressClass{}
			ic.Labels = tt.labels
			gotKey, gotValue, err := pickPreferredLabel(ic)
			if (err != nil) != tt.wantErr {
				t.Errorf("pickPreferredLabel() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotKey != tt.wantKey {
				t.Errorf("pickPreferredLabel() key = %v, want %v", gotKey, tt.wantKey)
			}
			if gotValue != tt.wantValue {
				t.Errorf("pickPreferredLabel() value = %v, want %v", gotValue, tt.wantValue)
			}
		})
	}
}

func TestResolveInstanceLabelConfiguredValidation(t *testing.T) {
	t.Parallel()

	t.Run("valid configured labels returns nil", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			TraefikInstanceLabelKey:   "app",
			TraefikInstanceLabelValue: "myapp",
			IngressClass:              "traefik",
		}
		err := ResolveInstanceLabel(context.TODO(), nil, cfg, nil)
		if err != nil {
			t.Errorf("ResolveInstanceLabel() unexpected error: %v", err)
		}
	})

	t.Run("invalid key in configuration returns error", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			TraefikInstanceLabelKey:   "app key",
			TraefikInstanceLabelValue: "myapp",
		}
		err := ResolveInstanceLabel(context.TODO(), nil, cfg, nil)
		if err == nil {
			t.Error("ResolveInstanceLabel() expected error for invalid key")
		}
		if !strings.Contains(err.Error(), "invalid label key") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestResolveInstanceLabelExplicitIdentityBypassesAutodetection(t *testing.T) {
	t.Parallel()

	const configuredIdentity = "edge-controller-blue"
	cfg := &config.Config{
		TraefikInstanceLabelKey:   "routing.example.com/instance",
		TraefikInstanceLabelValue: configuredIdentity,
		IngressClass:              "edge-traefik",
	}
	// A nil Kubernetes client makes any attempted IngressClass autodetection
	// fail. Explicit identity must return before consulting cluster state.
	if err := ResolveInstanceLabel(context.Background(), nil, cfg, nil); err != nil {
		t.Fatalf("explicit identity unexpectedly attempted autodetection: %v", err)
	}
	if cfg.TraefikInstanceLabelValue != configuredIdentity {
		t.Fatalf("TraefikInstanceLabelValue = %q, want %q", cfg.TraefikInstanceLabelValue, configuredIdentity)
	}
}

func TestMonitorEmptyConfig(t *testing.T) {
	t.Parallel()

	t.Run("returns nil when ingress class is empty", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{}
		err := Monitor(context.TODO(), nil, cfg, nil)
		if err != nil {
			t.Errorf("Monitor() error = %v, want nil", err)
		}
	})

	t.Run("returns nil when label key is empty", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			IngressClass: "traefik",
		}
		err := Monitor(context.TODO(), nil, cfg, nil)
		if err != nil {
			t.Errorf("Monitor() error = %v, want nil", err)
		}
	})
}

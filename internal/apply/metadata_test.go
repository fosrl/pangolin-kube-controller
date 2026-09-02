package apply

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"pangolin-kube-controller/internal/transform/routing"
)

const (
	managedLabelKey   = "app.kubernetes.io/managed"
	managedAnnoKey    = "pangolin.io/managed"
	resourceName      = "my-resource"
	testNamespace     = "ns"
	instanceLabelKey  = "app.kubernetes.io/instance"
	instanceLabelVal  = "traefik"
	managedLabelValue = "pangolin"
	managedAnnoValue  = "true"
)

func defaultMetadataConfig() MetadataConfig {
	return MetadataConfig{
		ManagedLabelKey:           managedLabelKey,
		ManagedLabelValue:         managedLabelValue,
		TraefikInstanceLabelKey:   instanceLabelKey,
		TraefikInstanceLabelValue: instanceLabelVal,
		ManagedAnnoKey:            managedAnnoKey,
		ManagedAnnoValue:          managedAnnoValue,
		IngressClass:              "traefik",
	}
}

func TestGetMetadataMap(t *testing.T) {
	obj := &unstructured.Unstructured{}
	obj.SetLabels(map[string]string{"key": "value"})
	obj.SetAnnotations(map[string]string{"anno": "val"})

	t.Run("with labels getter", func(t *testing.T) {
		got := GetMetadataMap(obj, (*unstructured.Unstructured).GetLabels)
		if got["key"] != "value" {
			t.Errorf("GetMetadataMap() = %v, want key=value", got)
		}
	})

	t.Run("with annotations getter", func(t *testing.T) {
		got := GetMetadataMap(obj, (*unstructured.Unstructured).GetAnnotations)
		if got["anno"] != "val" {
			t.Errorf("GetMetadataMap() = %v, want anno=val", got)
		}
	})

	t.Run("nil object", func(t *testing.T) {
		got := GetMetadataMap(nil, (*unstructured.Unstructured).GetLabels)
		if got != nil {
			t.Errorf("GetMetadataMap(nil) = %v, want nil", got)
		}
	})
}

func TestUpdateMetadataField(t *testing.T) {
	t.Run("existing nil adds new field", func(t *testing.T) {
		target := map[string]interface{}{}
		got := UpdateMetadataField(nil, "key", "value", target)
		if !got {
			t.Error("UpdateMetadataField() = false, want true for new field")
		}
		if target["key"] != "value" {
			t.Errorf("target[key] = %v, want value", target["key"])
		}
	})

	t.Run("existing value same", func(t *testing.T) {
		target := map[string]interface{}{}
		got := UpdateMetadataField(map[string]string{"key": "value"}, "key", "value", target)
		if got {
			t.Error("UpdateMetadataField() = true, want false for unchanged value")
		}
		if target["key"] != "value" {
			t.Errorf("target[key] = %v, want value retained in SSA payload", target["key"])
		}
	})

	t.Run("existing value different", func(t *testing.T) {
		target := map[string]interface{}{}
		got := UpdateMetadataField(map[string]string{"key": "old"}, "key", "new", target)
		if !got {
			t.Error("UpdateMetadataField() = false, want true for changed value")
		}
		if target["key"] != "new" {
			t.Errorf("target[key] = %v, want new", target["key"])
		}
	})

	t.Run("key missing in existing", func(t *testing.T) {
		target := map[string]interface{}{}
		got := UpdateMetadataField(map[string]string{"other": "val"}, "key", "value", target)
		if !got {
			t.Error("UpdateMetadataField() = false, want true for new key")
		}
		if target["key"] != "value" {
			t.Errorf("target[key] = %v, want value", target["key"])
		}
	})
}

func TestBuildMetadataForApplyCreateMode(t *testing.T) {
	cfg := defaultMetadataConfig()
	meta, force := BuildMetadataForApply(nil, resourceName, testNamespace, "Middleware", cfg)
	if force {
		t.Error("force = true, want false on create")
	}
	if meta["name"] != resourceName {
		t.Errorf("meta[name] = %v, want %s", meta["name"], resourceName)
	}
	if meta["namespace"] != testNamespace {
		t.Errorf("meta[namespace] = %v, want %s", meta["namespace"], testNamespace)
	}
	labels := meta["labels"].(map[string]interface{})
	if labels[managedLabelKey] != managedLabelValue {
		t.Errorf("labels[managed] = %v, want %s", labels[managedLabelKey], managedLabelValue)
	}
	annotations := meta["annotations"].(map[string]interface{})
	if annotations[managedAnnoKey] != managedAnnoValue {
		t.Errorf("annotations[managed] = %v, want %s", annotations[managedAnnoKey], managedAnnoValue)
	}
}

func TestBuildMetadataForApplyIngressRouteAddsIngressClass(t *testing.T) {
	cfg := defaultMetadataConfig()
	meta, _ := BuildMetadataForApply(nil, "my-route", testNamespace, "IngressRoute", cfg)
	annotations := meta["annotations"].(map[string]interface{})
	if annotations[routing.IngressClassAnnotation] != cfg.IngressClass {
		t.Errorf("annotations[router-class] = %v, want %s", annotations[routing.IngressClassAnnotation], cfg.IngressClass)
	}
}

func TestBuildMetadataForApplyUpdateSameValuesNoForce(t *testing.T) {
	cfg := defaultMetadataConfig()
	existing := &unstructured.Unstructured{}
	existing.SetLabels(map[string]string{
		managedLabelKey:  managedLabelValue,
		instanceLabelKey: instanceLabelVal,
	})
	existing.SetAnnotations(map[string]string{
		managedAnnoKey: managedAnnoValue,
	})

	meta, force := BuildMetadataForApply(existing, resourceName, testNamespace, "Middleware", cfg)
	if force {
		t.Error("force = true, want false when existing matches")
	}
	labels := meta["labels"].(map[string]interface{})
	if labels[managedLabelKey] != managedLabelValue || labels[instanceLabelKey] != instanceLabelVal {
		t.Errorf("unchanged controller-owned labels missing from SSA payload: %v", labels)
	}
	annotations := meta["annotations"].(map[string]interface{})
	if annotations[managedAnnoKey] != managedAnnoValue {
		t.Errorf("unchanged controller-owned annotation missing from SSA payload: %v", annotations)
	}
}

func TestBuildMetadataForApplyUpdateDifferentValuesForces(t *testing.T) {
	cfg := defaultMetadataConfig()
	existing := &unstructured.Unstructured{}
	existing.SetLabels(map[string]string{
		managedLabelKey: "other",
	})
	existing.SetAnnotations(map[string]string{
		managedAnnoKey: "false",
	})

	meta, force := BuildMetadataForApply(existing, resourceName, testNamespace, "Middleware", cfg)
	if !force {
		t.Error("force = false, want true when values differ")
	}
	labels := meta["labels"].(map[string]interface{})
	if labels[managedLabelKey] != managedLabelValue {
		t.Errorf("labels[managed] = %v, want %s", labels[managedLabelKey], managedLabelValue)
	}
}

func TestBuildMetadataForApplyPreservesUnrelatedExistingLabels(t *testing.T) {
	cfg := defaultMetadataConfig()
	existing := &unstructured.Unstructured{}
	existing.SetLabels(map[string]string{
		"user.example.com/owner": "platform",
		managedLabelKey:          managedLabelValue,
	})

	meta, _ := BuildMetadataForApply(existing, resourceName, testNamespace, "Middleware", cfg)
	labels := meta["labels"].(map[string]interface{})
	if _, overwritten := labels["user.example.com/owner"]; overwritten {
		t.Fatal("unrelated existing label was included in the controller patch")
	}
	if labels[instanceLabelKey] != instanceLabelVal {
		t.Fatalf("instance label = %v, want %s", labels[instanceLabelKey], instanceLabelVal)
	}
}

func TestBuildMetadataForApplyWithoutConfiguredIdentity(t *testing.T) {
	cfg := defaultMetadataConfig()
	cfg.TraefikInstanceLabelKey = ""
	cfg.TraefikInstanceLabelValue = ""

	meta, _ := BuildMetadataForApply(nil, resourceName, testNamespace, "Middleware", cfg)
	labels := meta["labels"].(map[string]interface{})
	if _, invented := labels[instanceLabelKey]; invented {
		t.Fatalf("unexpected invented instance label: %v", labels)
	}
}

func TestBuildMetadataForApplyIngressRouteChangedIngressClassForces(t *testing.T) {
	cfg := defaultMetadataConfig()
	existing := &unstructured.Unstructured{}
	existing.SetAnnotations(map[string]string{
		routing.IngressClassAnnotation: "other",
	})

	meta, force := BuildMetadataForApply(existing, "my-route", testNamespace, "IngressRoute", cfg)
	if !force {
		t.Error("force = false, want true when ingress class differs")
	}
	annotations := meta["annotations"].(map[string]interface{})
	if annotations[routing.IngressClassAnnotation] != cfg.IngressClass {
		t.Errorf("annotations[router-class] = %v, want %s", annotations[routing.IngressClassAnnotation], cfg.IngressClass)
	}
}

func TestForceNeededForMetadata(t *testing.T) {
	t.Run("nil existing", func(t *testing.T) {
		if ForceNeededForMetadata(nil, map[string]interface{}{"metadata": map[string]interface{}{}}) {
			t.Error("ForceNeededForMetadata(nil) = true, want false")
		}
	})

	t.Run("no metadata in desired", func(t *testing.T) {
		existing := &unstructured.Unstructured{}
		existing.SetLabels(map[string]string{"key": "value"})
		if ForceNeededForMetadata(existing, map[string]interface{}{}) {
			t.Error("ForceNeededForMetadata() = true, want false")
		}
	})

	t.Run("new label key requires force", func(t *testing.T) {
		existing := &unstructured.Unstructured{}
		existing.SetLabels(map[string]string{})
		desired := map[string]interface{}{
			"metadata": map[string]interface{}{
				"labels": map[string]interface{}{"new-key": "value"},
			},
		}
		if !ForceNeededForMetadata(existing, desired) {
			t.Error("ForceNeededForMetadata() = false, want true for new label key")
		}
	})

	t.Run("changed label value requires force", func(t *testing.T) {
		existing := &unstructured.Unstructured{}
		existing.SetLabels(map[string]string{"key": "old"})
		desired := map[string]interface{}{
			"metadata": map[string]interface{}{
				"labels": map[string]interface{}{"key": "new"},
			},
		}
		if !ForceNeededForMetadata(existing, desired) {
			t.Error("ForceNeededForMetadata() = false, want true for changed label value")
		}
	})

	t.Run("same label value no force", func(t *testing.T) {
		existing := &unstructured.Unstructured{}
		existing.SetLabels(map[string]string{"key": "same"})
		desired := map[string]interface{}{
			"metadata": map[string]interface{}{
				"labels": map[string]interface{}{"key": "same"},
			},
		}
		if ForceNeededForMetadata(existing, desired) {
			t.Error("ForceNeededForMetadata() = true, want false for unchanged value")
		}
	})
}

func TestNeedsForceOnMapChange(t *testing.T) {
	t.Run("nil existing is not force", func(t *testing.T) {
		if NeedsForceOnMapChange(nil, map[string]interface{}{"key": "value"}) {
			t.Error("NeedsForceOnMapChange(nil) = true, want false")
		}
	})

	t.Run("new key requires force", func(t *testing.T) {
		existing := map[string]string{}
		desired := map[string]interface{}{"new": "value"}
		if !NeedsForceOnMapChange(existing, desired) {
			t.Error("NeedsForceOnMapChange() = false, want true for new key")
		}
	})

	t.Run("changed value requires force", func(t *testing.T) {
		existing := map[string]string{"key": "old"}
		desired := map[string]interface{}{"key": "new"}
		if !NeedsForceOnMapChange(existing, desired) {
			t.Error("NeedsForceOnMapChange() = false, want true for changed value")
		}
	})

	t.Run("same value no force", func(t *testing.T) {
		existing := map[string]string{"key": "same"}
		desired := map[string]interface{}{"key": "same"}
		if NeedsForceOnMapChange(existing, desired) {
			t.Error("NeedsForceOnMapChange() = true, want false for unchanged value")
		}
	})
}

package apply

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"pangolin-kube-controller/internal/transform/routing"
)

func GetMetadataMap(existing *unstructured.Unstructured, getter func(*unstructured.Unstructured) map[string]string) map[string]string {
	if existing != nil {
		return getter(existing)
	}
	return nil
}

func UpdateMetadataField(existing map[string]string, key, desiredValue string, target map[string]interface{}) bool {
	// Server-Side Apply requires every controller-owned desired field to be
	// present on every apply. Omitting an unchanged field relinquishes it and
	// can prune it from the live object. Only unrelated fields remain absent
	// from this patch, so their ownership and values are preserved.
	target[key] = desiredValue
	if existing == nil {
		return true
	}
	val, ok := existing[key]
	if !ok || val != desiredValue {
		return true
	}
	return false
}

type MetadataConfig struct {
	ManagedLabelKey           string
	ManagedLabelValue         string
	TraefikInstanceLabelKey   string
	TraefikInstanceLabelValue string
	ManagedAnnoKey            string
	ManagedAnnoValue          string
	IngressClass              string
}

func BuildMetadataForApply(existing *unstructured.Unstructured, name, ns, kind string, cfg MetadataConfig) (map[string]interface{}, bool) {
	meta := map[string]interface{}{"name": name, "namespace": ns}
	labels := map[string]interface{}{}
	annotations := map[string]interface{}{}
	needForce := false
	isUpdate := existing != nil

	existingLabels := GetMetadataMap(existing, (*unstructured.Unstructured).GetLabels)
	existingAnnotations := GetMetadataMap(existing, (*unstructured.Unstructured).GetAnnotations)

	if UpdateMetadataField(
		existingLabels,
		cfg.ManagedLabelKey,
		cfg.ManagedLabelValue,
		labels,
	) && isUpdate {
		needForce = true
	}

	if cfg.TraefikInstanceLabelKey != "" && cfg.TraefikInstanceLabelValue != "" {
		if UpdateMetadataField(
			existingLabels,
			cfg.TraefikInstanceLabelKey,
			cfg.TraefikInstanceLabelValue,
			labels,
		) && isUpdate {
			needForce = true
		}
	}

	if UpdateMetadataField(
		existingAnnotations,
		cfg.ManagedAnnoKey,
		cfg.ManagedAnnoValue,
		annotations,
	) && isUpdate {
		needForce = true
	}

	if kind == "IngressRoute" {
		if UpdateMetadataField(
			existingAnnotations,
			routing.IngressClassAnnotation,
			cfg.IngressClass,
			annotations,
		) && isUpdate {
			needForce = true
		}
	}

	if len(labels) > 0 {
		meta["labels"] = labels
	}
	if len(annotations) > 0 {
		meta["annotations"] = annotations
	}

	return meta, needForce
}

func ForceNeededForMetadata(existing *unstructured.Unstructured, desired map[string]interface{}) bool {
	if existing == nil {
		return false
	}
	meta, _ := desired["metadata"].(map[string]interface{})
	if meta == nil {
		return false
	}
	if lblsIfc, ok := meta["labels"].(map[string]interface{}); ok {
		if NeedsForceOnMapChange(existing.GetLabels(), lblsIfc) {
			return true
		}
	}
	if annIfc, ok := meta["annotations"].(map[string]interface{}); ok {
		if NeedsForceOnMapChange(existing.GetAnnotations(), annIfc) {
			return true
		}
	}
	return false
}

func NeedsForceOnMapChange(existing map[string]string, desired map[string]interface{}) bool {
	if existing == nil {
		return false
	}
	for k, v := range desired {
		desiredVal, _ := v.(string)
		if cur, ok := existing[k]; ok {
			if cur != desiredVal {
				return true
			}
		} else {
			return true
		}
	}
	return false
}

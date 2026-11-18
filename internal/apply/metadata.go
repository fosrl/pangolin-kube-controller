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

// UpdateMetadataField always writes key=desiredValue into target and reports
// whether that differs from the existing object. The key must be present even
// when unchanged: under server-side apply, leaving a field out of the applied
// object tells the API server to remove it.
func UpdateMetadataField(existing map[string]string, key, desiredValue string, target map[string]interface{}) bool {
	target[key] = desiredValue
	if existing == nil {
		return true
	}
	val, ok := existing[key]
	return !ok || val != desiredValue
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

	if IsRouteKind(kind) && cfg.IngressClass != "" {
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

// IsRouteKind reports whether kind is one of the Traefik route kinds the
// ingress class annotation applies to.
func IsRouteKind(kind string) bool {
	return kind == "IngressRoute" || kind == "IngressRouteTCP" || kind == "IngressRouteUDP"
}

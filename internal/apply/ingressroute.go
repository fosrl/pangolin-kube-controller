package apply

import (
	"context"
	"encoding/json"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	logrus "github.com/sirupsen/logrus"

	"pangolin-kube-controller/internal/kube/resources"
	obslog "pangolin-kube-controller/internal/observability/logging"
	"pangolin-kube-controller/internal/transform/routing"
)

const FieldManagerName = "pangolin-kube-controller"

type IngressRouteOps struct {
	ResIfc                    resources.ResourceClient
	Namespace                 string
	ManagedLabelKey           string
	ManagedLabelValue         string
	TraefikInstanceLabelKey   string
	TraefikInstanceLabelValue string
	ManagedAnnoKey            string
	ManagedAnnoValue          string
	IngressClass              string
	ReadOnly                  bool
}

func (o *IngressRouteOps) Apply(ctx context.Context, name string, u map[string]interface{}) error {
	if o.ReadOnly {
		return nil
	}
	metaCfg := MetadataConfig{
		ManagedLabelKey:           o.ManagedLabelKey,
		ManagedLabelValue:         o.ManagedLabelValue,
		TraefikInstanceLabelKey:   o.TraefikInstanceLabelKey,
		TraefikInstanceLabelValue: o.TraefikInstanceLabelValue,
		ManagedAnnoKey:            o.ManagedAnnoKey,
		ManagedAnnoValue:          o.ManagedAnnoValue,
		IngressClass:              o.IngressClass,
	}
	existing, err := o.ResIfc.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			meta, _ := BuildMetadataForApply(nil, name, o.Namespace, "IngressRoute", metaCfg)
			routing.AnnotateRouterEntryPointsIfPresent(u, meta)
			u["metadata"] = meta
			return CreateUnstructured(ctx, o.ResIfc, u, "IngressRoute")
		}
		return err
	}
	meta, force := BuildMetadataForApply(existing, name, o.Namespace, "IngressRoute", metaCfg)
	routing.AnnotateRouterEntryPointsIfPresent(u, meta)
	u["metadata"] = meta
	return PatchUnstructured(ctx, o.ResIfc, name, u, existing, "IngressRoute", PatchConfig{Force: force, MetadataConfig: metaCfg})
}

func (o *IngressRouteOps) ApplySingle(ctx context.Context, m map[string]interface{}, kind string) error {
	metaIn, _ := m["metadata"].(map[string]interface{})
	name, _ := metaIn["name"].(string)
	if name == "" {
		logApplySingleMalformedInput(kind, m, metaIn)
		return nil
	}
	if o.ReadOnly {
		return nil
	}
	metaCfg := MetadataConfig{
		ManagedLabelKey:           o.ManagedLabelKey,
		ManagedLabelValue:         o.ManagedLabelValue,
		TraefikInstanceLabelKey:   o.TraefikInstanceLabelKey,
		TraefikInstanceLabelValue: o.TraefikInstanceLabelValue,
		ManagedAnnoKey:            o.ManagedAnnoKey,
		ManagedAnnoValue:          o.ManagedAnnoValue,
		IngressClass:              o.IngressClass,
	}
	existing, err := o.ResIfc.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			meta, _ := BuildMetadataForApply(nil, name, o.Namespace, kind, metaCfg)
			m["metadata"] = meta
			return CreateUnstructured(ctx, o.ResIfc, m, kind)
		}
		return err
	}
	meta, force := BuildMetadataForApply(existing, name, o.Namespace, kind, metaCfg)
	m["metadata"] = meta
	return PatchUnstructured(ctx, o.ResIfc, name, m, existing, kind, PatchConfig{Force: force, MetadataConfig: metaCfg})
}

func logApplySingleMalformedInput(kind string, input map[string]interface{}, metaIn map[string]interface{}) {
	fields := logrus.Fields{"kind": kind}
	var message string
	if metaIn == nil {
		// missing metadata case
		fields["namespace"] = ""
		if redacted, ok := redactMapForLog(input); ok {
			fields["metadata"] = redacted
		}
		message = "ApplySingle: missing metadata"
	} else {
		namespace, _ := metaIn["namespace"].(string)
		fields["namespace"] = namespace
		if redacted, ok := redactMapForLog(metaIn); ok {
			fields["metadata"] = redacted
		}
		message = "ApplySingle: missing name in metadata"
	}
	logrus.WithFields(fields).Warn(message)
}

func redactMapForLog(m map[string]interface{}) (string, bool) {
	if m == nil {
		return "", false
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", false
	}
	redacted, err := obslog.RedactJSONLike(b)
	if err != nil {
		return "", false
	}
	return string(redacted), true
}

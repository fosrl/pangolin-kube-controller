package apply

import (
	"context"
	"encoding/json"
	"time"

	logrus "github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"pangolin-kube-controller/internal/kube/resources"
	traefikconfig "pangolin-kube-controller/internal/transform/config"
)

const FieldManager = "pangolin-kube-controller"

func KindFor(resource string) string {
	switch resource {
	case "ingressroutes":
		return "IngressRoute"
	case "middlewares":
		return "Middleware"
	case "traefikservices":
		return "TraefikService"
	case "serverstransports":
		return "ServersTransport"
	case "ingressroutetcps":
		return "IngressRouteTCP"
	case "ingressrouteudps":
		return "IngressRouteUDP"
	case "serverstransporttcps":
		return "ServersTransportTCP"
	default:
		return resource
	}
}

func IgnoreFieldValidation(kind string) bool {
	return kind == "TraefikService"
}

func BoolPtr(v bool) *bool {
	return &v
}

type UnstructuredOps struct {
	Dyn       dynamic.Interface
	GVR       schema.GroupVersionResource
	Namespace string
	SSAForce  bool
}

type PatchConfig struct {
	Force          bool
	SSAForce       bool
	MetadataConfig MetadataConfig
}

type SSAOptions struct {
	Force         bool
	ForceOverride bool
	SSAForce      bool
}

func (o *UnstructuredOps) Apply(ctx context.Context, name string, raw json.RawMessage, metaCfg MetadataConfig) error {
	obj := map[string]interface{}{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return err
	}

	kind := KindFor(o.GVR.Resource)
	resIfc := resources.AdaptResource(o.Dyn.Resource(o.GVR).Namespace(o.Namespace))

	meta, _ := BuildMetadataForApply(nil, name, o.Namespace, kind, metaCfg)
	u := map[string]interface{}{
		"apiVersion": traefikconfig.GroupVersion,
		"kind":       kind,
		"metadata":   meta,
		"spec":       obj,
	}

	existing, err := resIfc.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return CreateUnstructured(ctx, resIfc, u, kind)
		}
		return err
	}

	var force bool
	meta, force = BuildMetadataForApply(existing, name, o.Namespace, kind, metaCfg)
	u["metadata"] = meta
	return PatchUnstructured(ctx, resIfc, name, u, existing, kind, PatchConfig{
		Force:          force,
		SSAForce:       o.SSAForce,
		MetadataConfig: metaCfg,
	})
}

func CreateUnstructured(ctx context.Context, resIfc resources.ResourceClient, u map[string]interface{}, kind string) error {
	patchBytes, err := json.Marshal(u)
	if err != nil {
		return err
	}
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(patchBytes); err != nil {
		return err
	}
	return ApplySSAWithRetry(ctx, resIfc, obj.GetName(), patchBytes, kind, SSAOptions{})
}

func PatchUnstructured(ctx context.Context, resIfc resources.ResourceClient, name string, u map[string]interface{}, existing *unstructured.Unstructured, kind string, cfg PatchConfig) error {
	if existing != nil {
		if changed := DiffSpecKeys(existing, u); len(changed) > 0 {
			logrus.Infof("Semantic changes for %s %s: %v", kind, name, changed)
		}
	}
	forceOverride := ForceNeededForMetadata(existing, u)
	legacyAdoption := ShouldAdoptLegacySpec(existing, cfg.MetadataConfig)
	patchBytes, err := json.Marshal(u)
	if err != nil {
		return err
	}
	return ApplySSAWithRetry(ctx, resIfc, name, patchBytes, kind, SSAOptions{Force: cfg.Force || legacyAdoption, ForceOverride: forceOverride, SSAForce: cfg.SSAForce})
}

func ShouldAdoptLegacySpec(existing *unstructured.Unstructured, cfg MetadataConfig) bool {
	if existing == nil || !isControllerManaged(existing, cfg) {
		return false
	}

	legacyOwnsSpec := false
	for _, entry := range existing.GetManagedFields() {
		if !managedFieldsEntryOwnsSpec(entry) {
			continue
		}
		switch entry.Manager {
		case FieldManager:
			continue
		case "controller":
			if entry.Operation != metav1.ManagedFieldsOperationUpdate {
				return false
			}
			legacyOwnsSpec = true
		default:
			return false
		}
	}
	return legacyOwnsSpec
}

func isControllerManaged(existing *unstructured.Unstructured, cfg MetadataConfig) bool {
	labels := existing.GetLabels()
	annotations := existing.GetAnnotations()
	return (cfg.ManagedLabelKey != "" && labels[cfg.ManagedLabelKey] == cfg.ManagedLabelValue) ||
		(cfg.ManagedAnnoKey != "" && annotations[cfg.ManagedAnnoKey] == cfg.ManagedAnnoValue)
}

func managedFieldsEntryOwnsSpec(entry metav1.ManagedFieldsEntry) bool {
	if entry.FieldsV1 == nil || len(entry.FieldsV1.Raw) == 0 {
		return false
	}
	fields := map[string]interface{}{}
	if err := json.Unmarshal(entry.FieldsV1.Raw, &fields); err != nil {
		return false
	}
	_, ownsSpec := fields["f:spec"]
	return ownsSpec
}

func DiffSpecKeys(existing *unstructured.Unstructured, desired map[string]interface{}) []string {
	oldObj := existing.UnstructuredContent()
	oldSpec, _ := oldObj["spec"].(map[string]interface{})
	newSpec, _ := desired["spec"].(map[string]interface{})
	return DiffKeys(oldSpec, newSpec)
}

func ApplySSAWithRetry(ctx context.Context, resIfc resources.ResourceClient, name string, patchBytes []byte, kind string, opts SSAOptions) error {
	useForce := opts.SSAForce || opts.Force || opts.ForceOverride
	patchOpts := metav1.PatchOptions{
		FieldManager: FieldManager,
	}
	if useForce {
		patchOpts.Force = BoolPtr(true)
	}
	if IgnoreFieldValidation(kind) {
		patchOpts.FieldValidation = metav1.FieldValidationIgnore
	}

	backoff := 50 * time.Millisecond
	var lastErr error
	for i := 0; i < 5; i++ {
		_, lastErr = resIfc.Patch(ctx, name, types.ApplyPatchType, patchBytes, patchOpts)
		if lastErr == nil {
			logrus.Infof("Applied %s %s via SSA", kind, name)
			return nil
		}
		if (errors.IsConflict(lastErr) && useForce) || isTransientError(lastErr) {
			t := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				t.Stop()
				return lastErr
			case <-t.C:
				backoff *= 2
				continue
			}
		}
		return lastErr
	}
	return lastErr
}

func isTransientError(err error) bool {
	return errors.IsTimeout(err) || errors.IsServerTimeout(err) || errors.IsTooManyRequests(err) || errors.IsServiceUnavailable(err)
}

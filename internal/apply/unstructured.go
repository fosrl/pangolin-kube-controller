package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	logrus "github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/csaupgrade"

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
	case "tlsoptions":
		return "TLSOption"
	default:
		return resource
	}
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
	Force    bool
	SSAForce bool
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
	return PatchUnstructured(ctx, resIfc, name, u, existing, kind, PatchConfig{Force: true, SSAForce: o.SSAForce || force})
}

func CreateUnstructured(ctx context.Context, resIfc resources.ResourceClient, u map[string]interface{}, kind string) error {
	// Objects are created with server-side apply, never with a plain Create:
	// a Create records an Update-operation field manager, and fields owned by
	// it survive later applies even after Pangolin stops emitting them.
	bytes, err := json.Marshal(u)
	if err != nil {
		return err
	}
	meta, _ := u["metadata"].(map[string]interface{})
	name, _ := meta["name"].(string)
	if err := ApplySSAWithRetry(ctx, resIfc, name, bytes, kind, SSAOptions{Force: true}); err != nil {
		return err
	}
	logrus.Infof("Created %s %s", kind, name)
	return nil
}

// LegacyFieldManagers are the managers 0.1.0-alpha.1 recorded with its
// Create/Update calls: it set no field manager, so the API server used the
// binary name.
var LegacyFieldManagers = sets.New("controller", FieldManager)

// UpgradeLegacyOwnership hands the fields owned by an alpha.1 Create/Update
// manager over to the server-side apply manager, so a later apply can remove
// them. It is a no-op for objects that never had such a manager.
func UpgradeLegacyOwnership(ctx context.Context, resIfc resources.ResourceClient, existing *unstructured.Unstructured) error {
	if existing == nil {
		return nil
	}
	patch, err := csaupgrade.UpgradeManagedFieldsPatch(existing, LegacyFieldManagers, FieldManager)
	if err != nil || len(patch) == 0 {
		return err
	}
	if _, err := resIfc.Patch(ctx, existing.GetName(), types.JSONPatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("upgrade field ownership of %s %s: %w", existing.GetKind(), existing.GetName(), err)
	}
	logrus.Infof("Migrated field ownership of %s %s to server-side apply", existing.GetKind(), existing.GetName())
	return nil
}

func PatchUnstructured(ctx context.Context, resIfc resources.ResourceClient, name string, u map[string]interface{}, existing *unstructured.Unstructured, kind string, cfg PatchConfig) error {
	if existing != nil {
		if changed := DiffSpecKeys(existing, u); len(changed) > 0 {
			logrus.Infof("Semantic changes for %s %s: %v", kind, name, changed)
		}
	}
	if err := UpgradeLegacyOwnership(ctx, resIfc, existing); err != nil {
		return err
	}
	forceOverride := ForceNeededForMetadata(existing, u)
	patchBytes, err := json.Marshal(u)
	if err != nil {
		return err
	}
	return ApplySSAWithRetry(ctx, resIfc, name, patchBytes, kind, SSAOptions{Force: cfg.Force, ForceOverride: forceOverride, SSAForce: cfg.SSAForce})
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

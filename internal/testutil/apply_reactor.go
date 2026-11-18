package testutil

import (
	"encoding/json"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// InstallApplyReactor makes a fake dynamic client handle server-side apply
// the way the API server does for a single field manager: the object is
// created when missing, and otherwise its spec is replaced by the applied one
// while labels and annotations are merged. The stock fake tracker only
// strategic-merges into existing objects, which neither creates nor removes
// fields.
func InstallApplyReactor(dyn *fake.FakeDynamicClient) {
	tracker := dyn.Tracker()
	dyn.PrependReactor("patch", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pa, ok := action.(k8stesting.PatchAction)
		if !ok || pa.GetPatchType() != types.ApplyPatchType {
			return false, nil, nil
		}
		desired := &unstructured.Unstructured{}
		if err := json.Unmarshal(pa.GetPatch(), &desired.Object); err != nil {
			return true, nil, err
		}
		desired.SetName(pa.GetName())
		desired.SetNamespace(pa.GetNamespace())
		gvr := pa.GetResource()

		cur, err := tracker.Get(gvr, pa.GetNamespace(), pa.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if err := tracker.Create(gvr, desired, pa.GetNamespace()); err != nil {
				return true, nil, err
			}
			return true, desired, nil
		}
		if err != nil {
			return true, nil, err
		}
		existing, ok := cur.(*unstructured.Unstructured)
		if !ok {
			return false, nil, nil
		}
		updated := existing.DeepCopy()
		if spec, found := desired.Object["spec"]; found {
			updated.Object["spec"] = spec
		} else {
			delete(updated.Object, "spec")
		}
		updated.SetLabels(mergeStrings(existing.GetLabels(), desired.GetLabels()))
		updated.SetAnnotations(mergeStrings(existing.GetAnnotations(), desired.GetAnnotations()))
		if err := tracker.Update(gvr, updated, pa.GetNamespace()); err != nil {
			return true, nil, err
		}
		return true, updated, nil
	})
}

func mergeStrings(a, b map[string]string) map[string]string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

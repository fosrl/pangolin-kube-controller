package testutil

import (
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// EnableSSAUpsert teaches client-go's fake dynamic client the API server's
// create-or-update semantics for server-side apply patches.
func EnableSSAUpsert(client *fake.FakeDynamicClient) {
	client.PrependReactor("patch", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patchAction, ok := action.(k8stesting.PatchAction)
		if !ok || patchAction.GetPatchType() != types.ApplyPatchType {
			return false, nil, nil
		}
		obj := &unstructured.Unstructured{}
		if err := obj.UnmarshalJSON(patchAction.GetPatch()); err != nil {
			return true, nil, err
		}
		obj.SetNamespace(action.GetNamespace())
		gvr := action.GetResource()
		_, err := client.Tracker().Get(gvr, action.GetNamespace(), obj.GetName())
		switch {
		case errors.IsNotFound(err):
			err = client.Tracker().Create(gvr, obj, action.GetNamespace())
		case err == nil:
			err = client.Tracker().Update(gvr, obj, action.GetNamespace())
		}
		return true, obj, err
	})
}

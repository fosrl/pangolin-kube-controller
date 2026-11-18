package testschema

import (
	"fmt"
	"strings"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/pruning"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// MapCRDByKind returns a map from Kind to CRD (first served version).
func MapCRDByKind(crds []*apiextensionsv1.CustomResourceDefinition) map[string]*apiextensionsv1.CustomResourceDefinition {
	m := make(map[string]*apiextensionsv1.CustomResourceDefinition, len(crds))
	for _, c := range crds {
		m[c.Spec.Names.Kind] = c
	}
	return m
}

// ValidatorFor builds a validation function for a CRD/version using structural schema.
func ValidatorFor(crd *apiextensionsv1.CustomResourceDefinition, version string) (func(obj *unstructured.Unstructured) []error, error) {
	vs, effectiveVersion, err := resolveSchema(crd, version)
	if err != nil {
		return nil, err
	}
	group := crd.Spec.Group
	kind := crd.Spec.Names.Kind
	internal := &apiextensions.JSONSchemaProps{}
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(vs.OpenAPIV3Schema, internal, nil); err != nil {
		return nil, fmt.Errorf("convert schema of %s: %w", crd.Name, err)
	}
	structural, err := structuralschema.NewStructural(internal)
	if err != nil {
		return nil, fmt.Errorf("structural schema of %s: %w", crd.Name, err)
	}
	validator, _, err := apiservervalidation.NewSchemaValidator(internal)
	if err != nil {
		return nil, fmt.Errorf("schema validator of %s: %w", crd.Name, err)
	}
	return func(obj *unstructured.Unstructured) []error {
		errs := lightValidate(obj, group, kind, effectiveVersion)
		if obj == nil {
			return errs
		}
		// Fields the schema does not know are pruned silently by the API
		// server, which is how an invalid spec used to look applied.
		pruned := obj.DeepCopy().Object
		for _, path := range pruning.PruneWithOptions(pruned, structural, true, structuralschema.UnknownFieldPathOptions{TrackUnknownFieldPaths: true}) {
			errs = append(errs, fmt.Errorf("%s %s: unknown field %q", kind, obj.GetName(), path))
		}
		for _, fe := range apiservervalidation.ValidateCustomResource(nil, obj.Object, validator) {
			errs = append(errs, fmt.Errorf("%s %s: %s", kind, obj.GetName(), fe.Error()))
		}
		return errs
	}, nil
}

// resolveSchema picks the requested served version schema or falls back to the first available.
func resolveSchema(crd *apiextensionsv1.CustomResourceDefinition, version string) (*apiextensionsv1.CustomResourceValidation, string, error) {
	if crd == nil {
		return nil, "", fmt.Errorf("nil CRD")
	}
	for _, v := range crd.Spec.Versions {
		if v.Name == version && v.Served && v.Schema != nil && v.Schema.OpenAPIV3Schema != nil {
			return v.Schema, v.Name, nil
		}
	}
	for _, v := range crd.Spec.Versions { // fallback
		if v.Schema != nil && v.Schema.OpenAPIV3Schema != nil {
			return v.Schema, v.Name, nil
		}
	}
	return nil, version, fmt.Errorf("no schema found for CRD %s", crd.Name)
}

// lightValidate performs simplified structural checks used in offline tests.
func lightValidate(obj *unstructured.Unstructured, group, kind, version string) []error {
	if obj == nil {
		return []error{fmt.Errorf("nil object")}
	}
	var errs []error
	if obj.GetKind() != kind {
		errs = append(errs, fmt.Errorf("kind mismatch: got %s want %s", obj.GetKind(), kind))
	}
	apiv := obj.GetAPIVersion()
	errs = append(errs, validateAPIVersion(apiv, group, version)...)
	if obj.GetName() == "" {
		errs = append(errs, fmt.Errorf("metadata.name is empty"))
	}
	return errs
}

func validateAPIVersion(apiv, expectedGroup, expectedVersion string) []error {
	var errs []error
	if apiv == "" {
		return []error{fmt.Errorf("missing apiVersion")}
	}
	group := apiv
	version := ""
	if i := strings.IndexRune(apiv, '/'); i >= 0 {
		group = apiv[:i]
		version = apiv[i+1:]
	}
	if group != expectedGroup {
		errs = append(errs, fmt.Errorf("group mismatch: got %s want %s", group, expectedGroup))
	}
	if expectedVersion != "" && version != "" && version != expectedVersion {
		errs = append(errs, fmt.Errorf("version mismatch: got %s want %s", version, expectedVersion))
	}
	return errs
}

// Validate validates obj by looking up its Kind in crdMap (v1alpha1 by default).
func Validate(obj *unstructured.Unstructured, crdMap map[string]*apiextensionsv1.CustomResourceDefinition) []error {
	if obj == nil {
		return []error{fmt.Errorf("nil obj")}
	}
	kind := obj.GetKind()
	crd := crdMap[kind]
	if crd == nil {
		return []error{fmt.Errorf("no CRD for kind %s", kind)}
	}
	fn, err := ValidatorFor(crd, "v1alpha1")
	if err != nil {
		return []error{err}
	}
	return fn(obj)
}

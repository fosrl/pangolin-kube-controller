package testschema

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// RepoRoot walks up from the current working directory to locate the repo root
// by finding a go.mod that declares the module "pangolin-kube-controller".
// It returns the directory path, or "." on failure.
func RepoRoot() string {
	wd, _ := os.Getwd()
	d := wd
	for {
		gomod := filepath.Join(d, "go.mod")
		f, err := os.Open(gomod)
		if err == nil {
			// read first few lines to match module name
			s := bufio.NewScanner(f)
			for s.Scan() {
				line := strings.TrimSpace(s.Text())
				if strings.HasPrefix(line, "module ") && strings.Contains(line, "pangolin-kube-controller") {
					_ = f.Close()
					return d
				}
			}
			_ = f.Close()
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "."
		}
		d = parent
	}
}

// TestDataPath builds an absolute path to a file under test/testdata.
func TestDataPath(elems ...string) string {
	root := RepoRoot()
	parts := append([]string{root, "test", "testdata"}, elems...)
	return filepath.Join(parts...)
}

// LoadTraefikCRDs loads all CRD YAMLs for the given version directory
// (e.g., "v3.7.19") and returns parsed CRD objects. It reads from the repo path
// test/testdata/crds/traefik/<version> to avoid go:embed constraints.
// LoadTraefikCRDs loads all CRD YAMLs for the given version directory
// (e.g., "v3.7.19") and returns parsed CRD objects.
// Complexity reduction: delegate file parsing to helper, flatten conditions.
func LoadTraefikCRDs(version string) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	dir := filepath.Join(TestDataPath("crds", "traefik"), version)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read CRD dir %s: %w", dir, err)
	}

	var out []*apiextensionsv1.CustomResourceDefinition
	for _, e := range entries {
		if e.IsDir() { // skip directories early
			continue
		}
		name := e.Name()
		switch ext := filepath.Ext(name); ext { // explicit switch keeps future extensions easy
		case ".yaml", ".yml":
			crds, err := loadCRDsFromFile(filepath.Join(dir, name))
			if err != nil {
				return nil, err
			}
			out = append(out, crds...)
		default:
			continue
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no CRDs found for version %s", version)
	}
	return out, nil
}

// loadCRDsFromFile reads a YAML file that may contain multiple documents and returns all CRDs within.
func loadCRDsFromFile(path string) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	docs := splitYAMLDocuments(b)
	if len(docs) == 0 { // empty file
		return nil, nil
	}
	out := make([]*apiextensionsv1.CustomResourceDefinition, 0, len(docs))
	for _, d := range docs {
		if len(d) == 0 { // defensive; splitYAMLDocuments already prunes
			continue
		}
		var crd apiextensionsv1.CustomResourceDefinition
		if err := yaml.Unmarshal(d, &crd); err != nil {
			return nil, fmt.Errorf("unmarshal CRD %s: %w", filepath.Base(path), err)
		}
		out = append(out, &crd)
	}
	return out, nil
}

func splitYAMLDocuments(b []byte) [][]byte {
	// Reduced complexity: leverage strings.Split and TrimSpace; ignore empty docs.
	parts := strings.Split(string(b), "\n---")
	if len(parts) == 1 { // single doc fast-path
		p := strings.TrimSpace(parts[0])
		if p == "" {
			return nil
		}
		return [][]byte{[]byte(p)}
	}
	docs := make([][]byte, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		docs = append(docs, []byte(p))
	}
	return docs
}

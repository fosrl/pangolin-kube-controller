package testschema

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const crdVersion = "v3.5.6"

func TestRepoRootAndTestDataPath(t *testing.T) {
	root := RepoRoot()
	if root == "." {
		t.Fatalf("RepoRoot returned '.'; expected to find module root")
	}
	// Verify testdata path resolves and exists
	p := TestDataPath("crds", "traefik", crdVersion)
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("TestDataPath not found: %v", err)
	}
	if !fi.IsDir() {
		t.Fatalf("TestDataPath should be a directory: %s", p)
	}
	// Path should be absolute under repo root
	if !filepath.IsAbs(p) {
		t.Fatalf("TestDataPath should return an absolute path, got: %s", p)
	}
}

func TestLoadTraefikCRDsSuccessAndErrors(t *testing.T) {
	// Success for known version directory
	crds, err := LoadTraefikCRDs(crdVersion)
	if err != nil {
		t.Fatalf("LoadTraefikCRDs error: %v", err)
	}
	if len(crds) != 10 {
		t.Fatalf("expected 10 CRDs for v3.5.6, got %d", len(crds))
	}

	// Error for missing version directory
	if _, err := LoadTraefikCRDs("v0.0.0-does-not-exist"); err == nil {
		t.Fatalf("expected error for missing version directory")
	}
}

func TestLoadCRDsFromFileVarious(t *testing.T) {
	// Success parsing a real CRD file
	path := TestDataPath("crds", "traefik", crdVersion, "traefik.io_ingressroutes.yaml")
	crds, err := loadCRDsFromFile(path)
	if err != nil {
		t.Fatalf("loadCRDsFromFile error: %v", err)
	}
	if len(crds) == 0 {
		t.Fatalf("expected at least one CRD from file: %s", path)
	}

	// Empty file returns no CRDs and no error
	tmp := t.TempDir()
	empty := filepath.Join(tmp, "empty.yaml")
	if err := os.WriteFile(empty, []byte("\n\n"), 0o644); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	crds, err = loadCRDsFromFile(empty)
	if err != nil {
		t.Fatalf("unexpected error for empty file: %v", err)
	}
	// len() is defined as zero for a nil slice; no need to check for nil explicitly.
	if len(crds) != 0 {
		t.Fatalf("expected empty slice for empty file, got len=%d", len(crds))
	}

	// Non-existent file returns an error
	if _, err := loadCRDsFromFile(filepath.Join(tmp, "nope.yaml")); err == nil {
		t.Fatalf("expected error for non-existent file")
	}
}

const (
	kindA = "kind: A"
	kindB = "kind: B"
	kindC = "kind: C"
)

func TestSplitYAMLDocumentsEmpty(t *testing.T) {
	cases := []string{
		"",         // empty string
		"    ",     // all spaces
		"\n\n",     // newlines only
		"   \n   ", // mixed whitespace
	}
	for _, input := range cases {
		got := splitYAMLDocuments([]byte(input))
		require.Nil(t, got, "input %q: empty/whitespace should yield nil, got %d docs", input, len(got))
	}
}

func TestSplitYAMLDocumentsSingle(t *testing.T) {
	singleDocYAML := "kind: foo\nmetadata:\n  name: bar\n"
	expected := "kind: foo\nmetadata:\n  name: bar"
	docs := splitYAMLDocuments([]byte(singleDocYAML))
	require.Len(t, docs, 1, "want 1 doc, got %d", len(docs))
	require.Equal(t, expected, string(docs[0]), "unexpected content: %q", string(docs[0]))
}

func TestSplitYAMLDocumentsMulti(t *testing.T) {
	// Test multi-document YAML input without a leading separator, to match expected document boundaries.
	multiDocYAML := kindA + "\n---\n\n" + kindB + "\n---\n  \n" + kindC + "\n"
	docs := splitYAMLDocuments([]byte(multiDocYAML))
	require.Len(t, docs, 3, "want 3 docs, got %d", len(docs))
	require.Equal(t, kindA, string(docs[0]), "doc[0] mismatch: got %q want %q", string(docs[0]), kindA)
	require.Equal(t, kindB, string(docs[1]), "doc[1] mismatch: got %q want %q", string(docs[1]), kindB)
	require.Equal(t, kindC, string(docs[2]), "doc[2] mismatch: got %q want %q", string(docs[2]), kindC)
}

// Edge case: leading separator at start yields no empty doc before it; the separator is preserved in the first document.
func TestSplitYAMLDocumentsLeadingSeparator(t *testing.T) {
	yamlWithLeadingSeparator := "---\n" + kindA + "\n---\n" + kindB + "\n"
	docs := splitYAMLDocuments([]byte(yamlWithLeadingSeparator))
	require.Len(t, docs, 2, "leading separator: want 2 docs, got %d", len(docs))
	require.Equal(t, "---\n"+kindA, string(docs[0]), "leading separator doc[0] mismatch: got %q", string(docs[0]))
	require.Equal(t, kindB, string(docs[1]), "leading separator doc[1] mismatch: got %q", string(docs[1]))
}

// Edge case: trailing separator after last document should be ignored (no empty doc appended).
func TestSplitYAMLDocumentsTrailingSeparator(t *testing.T) {
	yamlWithTrailingSeparator := kindA + "\n---\n" + kindB + "\n---"
	docs := splitYAMLDocuments([]byte(yamlWithTrailingSeparator))
	require.Len(t, docs, 2, "trailing separator: want 2 docs, got %d", len(docs))
	require.Equal(t, kindA, string(docs[0]), "trailing separator doc[0] mismatch: got %q", string(docs[0]))
	require.Equal(t, kindB, string(docs[1]), "trailing separator doc[1] mismatch: got %q", string(docs[1]))
}

// Edge case: input consisting only of separators devolves to a single doc containing the first '---'.
// This test asserts that for input of only document separators, the current behavior is intentional: a single doc containing the first '---', as documented.
func TestSplitYAMLDocumentsOnlySeparators(t *testing.T) {
	in := "---\n---\n---"
	docs := splitYAMLDocuments([]byte(in))
	require.Len(t, docs, 1, "only separators: want 1 doc, got %d", len(docs))
	require.Equal(t, "---", string(docs[0]), "only separators doc[0] mismatch: got %q", string(docs[0]))
}

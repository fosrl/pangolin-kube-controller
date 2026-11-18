package protocol

import (
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// TLSArtifacts is the Kubernetes representation of Pangolin's TLS options.
type TLSArtifacts struct {
	// Options maps the sanitized option name to a TLSOption spec.
	Options map[string]json.RawMessage
	// Secrets hold the client CA bundles TLSOptions reference.
	Secrets []*corev1.Secret
	Failed  map[string]error
}

// ClientCASecretKey is the Secret key Traefik reads a client CA bundle from.
const ClientCASecretKey = "ca.crt"

// ClientCASecretName is the Secret holding the client CAs of a TLS option.
func ClientCASecretName(option string) string { return option + "-client-ca" }

// TransformTLSOptions converts dynamic-config TLS options to TLSOption specs.
//
// Pangolin enforces mTLS with `clientAuth.caFiles` holding PEM contents
// inline. The TLSOption CRD has no caFiles field: it reads CAs from Secrets
// listed in `clientAuth.secretNames`. The PEMs are therefore written to a
// Secret per option. caFiles entries that are file paths cannot be resolved by
// the controller, so an option with any of them is rejected rather than
// enforced with a partial CA set.
func TransformTLSOptions(namespace string, options map[string]json.RawMessage) TLSArtifacts {
	out := TLSArtifacts{Options: map[string]json.RawMessage{}, Failed: map[string]error{}}
	for _, name := range sortedKeys(options) {
		var spec map[string]interface{}
		if err := json.Unmarshal(options[name], &spec); err != nil {
			out.Failed[name] = fmt.Errorf("tls option %s: %w", name, err)
			continue
		}
		if spec == nil {
			spec = map[string]interface{}{}
		}
		secret, err := convertClientAuth(namespace, name, spec)
		if err != nil {
			out.Failed[name] = err
			continue
		}
		b, err := json.Marshal(spec)
		if err != nil {
			out.Failed[name] = err
			continue
		}
		out.Options[name] = b
		if secret != nil {
			out.Secrets = append(out.Secrets, secret)
		}
	}
	return out
}

func convertClientAuth(namespace, name string, spec map[string]interface{}) (*corev1.Secret, error) {
	ca, ok := spec["clientAuth"].(map[string]interface{})
	if !ok {
		return nil, nil
	}
	files, _ := ca["caFiles"].([]interface{})
	delete(ca, "caFiles")
	if len(files) == 0 {
		return nil, nil
	}
	pems := make([]string, 0, len(files))
	for i, f := range files {
		s, _ := f.(string)
		s = strings.TrimSpace(s)
		if !strings.HasPrefix(s, "-----BEGIN") {
			return nil, fmt.Errorf("tls option %s: clientAuth.caFiles[%d] is not inline PEM content; file paths cannot be resolved in Kubernetes", name, i)
		}
		pems = append(pems, s)
	}
	secretName := ClientCASecretName(name)
	existing, _ := ca["secretNames"].([]interface{})
	ca["secretNames"] = append(existing, secretName)

	secret := &corev1.Secret{}
	secret.APIVersion = "v1"
	secret.Kind = "Secret"
	secret.Name = secretName
	secret.Namespace = namespace
	secret.Type = corev1.SecretTypeOpaque
	secret.Data = map[string][]byte{ClientCASecretKey: []byte(strings.Join(pems, "\n") + "\n")}
	return secret, nil
}

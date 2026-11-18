package protocol

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

const testPEM = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"

func TestTransformTLSOptionsMovesCAsToSecret(t *testing.T) {
	art := TransformTLSOptions("ns", map[string]json.RawMessage{
		"resource-1-mtls": json.RawMessage(`{"clientAuth":{"caFiles":["` + "-----BEGIN CERTIFICATE-----\\nMIIB\\n-----END CERTIFICATE-----" + `"],"clientAuthType":"RequireAndVerifyClientCert"},"minVersion":"VersionTLS12"}`),
	})
	require.Empty(t, art.Failed)
	var spec map[string]interface{}
	require.NoError(t, json.Unmarshal(art.Options["resource-1-mtls"], &spec))
	ca := spec["clientAuth"].(map[string]interface{})
	require.NotContains(t, ca, "caFiles")
	require.Equal(t, []interface{}{"resource-1-mtls-client-ca"}, ca["secretNames"])
	require.Equal(t, "VersionTLS12", spec["minVersion"])
	require.Len(t, art.Secrets, 1)
	require.Equal(t, testPEM+"\n", string(art.Secrets[0].Data[ClientCASecretKey]))
}

func TestTransformTLSOptionsRejectsFilePaths(t *testing.T) {
	art := TransformTLSOptions("ns", map[string]json.RawMessage{
		"opt": json.RawMessage(`{"clientAuth":{"caFiles":["/etc/ca.pem"]}}`),
	})
	require.Contains(t, art.Failed, "opt")
	require.Empty(t, art.Options)
	require.Empty(t, art.Secrets)
}

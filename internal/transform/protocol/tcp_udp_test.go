package protocol

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	traefikconfig "pangolin-kube-controller/internal/transform/config"
)

func TestTransformTCPKeepsUserTransportAndSkipsUnknownService(t *testing.T) {
	cfg := &traefikconfig.Config{TCP: &traefikconfig.TCPUDPConfig{
		Routers: map[string]json.RawMessage{
			"ok":       json.RawMessage(`{"entryPoints":["tcp-1"],"rule":"HostSNI(` + "`*`" + `)","service":"svc","tls":{"passthrough":true,"options":"opt"}}`),
			"dangling": json.RawMessage(`{"entryPoints":["tcp-2"],"rule":"HostSNI(` + "`*`" + `)","service":"missing"}`),
		},
		Services: map[string]json.RawMessage{
			"svc": json.RawMessage(`{"loadBalancer":{"servers":[{"address":"10.0.0.1:22"}],"serversTransport":"mine"}}`),
		},
		ServersTransports: map[string]json.RawMessage{"mine": json.RawMessage(`{"dialTimeout":"5s"}`)},
	}}
	art := TransformTCP(cfg, "ns")
	require.Contains(t, art.Failed, "dangling")
	require.Contains(t, art.Routes, "ok")
	spec := art.Routes["ok"]["spec"].(map[string]interface{})
	require.Equal(t, map[string]interface{}{"passthrough": true, "options": map[string]interface{}{"name": "opt"}}, spec["tls"])
	child := spec["routes"].([]interface{})[0].(map[string]interface{})["services"].([]interface{})[0].(map[string]interface{})
	require.Equal(t, "mine", child["serversTransport"])
	require.Len(t, art.Transports, 1, "no PROXY protocol transport is synthesised for a user transport")
	require.Len(t, art.Services, 1)
}

func TestTransformUDPHasNoMatch(t *testing.T) {
	cfg := &traefikconfig.Config{UDP: &traefikconfig.TCPUDPConfig{
		Routers:  map[string]json.RawMessage{"r": json.RawMessage(`{"entryPoints":["udp-53"],"service":"s"}`)},
		Services: map[string]json.RawMessage{"s": json.RawMessage(`{"loadBalancer":{"servers":[{"address":"10.0.0.1:53"},{"address":"10.0.0.1:54"}]}}`)},
	}}
	art := TransformUDP(cfg, "ns")
	route := art.Routes["r"]["spec"].(map[string]interface{})["routes"].([]interface{})[0].(map[string]interface{})
	require.NotContains(t, route, "match")
	require.Len(t, route["services"], 2)
	require.Len(t, art.Services, 2)
}

package config

import "encoding/json"

const (
	// Group is the Traefik CRD API group.
	Group = "traefik.io"
	// Version is the Traefik CRD API version.
	Version = "v1alpha1"
	// GroupVersion is the combined group/version string.
	GroupVersion = Group + "/" + Version
)

// Config is a simplified Traefik dynamic configuration model consumed by the
// controller.
type Config struct {
	HTTP HTTPConfig    `json:"http"`
	TCP  *TCPUDPConfig `json:"tcp,omitempty"`
	UDP  *TCPUDPConfig `json:"udp,omitempty"`
	TLS  *TLSConfig    `json:"tls,omitempty"`
}

// TLSConfig contains the TLS options Pangolin defines (for example the
// client-certificate requirements of mTLS resources). Stores and certificates
// are not modelled: Pangolin does not emit them.
type TLSConfig struct {
	Options map[string]json.RawMessage `json:"options,omitempty"`
}

// HTTPConfig contains HTTP middlewares, routers, services, and transports.
type HTTPConfig struct {
	Middlewares       map[string]json.RawMessage `json:"middlewares"`
	Routers           map[string]json.RawMessage `json:"routers"`
	Services          map[string]json.RawMessage `json:"services"`
	ServersTransports map[string]json.RawMessage `json:"serversTransports"`
}

// TCPUDPConfig contains TCP/UDP routers, services, and transports.
type TCPUDPConfig struct {
	Routers           map[string]json.RawMessage `json:"routers"`
	Services          map[string]json.RawMessage `json:"services"`
	ServersTransports map[string]json.RawMessage `json:"serversTransports"`
}

package testutil

// Shared test constants used across multiple test packages.
const (
	ProjectName                = "pangolin-kube-controller" //NOSONARLINT - No Duplicate
	TestNamespace              = "pangolin"
	ManagedLabelKey            = "pangolin.io/managed-by"
	ManagedAnnoKey             = "pangolin.io/managed-by"
	ManagedLabelValue          = "pangolin-kube-controller" //NOSONARLINT - No Duplicate
	ManagedAnnoValue           = "pangolin-kube-controller" //NOSONARLINT - No Duplicate
	DefaultIngressClass        = "traefik"
	DefaultCRDVersion          = "v3.5.6"
	TCPTransportName           = "my-tcp-transport"
	TraefikGroup               = "traefik.io"
	TraefikVersion             = "v1alpha1"
	TraefikServiceKind         = "TraefikService"
	TraefikMiddlewareKind      = "Middleware"
	TraefikIngressRouteKind    = "IngressRoute"
	TraefikIngressRouteTCPKind = "IngressRouteTCP"
	TraefikIngressRouteUDPKind = "IngressRouteUDP"
)

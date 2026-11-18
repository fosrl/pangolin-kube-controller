<!-- markdownlint-disable MD024 -->
# Changelog

All notable changes to this project are documented in this file.

The format is inspired by Keep a Changelog and based on Conventional Commits from the repository history.

## [Unreleased]

## [0.1.0] - 2026-11-18

First release out of alpha. It targets Pangolin 1.26 and Traefik 3.5 to 3.7.

### Fixed

- HTTP services now reach their backends. The controller wrote Pangolin's
  `loadBalancer` into TraefikServices, a field the CRD does not have; the API
  server pruned it and every tunnel-backed resource ended up with no servers.
  Each Pangolin service is now a `weighted` TraefikService over generated
  selectorless Services and EndpointSlices.
- TCP and UDP resources are reconciled again. Sanitization dropped the `tcp` and
  `udp` sections entirely.
- Resources with several targets work. Traefik de-duplicates endpoints by
  address, and two targets on one Newt site share an address, so they are now
  spread over separate Services with weights. A TCP/UDP service whose targets
  used different ports failed the whole reconcile before.
- Generated EndpointSlices carry the `kubernetes.io/service-name` label and
  named ports. Traefik selects slices by that label and skips ports without a
  name, so the previous TCP/UDP slices were never used.
- Generated Service names are valid DNS-1035 labels (`pkc-<service>`).
  Pangolin's names start with the resource id, which the API server rejects.
- mTLS (Pangolin 1.25+): `tls.options` become `TLSOption` objects with the
  client CAs in a Secret, and router `tls.options` is written as the object
  reference the IngressRoute CRD expects instead of a bare string.
- `chain` middlewares use `MiddlewareRef` objects as the CRD requires.
- References to other Traefik providers are kept: redirects (`noop@internal`)
  and user middlewares such as `crowdsec@file` were renamed into objects that
  do not exist. PROXY protocol references (`pp-transport-v1@file`) get a
  generated `ServersTransportTCP`, and the transport is read from
  `loadBalancer.serversTransport` where Pangolin writes it.
- The ingress class annotation is `kubernetes.io/ingress.class`, which Traefik
  actually reads, and it is set on IngressRouteTCP and IngressRouteUDP too.
- The Traefik instance label (`TRAEFIK_INSTANCE_LABEL*`) is applied to every
  managed object, not only EndpointSlices.
- TCP and UDP reconciliation no longer garbage-collect each other's Services.

### Changed

- **BREAKING:** the controller now also manages `tlsoptions` (traefik.io),
  `services`, `secrets` and `endpointslices`. Grant RBAC for them; the Pangolin
  Helm chart does this from chart 0.1.0.
- Every object, including new ones, is written with server-side apply and
  `force`. Objects created by 0.1.0-alpha.1 are migrated to the apply field
  manager on first contact, so fields Pangolin stops emitting are removed.
- A Pangolin entry that cannot be converted is skipped with a warning and its
  existing objects are kept, instead of failing the whole reconcile.
- The controller refuses to overwrite a Service, EndpointSlice or Secret it
  does not manage.

### Added

- `RESYNC_INTERVAL` (default `10m`) re-applies the full config even when
  Pangolin reports no change, repairing objects edited or deleted by hand.
- Certificate secret configuration and the `/api/v1/certificates` endpoint.
- Tests validate every generated object against the Traefik CRD schemas,
  including unknown fields, using a Pangolin 1.26 config fixture.

### Security

- `go.opentelemetry.io/otel` 1.47.0, `golang.org/x/net` 0.62.0 and
  `golang.org/x/text` 0.43.0.

### Internal

- Simplified controller leadership, garbage-collection, and readiness handling.
- Simplified Kubernetes client construction and related tests.
- Improved CI artifact metadata handling and Go tool caching.

## [0.1.0-alpha.1] - 2026-04-13

### Added

- Initial project scaffold and baseline controller implementation.
- Added a read-only check in applyDesiredObjects to improve reconciliation safety.

### Fixed

- Fixed Trivy GitHub Action execution in CI.
- Improved linting behavior and release signing flow.
- Added conditional SBOM upload behavior in CI based on job output.
- Updated Go image digests in Docker images.
- Updated Go version and OpenTelemetry dependencies.

### Changed

- Prepared release workflow and project metadata for 0.1.0-alpha.1.

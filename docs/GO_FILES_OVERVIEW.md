# Go Files Overview

Purpose: quick, non-developer friendly guide to what each Go file does and why it exists. Grouped by folder.

## Notes

- “Tests” entries are unit/integration tests that verify the behavior described; names indicate focus.
- CRD = Custom Resource Definition (Traefik’s Kubernetes objects such as IngressRoute, Middleware, TraefikService).

## cmd
- cmd/controller/doc.go — Command pangolin-kube-controller runs the Pangolin Kubernetes Controller
- cmd/controller/main.go — The main package for the Pangolin Kube Controller application
- cmd/controller/main_test.go — Tests main.
- cmd/healthcheck/doc.go — Command healthcheck probes the controller's readiness endpoint and exits 0 on
- cmd/healthcheck/main.go — Health probe binary. Runs liveness/readiness checks.
- cmd/healthcheck/main_test.go — Tests main.

## hack
- hack/tools/doccheck/main.go — Command doccheck scans packages to report missing package docs and exported
- hack/tools/doccheck/main_test.go — Tests main.
- hack/tools/generate.go — Go source file.
- hack/tools/genfilemap/main.go — Command genfilemap generates a Markdown overview of tracked Go files grouped
- hack/tools/genfilemap/main_test.go — Tests main.

## internal/apply
- internal/apply/apply_extra_test.go — Tests apply extra.
- internal/apply/diff.go — Go source file.
- internal/apply/diff_test.go — Tests diff.
- internal/apply/doc.go — Package apply provides Kubernetes resource apply operations using
- internal/apply/endpointslice.go — Go source file.
- internal/apply/endpointslice_test.go — Tests endpointslice.
- internal/apply/ingressroute.go — Go source file.
- internal/apply/ingressroute_test.go — Tests ingressroute.
- internal/apply/metadata.go — Go source file.
- internal/apply/metadata_test.go — Tests metadata.
- internal/apply/numeric.go — Go source file.
- internal/apply/numeric_test.go — Tests numeric.
- internal/apply/service.go — Go source file.
- internal/apply/service_test.go — Tests service.
- internal/apply/test_consts_test.go — Tests test consts.
- internal/apply/unstructured.go — Go source file.
- internal/apply/unstructured_test.go — Tests unstructured.

## internal/certificates
- internal/certificates/certificates.go — Go source file.
- internal/certificates/certificates_test.go — Tests certificates.
- internal/certificates/doc.go — Package certificates provides the HTTP handler for the /api/v1/certificates

## internal/config
- internal/config/config.go — Go source file.
- internal/config/config_test.go — Tests config.
- internal/config/defaults.go — Go source file.
- internal/config/doc.go — Package config loads and normalizes controller configuration from the
- internal/config/env.go — Go source file.
- internal/config/env_test.go — Tests env.
- internal/config/file.go — Go source file.
- internal/config/normalize.go — Go source file.
- internal/config/validate.go — Go source file.

## internal/controller
- internal/controller/apply.go — Go source file.
- internal/controller/apply_extra_test.go — Tests apply extra.
- internal/controller/apply_test.go — Tests apply.
- internal/controller/backoff.go — Go source file.
- internal/controller/backoff_extra_test.go — Tests backoff extra.
- internal/controller/backoff_test.go — Tests backoff.
- internal/controller/change_detection.go — Go source file.
- internal/controller/change_detection_test.go — Tests change detection.
- internal/controller/controller.go — Go source file.
- internal/controller/controller_test.go — Tests controller.
- internal/controller/doc.go — Package controller implements the main reconciliation loop, leader election,
- internal/controller/fetch.go — Go source file.
- internal/controller/fetch_extra_test.go — Tests fetch extra.
- internal/controller/fetch_test.go — Tests fetch.
- internal/controller/leader_election.go — Go source file.
- internal/controller/leader_election_extra_test.go — Tests leader election extra.
- internal/controller/leader_election_test.go — Tests leader election.
- internal/controller/loop.go — Go source file.
- internal/controller/loop_extra_test.go — Tests loop extra.
- internal/controller/loop_test.go — Tests loop.
- internal/controller/readiness.go — Go source file.
- internal/controller/readiness_test.go — Tests readiness.

## internal/httpserver
- internal/httpserver/doc.go — Package httpserver exposes the controller's metrics and health endpoints
- internal/httpserver/routes.go — Go source file.
- internal/httpserver/routes_test.go — Tests routes.
- internal/httpserver/server.go — Go source file.
- internal/httpserver/server_test.go — Tests server.
- internal/httpserver/tls.go — Go source file.
- internal/httpserver/tls_test.go — Tests tls.

## internal/kube
- internal/kube/client.go — Go source file.
- internal/kube/client_test.go — Tests client.
- internal/kube/crd.go — Go source file.
- internal/kube/doc.go — Package kube constructs Kubernetes clients used by the controller
- internal/kube/labels/doc.go — Package labels resolves and verifies the Traefik instance label
- internal/kube/labels/resolver.go — Go source file.
- internal/kube/labels/resolver_extra_test.go — Tests resolver extra.
- internal/kube/labels/resolver_test.go — Tests resolver.
- internal/kube/resources/doc.go — Package resources contains small adapters that wrap Kubernetes dynamic client
- internal/kube/resources/resource_adapter.go — Go source file.
- internal/kube/resources/resource_adapter_test.go — Tests resource adapter.

## internal/observability/health
- internal/observability/health/doc.go — Package health provides health check endpoints for the controller

## internal/observability/logging
- internal/observability/logging/doc.go — Package logging provides helpers for safe, structured logging such as redaction
- internal/observability/logging/redact.go — Go source file.
- internal/observability/logging/redact_test.go — Tests redact.

## internal/observability/metrics
- internal/observability/metrics/doc.go — Package metrics defines Prometheus metrics for the controller and exposes an

## internal/observability/metrics_otel
- internal/observability/metrics_otel/attributes.go — Go source file.
- internal/observability/metrics_otel/constants.go — Go source file.
- internal/observability/metrics_otel/doc.go — Package metrics_otel provides OpenTelemetry metric instruments that mirror the
- internal/observability/metrics_otel/otelmetrics.go — Go source file.
- internal/observability/metrics_otel/otelmetrics_test.go — Tests otelmetrics.
- internal/observability/metrics_otel/shutdown_test.go — Tests shutdown.

## internal/observability/metrics_prometheus
- internal/observability/metrics_prometheus/doc.go — Package metrics_prometheus provides Prometheus metrics collection
- internal/observability/metrics_prometheus/metrics.go — Go source file.
- internal/observability/metrics_prometheus/metrics_test.go — Tests metrics.
- internal/observability/metrics_prometheus/shutdown_test.go — Tests shutdown.

## internal/observability/profiling
- internal/observability/profiling/doc.go — Package profiling provides pprof endpoints for performance analysis

## internal/observability/tracing
- internal/observability/tracing/doc.go — Package tracing provides distributed tracing support for the controller

## internal/orchestration
- internal/orchestration/run.go — Package orchestration provides the top-level orchestration logic for
- internal/orchestration/run_test.go — Tests run.

## internal/pangolin
- internal/pangolin/doc.go — Package pangolin provides the HTTP client for fetching configuration from

## internal/reconcile
- internal/reconcile/doc.go — Package reconcile implements the core reconciliation phases for applying
- internal/reconcile/gc.go — Go source file.
- internal/reconcile/gc_test.go — Tests gc.

## internal/testschema
- internal/testschema/deterministic_yaml.go — Go source file.
- internal/testschema/deterministic_yaml_test.go — Tests deterministic yaml.
- internal/testschema/doc.go — Package testschema provides lightweight helpers to load, scrub, and validate
- internal/testschema/loader.go — Go source file.
- internal/testschema/loader_test.go — Tests loader.
- internal/testschema/scrub.go — Go source file.
- internal/testschema/scrub_test.go — Tests scrub.
- internal/testschema/validate.go — Go source file.
- internal/testschema/validate_test.go — Tests validate.

## internal/testutil
- internal/testutil/apply_reactor.go — Go source file.
- internal/testutil/consts.go — Go source file.
- internal/testutil/doc.go — Package testutil contains generic helpers used by tests across packages,
- internal/testutil/helpers.go — Go source file.
- internal/testutil/helpers_test.go — Tests helpers.

## internal/transform
- internal/transform/backend/backend.go — Package backend turns the server lists of Pangolin's Traefik dynamic
- internal/transform/backend/backend_test.go — Tests backend.
- internal/transform/config/config.go — Go source file.
- internal/transform/config/config_test.go — Tests config.
- internal/transform/config/doc.go — Package config defines a lightweight model of Traefik dynamic configuration
- internal/transform/doc.go — Package transform provides Traefik configuration transformation, including
- internal/transform/protocol/doc.go — Package protocol contains helpers for HTTP and TCP/UDP protocol handling
- internal/transform/protocol/env_url_test.go — Tests env url.
- internal/transform/protocol/http_conversion.go — Go source file.
- internal/transform/protocol/http_conversion_regression_test.go — Tests http conversion regression.
- internal/transform/protocol/http_conversion_test.go — Tests http conversion.
- internal/transform/protocol/tcp_udp.go — Go source file.
- internal/transform/protocol/tcp_udp_test.go — Tests tcp udp.
- internal/transform/protocol/tls.go — Go source file.
- internal/transform/protocol/tls_test.go — Tests tls.
- internal/transform/routing/annotation_test.go — Tests annotation.
- internal/transform/routing/doc.go — Package routing converts simplified router definitions to Traefik
- internal/transform/routing/entrypoints_annotation_test.go — Tests entrypoints annotation.
- internal/transform/routing/entrypoints_test.go — Tests entrypoints.
- internal/transform/routing/router_priority_test.go — Tests router priority.
- internal/transform/routing/routing.go — Go source file.
- internal/transform/routing/routing_test.go — Tests routing.
- internal/transform/routing/test_consts_test.go — Tests test consts.
- internal/transform/routing/transform_test.go — Tests transform.
- internal/transform/sanitize/doc.go — Package sanitize normalizes resource names and rewrites cross-references in
- internal/transform/sanitize/reference_test.go — Tests reference.
- internal/transform/sanitize/router_middleware_order_test.go — Tests router middleware order.
- internal/transform/sanitize/sanitize.go — Go source file.
- internal/transform/sanitize/sanitize_extra_test.go — Tests sanitize extra.
- internal/transform/sanitize/sanitize_test.go — Tests sanitize.
- internal/transform/sanitize/servers_transport_test.go — Tests servers transport.
- internal/transform/testutil/constants.go — Go source file.
- internal/transform/testutil/doc.go — Package testutil provides helpers for controller package tests such as
- internal/transform/testutil/objects.go — Go source file.
- internal/transform/testutil/objects_test.go — Tests objects.
- internal/transform/testutil/resource_interface_spy.go — Go source file.

## internal/version
- internal/version/doc.go — Package version exposes build metadata (Version, Commit, Date) for the
- internal/version/version.go — Go source file.
- internal/version/version_test.go — Tests version.

## test
- test/e2e/doc.go — Package e2e contains end-to-end style helpers and tests that validate the
- test/e2e/helpers.go — Integration/E2E test helpers and assets.
- test/e2e/helpers_test.go — Tests helpers.
- test/e2e/offline_e2e_test.go — Tests offline e2e.
- test/e2e/pangolin_e2e_test.go — Tests pangolin e2e.
- test/integration/controller_integration_test.go — Tests controller integration.
- test/integration/pangolin_integration_test.go — Tests pangolin integration.
- test/integration/suite_test.go — Tests suite.


<!-- markdownlint-disable MD013 MD033 -->
# **Pangolin Kubernetes Controller — Unified Documentation**

## Overview

The Pangolin Kubernetes Controller synchronizes dynamic Traefik Custom Resource Definitions (CRDs) — e.g., IngressRoute, Middleware, TraefikService — from a Pangolin Traefik Config API (commonly used as an HTTPFileProvider for Traefik) into a Kubernetes namespace.

- Quick Start: see the "Quick Start" section below.

---

## **Quick Summary**

The **Pangolin Kubernetes Controller** automatically synchronizes dynamic Traefik Custom Resource Definitions (CRDs) such as *IngressRoute*, *Middleware*, and *TraefikService* from the **Pangolin Traefik Config API** (usually used as an `HTTPFileProvider` for Traefik) into a Kubernetes namespace.

- **Key Benefits:**
  - Safe, minimal writes via ETag/body-hash change detection
  - Optional high-availability leader election
  - Robust observability: structured logs, metrics, traces
  - Read-only (dry-run) mode for validation/audit
- **Typical Use Cases:** Automated Traefik routing, safe configuration validation, CI/CD deployment pipelines

## **Prerequisites**

- Running **Kubernetes** cluster with Traefik V3 as IngressController installed
- **Pangolin Config API** endpoint accessible
- [Traefik CRD support enabled](https://doc.traefik.io/traefik/routing/providers/kubernetes-crd/)
- Appropriate **RBAC permissions** for Traefik CRDs (see examples below)
- **(Optional):** mTLS certificates and keys (via Secrets) for secure API access

---

## **Quick Start**

1. Ensure you have a running Kubernetes cluster with [Traefik v3 as IngressController](https://doc.traefik.io/traefik/routing/providers/kubernetes-crd/) and CRD support enabled.
2. Install the controller using Helm (recommended) or your own deployment manifests:

    ```bash
    # Recommended: install the published Helm chart
    helm repo add fossorial https://charts.fossorial.io
    helm repo update
    helm install pangolin fossorial/pangolin

    # Or apply your own Kubernetes manifests (not included in this repository)
    ```

3. Configure the controller via environment variables. The examples below show three common deployment approaches:

- Local development (shell): set env vars in your shell session for quick testing. These do not persist across shells.

  ```bash
  # local shell (development only)
  export CONFIG_ENDPOINT=https://your-pangolin:3001/api/v1/traefik-config
  export TARGET_NAMESPACE=pangolin
  # then run the binary locally
  ./pangolin-kube-controller
  ```

- Helm install (recommended for clusters): set chart values via `--set` or `values.yaml`. Example using `--set`:

  ```bash
  helm install pangolin fossorial/pangolin \
    --set env.CONFIG_ENDPOINT="https://your-pangolin:3001/api/v1/traefik-config" \
    --set env.TARGET_NAMESPACE="pangolin"
  ```

  Or add the values to your `values.yaml`:

  ```yaml
  env:
    CONFIG_ENDPOINT: https://your-pangolin:3001/api/v1/traefik-config
    TARGET_NAMESPACE: pangolin
  ```

- Kubernetes manifest (Deployment): add the env values under the `containers[].env` section in your Deployment manifest:

  ```yaml
  apiVersion: apps/v1
  kind: Deployment
  metadata:
    name: pangolin-kube-controller
    namespace: pangolin
  spec:
    replicas: 1
    template:
      spec:
        containers:
          - name: controller
            image: ghcr.io/fosrl/pangolin-kube-controller:0.1.0
            env:
              - name: CONFIG_ENDPOINT
                value: "https://your-pangolin:3001/api/v1/traefik-config"
              - name: TARGET_NAMESPACE
                value: "pangolin"
  ```

4. Verify the controller is running:

   ```bash
   kubectl get pods -n pangolin
   kubectl logs -n pangolin deployment/pangolin-kube-controller
   ```

5. Check the metrics endpoint:

   ```bash
   kubectl port-forward -n pangolin svc/pangolin-kube-controller 9090 &
   curl http://localhost:9090/metrics
   ```

For a production deployment, review and adjust the RBAC scope, resource limits, and environment variables in your deployment manifests (not included in this repository).

### **Key Features**

- **Change detection:** Uses ETag (`If-None-Match`/`304 Not Modified`) when available, falling back to `SHA256(body)` when not. Weak ETags (`W/`) are treated as equivalent when the body content is unchanged.
- **Garbage collection**: Deletes only resources it manages (label: `app.kubernetes.io/managed-by=pangolin-kube-controller`).
- **Leader election** (optional) for safe, high availability across replicas.
- **Exponential backoff** with jitter for safe retries.
- **Read-only (dry-run) mode** for production-safe testing and CI validation.
- **Prometheus & OpenTelemetry** metrics, traces, and optional profiling (pprof).

---

## **Goals**

- **Minimized writes:** Uses ETag/body-hash fallback to avoid unnecessary updates.
- **Safe & resilient:** Robust reconciliation with exponential backoff and optional leader election.
- **Observability:** Structured diff logging, metrics, traces.
- **Extensible:** Easy support for additional Traefik CRDs.
- **Zero required changes to Pangolin source code.**
- **Production-ready, full-HA support.**

---

## **Supported resource kinds**

The controller writes these objects into `TARGET_NAMESPACE`:

| Pangolin dynamic config | Kubernetes objects |
| --- | --- |
| `http.routers` | `IngressRoute` |
| `http.middlewares` | `Middleware` (`chain` members become `MiddlewareRef` objects) |
| `http.services` | `TraefikService` (`weighted`) plus selectorless `Service` + `EndpointSlice` per backend group |
| `http.serversTransports` | `ServersTransport` |
| `tcp.routers` / `tcp.services` | `IngressRouteTCP` plus `Service` + `EndpointSlice` |
| `tcp.serversTransports` and `pp-transport-v<N>@file` references | `ServersTransportTCP` |
| `udp.routers` / `udp.services` | `IngressRouteUDP` plus `Service` + `EndpointSlice` |
| `tls.options` | `TLSOption`, with `clientAuth.caFiles` moved to a `Secret` (`<option>-client-ca`, key `ca.crt`) |

### How backends are represented

Traefik's CRDs cannot carry a server list: an IngressRoute or TraefikService
can only reference Kubernetes Services. For every Pangolin service the
controller therefore creates headless, selectorless Services named
`pkc-<service>[-<n>]` whose EndpointSlices hold the backend addresses
(IPv4, IPv6 or hostnames). Two details of Traefik's resolver shape these:

- Traefik matches EndpointSlice ports to the Service port by **name**, so every
  generated port is named (`web`, `tcp` or `udp`).
- Traefik de-duplicates endpoints by **address only**. Two targets on the same
  Newt site share the site address and differ only by port, so they are placed
  in separate Services and combined with weights.

A server that is already a Service in `TARGET_NAMESPACE`
(`http://<name>.<ns>.svc...`) is referenced directly. Load-balancer options
(`sticky`, `strategy`, `passHostHeader`, `serversTransport`) are copied onto
each Service reference. Active `healthCheck`s are dropped: Traefik only
supports them for ExternalName Services.

References to another Traefik provider (`noop@internal`, `crowdsec@file`) are
passed through unchanged.

The controller never takes over a Service it did not create: if a Service with
a generated name exists without the managed annotation, the reconcile fails
with an error instead.

---

## **Architecture (High-level)**

1. **Fetch loop**: Poll JSON config from `CONFIG_ENDPOINT` via HTTP.
2. **Change detection**: Compare ETag or fall back to SHA256(body).
3. **Parse** raw JSON to a simplified `TraefikConfig`.
4. **Reconcile:** For each resource kind:
   - Only objects labeled `app.kubernetes.io/managed-by=pangolin-kube-controller`.
   - **Apply** with [Server-Side Apply (SSA)](https://kubernetes.io/docs/reference/using-api/server-side-apply/) and stable `fieldManager: "pangolin-kube-controller"`.
   - **Garbage Collect (GC):** Remove labeled resources not in desired set.
5. **Metrics & logging**: Record durations, changes, errors, GC events.
6. **Error handling**: Exponential backoff + jitter to avoid hot‑looping.
7. **(Optional) leader election**: Prevents concurrent writes in multi‑replica setups.

---

## **Environment Variables**

> Note: All durations use Go’s `time.Duration` syntax (e.g., `30s`). Leader election duration values are interrelated; see client-go leaderelection docs.

Core behavior

- CONFIG_ENDPOINT (string, required): Pangolin config API URL
- READ_ONLY (bool, default=false): Dry-run mode (no mutations)
- POLL_INTERVAL (duration, default=15s): Base polling/backoff interval
- RESYNC_INTERVAL (duration, default=10m): Re-apply the full config even when Pangolin reports no change, repairing objects that were edited or deleted by hand. `0` disables it.
- MAX_BACKOFF (duration, default=2m): Maximum backoff wait
- TARGET_NAMESPACE (string, default=pangolin): Namespace to manage Traefik CRDs
- ON_LOSE (string, default=exit): Behavior on leadership loss: exit | pause

HTTP/TLS fetch

- FETCH_TIMEOUT (duration, default=30s)
- CONFIG_AUTH_HEADER (string): Authorization header (e.g., "Bearer ...")
- CONFIG_CA_FILE (path)
- CONFIG_CLIENT_CERT_FILE (path)
- CONFIG_CLIENT_KEY_FILE (path)
- CONFIG_TLS_SKIP_VERIFY (bool, default=false) — do not use in production

HTTP transport tuning

- HTTP_MAX_IDLE_CONNS (int, default=100)
- HTTP_MAX_IDLE_CONNS_PER_HOST (int, default=100)
- HTTP_IDLE_CONN_TIMEOUT (duration, default=90s)

client-go tuning (Kubernetes)

- CLIENT_QPS (float, default=0=disabled)
- CLIENT_BURST (int, default=0=disabled)

Traefik specifics

- INGRESS_CLASS (string, default=traefik)
- TRAEFIK_INSTANCE_LABEL_KEY / TRAEFIK_INSTANCE_LABEL_VALUE (optional): Explicit instance label pair applied to all managed resources
- TRAEFIK_INSTANCE_LABEL (optional): Combined form "key=value" used if KEY/VALUE are not set
- INGRESS_CLASS_LABEL_VERIFY_INTERVAL (duration, default=3h): Periodic verification of the selected IngressClass having the instance label
- INGRESS_CLASS_LABEL_STRICT (bool, default=false): If true, a verification mismatch is fatal (CrashLoop)
- CONFIG_FILE (string, optional): Path to YAML/JSON with the same fields; precedence is ENV > file > defaults
- INGRESS_CLASS (string, default=traefik): Written as the `kubernetes.io/ingress.class` annotation on IngressRoute, IngressRouteTCP and IngressRouteUDP so a Traefik with `providers.kubernetesCRD.ingressClass` set picks them up
- TRAEFIK_LB_URL (string): Full URL used to fill empty TraefikService specs
- TRAEFIK_LB_IP (string), TRAEFIK_LB_SCHEME (default=http), TRAEFIK_LB_PORT (string): Alternative for building the URL

Logging

- CONFIG_LOG_PREVIEW (bool, default=false): When true, logs a redacted preview of the fetched Traefik configuration. Intended strictly for debugging. The preview passes through a redaction pipeline that replaces values of any JSON keys whose names contain (case-insensitive) "auth", "pass", "secret", "token", or "key" with "***redacted***".
- LOG_TRAEFIK_CONFIG (bool, default=false): Backward-compatible alias for CONFIG_LOG_PREVIEW. Also debug-only and goes through the same redaction pipeline.
- MAX_CONFIG_LOG_BYTES (int, default=0=no cap): Maximum number of bytes to include in the preview section; appended with "...<truncated>" when cut.
- FETCH_LOG_INTERVAL (duration, default=5m, max=24h): Emit INFO-level polling status on this cadence. Set to `0` to suppress periodic fetch logs and only log on startup or when changes occur.

At INFO level the controller reports configuration polls at the configured interval and always emits change detections. DEBUG level retains per-cycle fetch chatter (including "no change" messages) to aid troubleshooting without spamming production logs.

Reconcilers & GC

- RECONCILE_PARALLEL (bool, default=false)
- RECONCILE_MAX (int, default=3)
- GC_GRACE_PERIOD (duration, default=0)
- GC_WORKERS (int, default=2)

Leader election

- ENABLE_LEADER_ELECTION (bool, default=false)
- LEASE_LOCK_NAME (string, default=pangolin-kube-controller-leader)
- LEASE_LOCK_NAMESPACE (string, default=TARGET_NAMESPACE)
- LEASE_DURATION (duration, default=30s)
- RENEW_DEADLINE (duration, default=20s)
- RETRY_PERIOD (duration, default=5s)

Metrics & debug

- METRICS_ADDR (string, default=:9090)
- DISABLE_LIVEZ (bool, default=false)
- ENABLE_PPROF (bool, default=false)
- DISABLE_PPROF (bool, default=false)

> Always mount secrets as files via Kubernetes Secrets; **never as environment variables in production**.

### **Usage Examples**

**Local:**

```bash
# Example: standalone local run (HTTP-only mode)
STANDALONE_HTTP_ONLY=true \
METRICS_ADDR=:9090 \
LOG_TRAEFIK_CONFIG=false \
./pangolin-kube-controller
```

```bash
# Example: in-cluster style (not starting reconcile here)
CONFIG_ENDPOINT="https://config.example.com" \
CONFIG_AUTH_HEADER="Bearer abc" \
FETCH_TIMEOUT=15s \
RECONCILE_PARALLEL=true RECONCILE_MAX=3 \
LOG_TRAEFIK_CONFIG=false MAX_CONFIG_LOG_BYTES=2048 \
./pangolin-kube-controller
```

**Kubernetes Deployment (env section):**

```yaml
env:
  - name: CONFIG_ENDPOINT
    value: "https://config.example.com"
  - name: FETCH_TIMEOUT
    value: "30s"
  - name: READ_ONLY
    value: "true"
  - name: METRICS_ADDR
    value: ":9090"
  - name: CONFIG_CLIENT_CERT_FILE
    value: "/etc/pki/tls/client.crt"
  - name: CONFIG_CLIENT_KEY_FILE
    value: "/etc/pki/tls/client.key"
```

> **Security Tip**: If using mTLS, mount cert/key files as Kubernetes Secrets, not env var blobs.

---

## **Reconcile Loop (Go‑style pseudocode)**

**`parseBody`**: Unmarshal and validate the remote JSON config.  
**`sleepWithBackoff`/`sleepWithContext`**: Sleep using context for cancellation/reactivity.

```go
for {
  select {
  case <-ctx.Done():
    return
  default:
  }

  var etag, body string
  var status int
  var err error

  if lastETag != "" {
    etag, status, body, err = fetchConditional(ctx, lastETag)
  } else {
    etag, status, body, err = fetchConditional(ctx, "")
  }
  if err != nil {
    handleError(err)
    sleepWithBackoff()
    continue
  }

  if status == http.StatusNotModified {
    time.Sleep(pollInterval)
    continue
  }

  traefikCfg, err := parseBody(body)
  if err != nil {
    handleError(err)
    sleepWithBackoff()
    continue
  }

  if err := reconcileAll(ctx, traefikCfg); err != nil {
    handleError(err)
    sleepWithBackoff()
    continue
  }

  if etag != "" {
    lastETag = etag
  } else {
    lastETag = computeSHA256(body)
  }

  observeSuccessMetrics()
  time.Sleep(pollInterval)
}
```

---

## **Fetch Behavior & Signature Handling**

- HTTP client uses pooling, timeout, TLS settings from env/config.
- If `CONFIG_AUTH_HEADER` is set, add to requests as `Authorization`. **Risk:** Avoid secrets in env vars if possible.
- If both `CONFIG_CLIENT_CERT_FILE` and `CONFIG_CLIENT_KEY_FILE` are present, enable mTLS using the mounted files (via Secret).
- If `CONFIG_CA_FILE` present, load as additional root CA.
- On `200`, prefer ETag, else SHA256 of body as signature.
- On `304 Not Modified`, skip parsing/reconcile.
- Treat weak ETags (`W/`): consider unchanged if body matches.
- **Avoid `CONFIG_TLS_SKIP_VERIFY` in production.**

### ETag + SHA256 signature algorithm (detailed)

The controller keeps two signatures to detect changes robustly:

- `lastETag` — the last ETag header value received from the server when present (tracked only when the server returns an ETag header).
- `lastHash` — SHA256(body) hex of the last successfully-processed body.

Decision rules used by the controller when fetching a new response:

1. Only send `If-None-Match` when `lastETag` was previously set from a header (do not send conditional header when only a body hash exists).
2. If server returns `304 Not Modified` — skip parsing and reconcile.
3. If server returns a strong ETag and it equals `lastETag` — skip (no change).
4. If server returns a weak ETag (`W/...`) — compute SHA256(body) and compare with `lastHash`; if equal, skip (no change). If different, treat as changed.
5. If server returns any ETag that changed (strong or weak) but the body SHA256 equals `lastHash`, treat as no-change and update `lastETag` to the new value without reconciling.
6. If no ETag header is present, rely on SHA256(body) vs `lastHash` to detect changes.

Notes:

- The controller only updates `lastETag`/`lastHash` after a successful parse and reconcile (or successful diff in read-only mode). This prevents advancing signatures on transient parse errors.

- This approach prevents unnecessary applies when the server changes ETag semantics but not the body.

---

## **Backoff Strategy**

- Use exponential backoff with full jitter:
  - base = POLL_INTERVAL
  - MAX_BACKOFF = e.g., 5 * base
  - consecutiveErrors increments on each error
  - wait = max(200ms, rand(0, min(MAX_BACKOFF, base * 2^(consecutiveErrors-1))))
- Reset `consecutiveErrors` on successful reconcile.
- Increment metric `pangolin_kube_controller_reconcile_errors_total` on each error.

---

## **Apply / Server-Side Apply (SSA)**

- Build [unstructured.Unstructured](https://pkg.go.dev/k8s.io/apimachinery/pkg/apis/meta/v1/unstructured) object with `apiVersion`, `kind`, metadata, spec.
- Every object, including new ones, is written with [SSA](https://kubernetes.io/docs/reference/using-api/server-side-apply/) (`types.ApplyPatchType`) under the stable `FieldManager: "pangolin-kube-controller"` with `force: true`. The controller is the only writer of the objects it manages, and a plain Create would leave fields owned by an `Update` manager that later applies cannot remove.
- Objects created by 0.1.0-alpha.1 (managers `controller` / `pangolin-kube-controller` with operation `Update`) are migrated to the apply manager with `csaupgrade` before the first apply.
- Services, EndpointSlices and Secrets are applied first, then the Traefik CRDs, so a route never references a backend that does not exist yet.

Example apply function:

```go
func applyResource(ctx context.Context, dynamicClient dynamic.Interface, gvr schema.GroupVersionResource, ns, name string, unstructuredObj *unstructured.Unstructured, readOnly bool) error {
  if readOnly {
    log.Infof("[READ-ONLY] would apply %s/%s", unstructuredObj.GetKind(), name)
    return nil
  }

  patch := marshalForApply(unstructuredObj)
  _, err := dynamicClient.Resource(gvr).Namespace(ns).Patch(ctx, name, types.ApplyPatchType, patch, metav1.PatchOptions{
    FieldManager: "pangolin-kube-controller",
  })
  return err
}
```

---

## **Garbage Collection (GC)**

For each resource kind:

1. Build desired set keyed by name (and optional stable key).
2. List existing objects in namespace labeled `app.kubernetes.io/managed-by=pangolin-kube-controller`.
3. For each existing object not in desired set: delete (or log if `READ_ONLY=true`) and increment `pangolin_kube_controller_objects_deleted_total{kind=...}`.
4. Perform apply loop (create/update) **before** GC to avoid downtime on renames: apply -> GC.
5. Process items deterministically (sort by name) to ensure stable behavior and logs.
6. Services, EndpointSlices and Secrets are collected once per reconcile across HTTP, TCP and UDP, so one protocol never deletes another's backends.
7. A Pangolin entry that cannot be converted is logged and skipped. Its existing object is kept, and Service/EndpointSlice/Secret GC is skipped for that reconcile, so one malformed entry never takes down routes that currently work.

### GC grace period and events

The controller supports an optional `GC_GRACE_PERIOD` duration. When set, stale objects are scheduled for deletion after the grace period instead of being deleted immediately. This provides an operator window to inspect or recover from transient upstream configuration problems.

- If `READ_ONLY=true`, GC runs in dry-run mode and only logs deletions.
- Deletions performed after the grace period are recorded in `pangolin_kube_controller_gc_deleted_total{reason="grace"}`. Immediate deletions use `reason="immediate"`.
- The controller emits Kubernetes Events for GC deletions when possible (best-effort).

---

## **Read‑Only Mode**

Set `READ_ONLY=true` to:

- Skip mutating API calls (create/patch/delete).
- Log instead:  
  `[READ-ONLY] would apply IngressRoute my-route`
- Still track ETag/SHA256 so future writes skip unchanged resources.
- Record duration/error metrics but skip mutation counters.

**Use Cases**:

- Pre‑deployment validation in CI/CD.
- Audit in production without touching resources.

---

## **Change Detection & Diffing**

To avoid unnecessary or noisy updates to Kubernetes objects, the controller uses **semantic diffing** with normalization before applying changes.

### Normalization & Comparison Strategy

- **Semantic comparison:** Compare resources logically, not by raw JSON strings.
- **Normalization steps:**
  - Sort maps (for order-independent comparison).
  - Strip server-defaulted fields.
  - Remove metadata fields that should not trigger updates (e.g., timestamps, UIDs, resourceVersion, managedFields).
  - Canonicalize numeric types to avoid type-related false positives.
- **Comparison tools:**
  - [`apiequality.Semantic.DeepEqual`](https://pkg.go.dev/k8s.io/apimachinery/pkg/api/equality#Semantic.DeepEqual) for Kubernetes-aware structural equality checks.
  - [`go-cmp`](https://pkg.go.dev/github.com/google/go-cmp/cmp) with appropriate canonicalization options for stable, reproducible diffs.

### Diff Logging

- **INFO level:** Log concise, structured summaries of changed fields.
- **DEBUG level:** Log full before/after payloads for deep troubleshooting.
- Logged diffs should be order-independent to avoid noise from serialization order changes.

### Practices to Avoid

- **No JSON string comparison:** String-based diffing is order-sensitive and will generate unnecessary "changes" when only serialization order varies.
- Avoid including high-cardinality data (names, UIDs) in metric labels to prevent metric cardinality issues.

> **Tip:** This semantic diffing process significantly reduces unnecessary patches, stabilizes reconciliation behavior, and improves observability when combined with structured logging.

---

## **Metrics (Prometheus)**

Keep labels low-cardinality; recommended sets:

- `kind`: `IngressRoute`, `Middleware`, `TraefikService`
- `action`: `create`, `patch`

| Metric                                                        | Type      | Description                              |
| ------------------------------------------------------------- | --------- | ---------------------------------------- |
| `pangolin_kube_controller_reconcile_seconds`                  | Histogram | Duration of reconcile loop               |
| `pangolin_kube_controller_reconcile_errors_total`             | Counter   | Number of reconcile errors               |
| `pangolin_kube_controller_objects_applied_total{kind,action}` | Counter   | Count of created/patched resources       |
| `pangolin_kube_controller_objects_deleted_total{kind}`        | Counter   | Count of deleted resources               |
| `pangolin_kube_controller_leader`                             | Gauge     | 1 if leader                              |
| `pangolin_kube_controller_ready`                              | Gauge     | 1 if ready                               |
| `up`                                                          | Gauge     | Exporter reachability                    |
| `pangolin_kube_controller_build_info{version,git_sha}`        | Gauge     | Always 1; with build metadata            |

Additional metrics and observability:

- `pangolin_kube_controller_consecutive_errors` (gauge): number of consecutive fetch/reconcile errors.
- `pangolin_kube_controller_last_fetch_success_timestamp_seconds` (gauge): unix timestamp of last successful fetch+reconcile.
- `pangolin_kube_controller_desired_objects_count{kind}` (gauge): desired objects per kind observed in last config.
- `pangolin_kube_controller_gc_deleted_total{kind,reason}` (counter): GC deletions annotated by reason (e.g., "immediate", "grace").
- `pangolin_kube_controller_gc_runs_total{result}` (counter): GC run results (start/success/fail/dryrun).

### Metrics (OpenTelemetry)

The controller also exposes additional metrics via the OpenTelemetry Prometheus exporter on the same `/metrics` endpoint. These series are safe to scrape with Prometheus and complement the legacy metrics above.

- `pangolin_controller_reconcile_phase_duration_seconds` (Histogram, unit: s)
  - Duration of each reconcile phase.
  - Labels:
    - `phase`: `middlewares` | `routers` | `serversTransports` | `services` | `tcp` | `udp`
    - `result`: `success` | `error`

- `pangolin_controller_fetch_duration_seconds` (Histogram, unit: s)
  - Duration of remote fetch cycle HTTP requests.
  - Labels:
    - `status_code`: e.g., `"200"`, `"304"`, `"401"`, `"403"`, `"404"`, `"5xx"`
    - `status_class`: `2xx` | `3xx` | `4xx` | `5xx`

- `pangolin_controller_k8s_request_duration_seconds` (Histogram, unit: s)
  - Duration of Kubernetes API requests.
  - Labels:
    - `verb`: `get` | `create` | `patch` | `update` | `delete` | `list`
    - `resource_kind`: low-cardinality kind (e.g., `IngressRoute`, `Middleware`, `TraefikService`, `ServersTransport`, `ServersTransportTCP`, `Service`, `EndpointSlice`)
    - `result`: `success` | `error` | `conflict`
    - `forced`: `true` | `false`

- `pangolin_controller_k8s_requests_total` (Counter)
  - Total Kubernetes API requests.
  - Labels: same as `pangolin_controller_k8s_request_duration_seconds`

- `pangolin_controller_retries_total` (Counter)
  - Total retry attempts by reason in the SSA apply loop.
  - Labels:
    - `reason`: `conflict` | `transient` | `timeout`
    - `operation`: `get` | `create` | `patch` | `delete` | `apply`
    - `resource_kind`: Kubernetes kind

- `pangolin_controller_active_reconcile_routines` (UpDownCounter)
  - Number of active reconcile routines by phase (parallel mode).
  - Labels:
    - `phase`: `middlewares` | `routers` | `serversTransports` | `services` | `tcp` | `udp`

- `pangolin_controller_gc_run_duration_seconds` (Histogram, unit: s)
  - Duration of GC runs.
  - Labels:
    - `result`: `success` | `fail` | `dryrun`

- `pangolin_controller_config_parse_duration_seconds` (Histogram, unit: s)
  - Duration of configuration parsing.
  - Labels:
    - `section`: `full`

- `pangolin_controller_loop_iterations_total` (Counter)
  - Number of controller loop iterations by outcome.
  - Labels:
    - `outcome`: `success` | `nochange` | `error`

---

## Health & Probes

| Endpoint        | Purpose   | Description                                                                             |
| --------------- | --------- | --------------------------------------------------------------------------------------- |
| `/healthz`      | Readiness | 200 after one successful reconcile and if recent (`< 5 * POLL_INTERVAL`), else 503      |
| `/readyz`       | Readiness | Alias to `/healthz`                                                                     |
| `/livez`        | Liveness  | 200 if server/process is up; can disable with `DISABLE_LIVEZ=true`                      |
| `/metrics`      | Metrics   | Prometheus endpoint                                                                     |
| `/debug/pprof/` | Profiling | Available if `ENABLE_PPROF=true` and not disabled by `DISABLE_PPROF`                    |

> **Adapt readiness/liveness probe delays and timeouts to `POLL_INTERVAL` for reliability.**

---

## **Leader Election**

- If enabled, only the elected leader executes reconciliation.
- On losing leadership, default behavior is to log and exit (OnStoppedLeading → warn + exit). Alternatively, you can pause reconciliation until leadership is regained.
- Requires RBAC for `coordination.k8s.io/leases`.

### `--on-lose` / `ON_LOSE` behavior

You can control controller behavior when leadership is lost via the `ON_LOSE` environment variable (or CLI flag `--on-lose`) with the following values:

- `exit` (default): log a warning and exit the process. This is suitable when using a Pod restart to re-elect.
- `pause`: stop reconciling but keep the process alive; useful for graceful handovers and rolling updates where automatic restarts are not desired.

If `pause` is used, the controller will stop executing the main reconcile loop while still serving metrics and health endpoints.

---

## **RBAC Example**

Adjust scope (Role vs ClusterRole) and namespace bindings as needed.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: pangolin-kube-controller
rules:
  - apiGroups: ["traefik.io"]
    resources: ["ingressroutes", "ingressroutetcps", "ingressrouteudps", "middlewares", "traefikservices", "serverstransports", "serverstransporttcps", "tlsoptions"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: [""]
    resources: ["services", "secrets"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["discovery.k8s.io"]
    resources: ["endpointslices"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["networking.k8s.io"]
    resources: ["ingressclasses"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "list", "watch", "create", "update", "patch"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch", "get", "list"]
```

---

## **Service + ServiceMonitor Example (Prometheus Operator)**

```yaml
apiVersion: v1
kind: Service
metadata:
  name: pangolin-kube-controller
  labels:
    app: pangolin-kube-controller
spec:
  selector:
    app: pangolin-kube-controller
  ports:
    - name: http-metrics
      port: 9090
      targetPort: 9090
---
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: pangolin-kube-controller
spec:
  selector:
    matchLabels:
      app: pangolin-kube-controller
  endpoints:
    - port: http-metrics
      path: /metrics
      interval: 30s
```

> If pprof is enabled and should not be scraped, use relabeling/metric_relabel_configs in Prometheus to restrict.

---

## **Extension Ideas**

- Add support for more Traefik CRDs: `TLSOptions`, `ServerTransport`, `MiddlewareChain`.
- Report CRD status conditions or emit Kubernetes Events.
- ConfigMap‑based configuration with hot reload.
- Additional error metrics by CRD kind/action and step-level latency histograms.

- Webhook/streaming updates to reduce polling (Pangolin SourceCode changes needed).

---

## **Security Best Practices**

- Use namespace-scoped RBAC when possible.
- Run as non-root with least privilege ServiceAccount.
- Mount certificates/keys/CA as files via Secrets; never as env vars.
- Avoid `CONFIG_TLS_SKIP_VERIFY` in production.
- Keep metric labels low cardinality (avoid object names, namespaces, UIDs).
- Prefer SSA for safe field ownership.

---

## **Implementation Notes & Best Practices**

- Default to SSA with `FieldManager: "pangolin-kube-controller"`. Use `force` sparingly.
- Normalize and sort resources before comparison for stable diffs.
- Apply then GC (create/patch first, delete later) to handle renames without traffic loss.
- Ensure `FETCH_TIMEOUT <= POLL_INTERVAL` to avoid overlapping fetches.
- Use structured JSON logging for production; log diffs at INFO, full payloads at DEBUG.

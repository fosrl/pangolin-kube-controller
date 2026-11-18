package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	logrus "github.com/sirupsen/logrus"

	obslog "pangolin-kube-controller/internal/observability/logging"
	otelmetrics "pangolin-kube-controller/internal/observability/metrics_otel"
	traefikconfig "pangolin-kube-controller/internal/transform/config"

	"go.opentelemetry.io/otel/metric"
)

func (c *Controller) runLoop(ctx context.Context) {
	var lastETag, lastHash string
	var lastETagIsHeader bool
	var consecutiveErrors int
	var lastFetchInfoLog time.Time
	var lastApplied time.Time
	for {
		if ctx.Err() != nil {
			logrus.Info("Context cancelled, shutting down main loop.")
			return
		}
		start := time.Now()
		if c.resyncDue(start, lastApplied) {
			// Forget what was applied so this pass fetches and applies the
			// full config even if it is unchanged.
			logrus.Debugf("Resync interval %s elapsed; forcing a full apply", c.cfg.ResyncInterval)
			lastETag, lastHash, lastETagIsHeader = "", "", false
		}
		logAtInfo := c.shouldLogFetchInfo(start, lastFetchInfoLog)
		lastFetchInfoLog = c.logPollingStart(logAtInfo, start, lastFetchInfoLog)

		etagHeader, body, status, err := c.fetchConfigOnce(ctx, c.computeIfNoneMatch(lastETagIsHeader, lastETag))
		if err != nil {
			c.handleReconcileError("fetch", err, &consecutiveErrors)
			c.sleepWithBackoff(ctx, consecutiveErrors)
			continue
		}

		cfgBody, etagHeaderAdj, proceed, ok, hash := c.shouldProceedAfterStatus(body, etagHeader, status, lastETag, lastHash, lastETagIsHeader)
		c.logFetchDecision(ok, proceed, logAtInfo, etagHeaderAdj, hash, len(cfgBody))
		if c.handleFetchOutcome(ctx, ok, proceed, &consecutiveErrors) {
			continue
		}

		c.resetConsecutiveErrors(&consecutiveErrors)
		c.logFetchedBodyIfEnabled(cfgBody, etagHeaderAdj)
		if !c.processAndApply(ctx, cfgBody, &consecutiveErrors) {
			continue
		}

		c.updateSignaturesAfterSuccess(etagHeaderAdj, cfgBody, &lastETag, &lastETagIsHeader, &lastHash)
		lastApplied = time.Now()
		c.recordSyncSuccess(start)
		c.sleepWithBackoff(ctx, 0)
	}
}

func (c *Controller) logPollingStart(info bool, start time.Time, last time.Time) time.Time {
	if info {
		logrus.Infof("Polling Traefik dynamic config from %s (pollInterval=%s)", c.cfg.Endpoint, c.cfg.PollInterval)
		return start
	}
	logrus.Debugf("Polling Traefik dynamic config from %s", c.cfg.Endpoint)
	return last
}

func (c *Controller) logFetchDecision(ok, proceed, info bool, etag string, hash string, payloadLen int) {
	if !ok {
		return
	}
	if proceed {
		logrus.Infof("Detected Traefik config update (etag=%s sha256=%s); reconciling", c.orNone(etag), hash)
		logrus.Debugf("Traefik config update payload bytes=%d", payloadLen)
		return
	}
	if info {
		logrus.Infof("Traefik config unchanged (etag=%s)", c.orNone(etag))
	} else {
		logrus.Debugf("Traefik config unchanged (etag=%s)", c.orNone(etag))
	}
}

func (c *Controller) resetConsecutiveErrors(consecutiveErrors *int) {
	*consecutiveErrors = 0
	if c.collector != nil {
		c.collector.ConsecutiveErrors.Set(0)
	}
}

func (c *Controller) processAndApply(ctx context.Context, body []byte, consecutiveErrors *int) bool {
	pstart := time.Now()
	cfg, err := c.parseTraefikConfig(body)
	if c.collector != nil && c.collector.OTel != nil {
		c.collector.OTel.ConfigParseDuration.Record(ctx, time.Since(pstart).Seconds(),
			metric.WithAttributes(otelmetrics.AttrSection.String("full")),
		)
	}
	if err != nil {
		c.handleReconcileError("json decode", err, consecutiveErrors)
		c.sleepWithBackoff(ctx, *consecutiveErrors)
		return false
	}
	if err := c.applyConfig(ctx, cfg); err != nil {
		c.handleReconcileError("reconcile", err, consecutiveErrors)
		c.sleepWithBackoff(ctx, *consecutiveErrors)
		return false
	}
	return true
}

func (c *Controller) recordSyncSuccess(start time.Time) {
	c.lastSuccessfulReconcile.Store(time.Now().UnixNano())
	elapsed := time.Since(start)
	if c.collector != nil {
		c.collector.LastFetchSuccess.Set(float64(time.Now().Unix()))
		c.setReady(1)
		c.collector.ReconcileDuration.Observe(elapsed.Seconds())
		if c.collector.OTel != nil {
			c.collector.OTel.LoopIterationsTotal.Add(context.Background(), 1,
				metric.WithAttributes(otelmetrics.AttrOutcome.String("success")),
			)
		}
	}
	logrus.Infof("Sync complete in %s", elapsed)
}

func (c *Controller) shouldLogFetchInfo(now, last time.Time) bool {
	interval := c.cfg.FetchLogInterval
	if interval <= 0 {
		return last.IsZero()
	}
	if last.IsZero() {
		return true
	}
	return now.Sub(last) >= interval
}

func (*Controller) orNone(value string) string {
	if value == "" {
		return "<none>"
	}
	return value
}

func (*Controller) computeIfNoneMatch(lastETagIsHeader bool, lastETag string) string {
	if lastETagIsHeader && lastETag != "" {
		return lastETag
	}
	return ""
}

func (*Controller) parseTraefikConfig(body []byte) (*traefikconfig.Config, error) {
	var cfg traefikconfig.Config
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Controller) handleReconcileError(stage string, err error, consecutiveErrors *int) {
	logrus.Errorf("%s error: %v", stage, err)
	(*consecutiveErrors)++
	if c.collector != nil {
		c.collector.ReconcileErrors.Inc()
		c.collector.ConsecutiveErrors.Set(float64(*consecutiveErrors))
		if c.collector.OTel != nil {
			c.collector.OTel.LoopIterationsTotal.Add(context.Background(), 1,
				metric.WithAttributes(otelmetrics.AttrOutcome.String("error")),
			)
		}
	}
}

func (c *Controller) handleFetchOutcome(ctx context.Context, ok bool, proceed bool, consecutiveErrors *int) bool {
	if !ok {
		(*consecutiveErrors)++
		if c.collector != nil {
			c.collector.ConsecutiveErrors.Set(float64(*consecutiveErrors))
			c.collector.ReconcileErrors.Inc()
		}
		if c.collector != nil && c.collector.OTel != nil {
			c.collector.OTel.LoopIterationsTotal.Add(ctx, 1,
				metric.WithAttributes(otelmetrics.AttrOutcome.String("error")),
			)
		}
		c.sleepWithBackoff(ctx, *consecutiveErrors)
		return true
	}
	if !proceed {
		*consecutiveErrors = 0
		c.lastSuccessfulReconcile.Store(time.Now().UnixNano())
		if c.collector != nil {
			c.collector.ConsecutiveErrors.Set(0)
			c.collector.LastFetchSuccess.Set(float64(time.Now().Unix()))
			c.setReady(1)
		}
		if c.collector != nil && c.collector.OTel != nil {
			c.collector.OTel.LoopIterationsTotal.Add(ctx, 1,
				metric.WithAttributes(otelmetrics.AttrOutcome.String("nochange")),
			)
		}
		c.sleepWithBackoff(ctx, 0)
		return true
	}
	return false
}

func (c *Controller) logFetchedBodyIfEnabled(body []byte, etag string) {
	if !c.cfg.ShouldLogConfigPreview() {
		return
	}
	logger := logrus.StandardLogger()
	if c.logger != nil {
		logger = c.logger
	}
	sha := c.computeHash(body)
	limit := c.cfg.MaxConfigLogBytes
	var src []byte
	if len(body) > 0 {
		if red, err := obslog.RedactJSONLike(body); err == nil {
			src = red
		} else {
			logger.Debugf("Failed to redact JSON: %v", err)
			// Do NOT fall back to the original body on redaction failure to avoid
			// potentially logging secrets. Leave `src` empty so we only log the summary.
			src = nil
		}
	}
	if limit <= 0 || len(src) == 0 {
		logger.Infof("Fetched config summary len=%d sha256=%s etag=%s", len(body), sha, etag)
		return
	}
	if limit > len(src) {
		limit = len(src)
	}
	preview := string(src[:limit])
	if limit < len(src) {
		preview += "...<truncated>"
	}
	logger.Infof("Fetched config summary len=%d sha256=%s etag=%s %s%q", len(body), sha, etag, PreviewLogFieldPrefix, preview)
}

func (c *Controller) shouldProceedAfterStatus(body []byte, etagHeader string, status int, lastETag string, lastHash string, lastETagIsHeader bool) ([]byte, string, bool, bool, string) {
	hash := lastHash
	switch status {
	case http.StatusNotModified:
		return nil, etagHeader, false, true, hash
	case http.StatusNotFound:
		body = []byte("{}")
		etagHeader = ""
	case http.StatusUnauthorized, http.StatusForbidden:
		logrus.Errorf("fetch status %d: auth error", status)
		return nil, "", false, false, hash
	default:
		if status >= 500 {
			logrus.Errorf("fetch status %d: server error", status)
			return nil, "", false, false, hash
		}
		if status != http.StatusOK {
			logrus.Errorf("fetch status %d: unexpected status", status)
			return nil, "", false, false, hash
		}
	}
	hash = c.computeHash(body)
	if !c.decideChange(etagHeader, lastETag, lastHash, body, lastETagIsHeader) {
		return nil, etagHeader, false, true, hash
	}
	return body, etagHeader, true, true, hash
}

func (c *Controller) updateSignaturesAfterSuccess(etag string, body []byte, lastETag *string, lastETagIsHeader *bool, lastHash *string) {
	if etag != "" {
		*lastETag = etag
		*lastETagIsHeader = true
	} else {
		*lastETag = ""
		*lastETagIsHeader = false
	}
	*lastHash = c.computeHash(body)
}

// resyncDue reports whether the resync interval has elapsed since the last
// successful apply.
func (c *Controller) resyncDue(now, lastApplied time.Time) bool {
	if c.cfg.ResyncInterval <= 0 || lastApplied.IsZero() {
		return false
	}
	return now.Sub(lastApplied) >= c.cfg.ResyncInterval
}

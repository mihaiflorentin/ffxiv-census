# Metrics & Observability

`ffxiv-census` provides dual-stack observability utilizing **Prometheus** (pull-based `/metrics` endpoint via Kubernetes `ServiceMonitor`) and **StatsD / Graphite** (push-based UDP telemetry).

---

## Prometheus Metrics

Prometheus metrics are exposed at `http://<host>:8080/metrics` and scraped automatically by Prometheus Operator via the Helm chart's `ServiceMonitor`.

### Core Metrics Reference

| Metric Name | Type | Description / Labels |
|---|---|---|
| `http_requests_total` | Counter | Total processed HTTP requests (`method`, `path`, `status`). |
| `http_request_duration_seconds` | Histogram | Request latency distribution (`method`, `path`, `le`). |
| `process_uptime_seconds` | Gauge | Total process uptime in seconds. |
| `sqlite_open_connections` | Gauge | Established open database connections in the pool. |
| `sqlite_in_use_connections` | Gauge | Database connections currently in use. |
| `sqlite_idle_connections` | Gauge | Idle database connections in the pool. |
| `queue_jobs_depth` | Gauge | Current count of pending/claimed queue jobs. |
| `ui_stats_cache_total` | Counter | Snapshot cache outcomes (`result`: `hit`, `reload`, `stale_served`, or `error`). |
| `ui_stats_refresh_total` | Counter | Snapshot refresh outcomes (`result`: `success`, `skipped`, or `error`). |
| `ui_stats_refresh_duration_seconds` | Histogram | End-to-end snapshot refresh duration. |
| `ui_stats_snapshot_age_seconds` | Gauge | Age of the snapshot most recently served by this process. |
| `ui_stats_payload_bytes` | Gauge | Serialized size of the snapshot most recently loaded or refreshed by this process. |
| `ui_stats_last_refresh_duration_seconds` | Gauge | Refresh duration stored in the snapshot currently served by this process. |
| `result_writer_batch_size` | Histogram | Results stored per batching-writer flush. |
| `result_writer_flush_duration_seconds` | Histogram | Duration of one batch transaction. |
| `result_writer_flush_failures_total` | Counter | Batch flushes that failed to commit (their jobs stay unacked and are retried). |

The `result_writer_*` metrics observe every batch transaction attempt, including the extra attempts made while isolating a result the database rejected, so `result_writer_flush_failures_total` can rise by more than one for a single bad character. Batch size is counted in results (characters plus achievement updates). Tune `[writer] batch_size` and `flush_interval` (`WRITER_BATCH_SIZE`, `WRITER_FLUSH_INTERVAL`) against these; see [docs/queue.md](queue.md#batched-writes-and-acks).

`ui_stats_refresh_total` and `ui_stats_refresh_duration_seconds` are emitted by the process that executes a refresh. In the default short-lived Kubernetes CronJob, the completion log is the durable refresh outcome; the web Grafana dashboard therefore uses snapshot age, last refresh duration, and `ui_stats_cache_total`, which are continuously scrapeable from web pods.

---

## StatsD / Graphite Metrics

StatsD integration sends UDP timing metrics to Graphite/StatsD. Configure the endpoint in `config/config.toml` or environment variables (`METRICS_ENDPOINT`):

```toml
[metrics]
endpoint = "graphite.monitoring.svc.cluster.local:8125"
prefix   = "ffxiv-census"
```

### Emitted Telemetry
- `ffxiv-census.http.<route>` (e.g. `ffxiv-census.http.ui_dashboard`, `ffxiv-census.http.api_v1_census_latest`, `ffxiv-census.http.api_v1_queue`): Request duration timing in milliseconds.

---

## Grafana Dashboards

Ready-to-import Grafana dashboard exports are maintained in the [`dashboards/`](../dashboards/) directory:

1. **System Overview**: [`dashboards/ffxiv-census-overview.json`](../dashboards/ffxiv-census-overview.json) (`uid: ffxiv-census-overview`)
2. **Web Application & UI**: [`dashboards/ffxiv-census-webapp.json`](../dashboards/ffxiv-census-webapp.json) (`uid: ffxiv-census-webapp`)
3. **REST APIs & Swagger**: [`dashboards/ffxiv-census-api.json`](../dashboards/ffxiv-census-api.json) (`uid: ffxiv-census-api`)
4. **Workers, Queue & CronJobs**: [`dashboards/ffxiv-census-workers.json`](../dashboards/ffxiv-census-workers.json) (`uid: ffxiv-census-workers`)

See [`dashboards/README.md`](../dashboards/README.md) for detailed import guides.

# Local verification — 2026-10-27

This report records local verification before the initial source publication. Current remote checks are listed separately in [GitHub Actions](https://github.com/Singmin/GorunTrace/actions); local results do not establish remote CI, a tagged release or a hosted deployment.

## Verified

| Area | Evidence |
| --- | --- |
| Go build | Windows server and MCP binaries compile; Linux binaries compile in Docker with CGO disabled |
| Static checks | `go vet ./...` passes |
| Automated tests | SDK, event validation, SQLite, HTTP and MCP integration tests pass |
| Race detection | Full backend suite passes in a Linux container; final SDK/event changes separately pass `go test -race` through read-only source mounts |
| SDK semantics | Original errors and panic identity; success/failure/timeout/cancel states; redaction; failing error filters/string methods; bounded Close; nonblocking queue overflow; stable retry IDs; concurrent Trace/Close accounting |
| SQLite | Persistence after reopen, duplicate IDs, terminal-before-start, immutable identities/outcomes, atomic rollback, filtered pagination and exact metrics |
| HTTP | Real SDK → authenticated HTTP → SQLite; malformed input, invalid pagination/time, missing runs and unauthenticated reads |
| MCP protocol | Official SDK client/server integration for all five tools; real executable stdio initialize, tools/list, get_failed_runs and get_run_details |
| Frontend | `vue-tsc --noEmit` and Vite production build pass locally and inside Docker |
| Browser | Overview, task directory → filtered history, failed runs, timed-out run details with two raw events, status filter, pagination, MCP setup and 390px responsive layout inspected |
| Windows launcher | `scripts/dev.ps1 -Seed -Port 8081` builds, starts collector, sends 48 events with zero drops, and stops its collector on Ctrl+C |
| Docker image | `docker build -t goruntrace:local .` passes |
| Docker runtime | Temporary loopback-bound container serves Dashboard (200), rejects unauthenticated API (401), ingests six runs/twelve events with zero drops, preserves six runs across restart, runs as UID 10001; temporary container removed |

The Windows example and launcher each generated 24 observed executions of simulated business functions. The retained local database therefore contains **48 runs**: 24 successes and 24 failures/timeouts/panics. These are example workloads, not production task records. The screenshot [Dashboard](dashboard.jpg) shows these real observations. The Docker smoke test used an independent temporary database.

The real stdio check returned five read-only tools, 24 failed runs and a matching source run with two raw events. Reproduce after building the MCP executable and starting the collector:

```sh
node scripts/mcp-smoke.mjs
```

## Coverage

Package statement coverage from local Go tests (not a proof of complete correctness):

| Package | Coverage |
| --- | ---: |
| SDK | 89.9% |
| Event validation | 83.3% |
| MCP server | 85.4% |
| Storage | 63.5% |
| HTTP API | 60.3% |

CLI entrypoints and the example do not have dedicated unit tests; they were exercised through local builds, launcher/stdio checks and Docker runtime checks.

## Small baseline benchmarks

Windows amd64, AMD Ryzen 7 5800H, Go 1.27.3, default GOMAXPROCS (16), `-benchtime=200ms -count=1 -benchmem`:

| Benchmark | Measured sample | Allocation sample |
| --- | --- | --- |
| `BenchmarkTrace` | 508.6 ns/op | 147 B/op, 3 allocs/op |
| `BenchmarkIngest` | 232,368 ns/op | 9,545 B/op, 236 allocs/op |

`BenchmarkTrace` wraps a no-op and uses a bounded queue with an unavailable endpoint. The queue saturates; most events are deliberately dropped. This measures a best-effort instrumentation/drop path, **not successful event delivery or network throughput**. `BenchmarkIngest` inserts one start/terminal pair per transaction into **in-memory** SQLite. It does not measure disk durability, HTTP throughput or production tail latency. These short, single-sample runs are baselines for reproducibility and carry no performance-improvement claim.

## Remaining release work

- Sustained HTTP ingestion/load tests, disk-backed durability/throughput benchmarks and broader malformed-input coverage.
- Database schema upgrades, retention controls, task-framework adapters and operational metrics.
- Production access controls, TLS termination and deployment-specific review if remote operation is needed.
- A walkthrough recording/GIF, tagged release and verification of remote CI results.

The verified result is a local MVP. No external AI client settings were changed and no production tasks were run. Source publication and remote CI are tracked separately from this local report.

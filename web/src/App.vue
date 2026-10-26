<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
type Run = {
  run_id: string;
  task_id: string;
  service: string;
  task_name: string;
  status: string;
  attempt: number;
  started_at: string;
  ended_at: string | null;
  duration_ms: number;
  error_summary: string;
};
type Task = {
  id: string;
  service: string;
  name: string;
  runs: number;
  failures: number;
  last_seen: string;
};
type Event = {
  event_id: string;
  type: string;
  occurred_at: string;
  error_summary?: string;
};
type Page<T> = { items: T[]; total: number; limit: number; offset: number };
type Summary = {
  total: number;
  succeeded: number;
  failed: number;
  running: number;
  canceled: number;
  success_rate: number;
  avg_duration_ms: number;
  p95_duration_ms: number;
  buckets: { hour: string; runs: number; failures: number }[];
  errors: { summary: string; count: number; example_run_id: string }[];
};
const nav = [
  { id: "overview", label: "Overview", icon: "◫" },
  { id: "tasks", label: "Tasks", icon: "▦" },
  { id: "runs", label: "Task runs", icon: "≡" },
  { id: "errors", label: "Errors", icon: "!" },
  { id: "mcp", label: "MCP tools", icon: "⌘" },
  { id: "settings", label: "Settings", icon: "⚙" },
];
const tab = ref(
  nav.some((n) => n.id === location.hash.slice(1))
    ? location.hash.slice(1)
    : "overview",
);
const service = ref(""),
  hours = ref("24"),
  task = ref(""),
  status = ref(""),
  offset = ref(0),
  token = ref(""),
  draftToken = ref(""),
  auto = ref(true);
const loading = ref(false),
  error = ref(""),
  connected = ref(false),
  updated = ref<Date | null>(null),
  copied = ref(false);
const summary = ref<Summary | null>(null),
  runs = ref<Page<Run>>({ items: [], total: 0, limit: 10, offset: 0 }),
  tasks = ref<Page<Task>>({ items: [], total: 0, limit: 10, offset: 0 }),
  services = ref<string[]>([]);
const detail = ref<{ run: Run; events: Event[] } | null>(null),
  detailError = ref(""),
  detailLoading = ref(false),
  detailOpen = ref(false),
  dialog = ref<HTMLDialogElement | null>(null);
let controller: AbortController | undefined,
  detailController: AbortController | undefined,
  timer: ReturnType<typeof setInterval>,
  debounce: ReturnType<typeof setTimeout>,
  previousFocus: HTMLElement | null = null;
const titles: Record<string, string> = {
  overview: "Task overview",
  tasks: "Task directory",
  runs: "Task runs",
  errors: "Error explorer",
  mcp: "Connect your AI tools",
  settings: "Workspace settings",
};
const title = computed(() => titles[tab.value]);
const apiOrigin = location.origin;
const total = computed(() =>
  tab.value === "tasks" ? tasks.value.total : runs.value.total,
);
const duration = (ms: number) =>
  ms >= 1000 ? `${(ms / 1000).toFixed(2)} s` : `${Math.round(ms)} ms`;
const date = (v: string) =>
  new Date(v).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
const label = (s: string) =>
  ({
    started: "Running",
    succeeded: "Success",
    failed: "Failed",
    timed_out: "Timed out",
    canceled: "Canceled",
    panicked: "Panicked",
  })[s] || s;
const chart = computed(() => {
  const end = updated.value?.getTime() || Date.now(),
    count = Math.min(Number(hours.value) + 1, 73),
    start = Math.floor(end / 3600000) * 3600000 - (count - 1) * 3600000;
  const buckets = new Map(
    summary.value?.buckets.map((b) => [new Date(b.hour).getTime(), b]) || [],
  );
  const data = Array.from({ length: count }, (_, i) => {
    const time = start + i * 3600000;
    return { time, ...(buckets.get(time) || { runs: 0, failures: 0 }) };
  });
  const max = Math.max(1, ...data.map((b) => b.runs));
  return data.map((b) => ({
    ...b,
    height: Math.max(b.runs ? 3 : 0, (b.runs / max) * 100),
    failedHeight: b.runs ? (b.failures / b.runs) * 100 : 0,
  }));
});
const mcpConfig =
  '[mcp_servers.goruntrace]\ncommand = "/absolute/path/to/goruntrace-mcp"\nargs = ["-endpoint", "http://127.0.0.1:8080"]\n# If enabled, pass GORUNTRACE_TOKEN through your MCP client environment.';
async function api<T>(path: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch("/api/v1/" + path, {
    signal,
    headers: token.value ? { Authorization: "Bearer " + token.value } : {},
  });
  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    throw new Error(body.error || `HTTP ${response.status}`);
  }
  return response.json();
}
async function refresh() {
  controller?.abort();
  const current = new AbortController();
  controller = current;
  loading.value = true;
  error.value = "";
  const to = new Date(),
    from = new Date(to.getTime() - Number(hours.value) * 3600000);
  const common = new URLSearchParams({
    from: from.toISOString(),
    to: to.toISOString(),
  });
  if (service.value) common.set("service", service.value);
  if (task.value.trim()) common.set("task_name", task.value.trim());
  const runParams = new URLSearchParams(common);
  runParams.set("limit", "10");
  runParams.set("offset", String(offset.value));
  if (status.value) runParams.set("status", status.value);
  if (tab.value === "errors") runParams.set("failures_only", "true");
  const taskParams = new URLSearchParams(common);
  taskParams.set("limit", "10");
  taskParams.set("offset", String(tab.value === "tasks" ? offset.value : 0));
  const serviceParams = new URLSearchParams({
    from: from.toISOString(),
    to: to.toISOString(),
    limit: "200",
  });
  try {
    const [s, r, t, sv] = await Promise.all([
      api<Summary>("metrics/summary?" + common, current.signal),
      api<Page<Run>>("runs?" + runParams, current.signal),
      api<Page<Task>>("tasks?" + taskParams, current.signal),
      api<Page<string>>("services?" + serviceParams, current.signal),
    ]);
    if (current.signal.aborted) return;
    summary.value = s;
    runs.value = r;
    tasks.value = t;
    services.value = sv.items;
    connected.value = true;
    updated.value = to;
  } catch (e) {
    if (current.signal.aborted) return;
    connected.value = false;
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    if (!current.signal.aborted) loading.value = false;
  }
}
function navigate(id: string) {
  tab.value = id;
  location.hash = id;
  offset.value = 0;
  status.value = "";
}
function clear() {
  service.value = "";
  task.value = "";
  status.value = "";
  offset.value = 0;
}
function selectTask(t: Task) {
  service.value = t.service;
  task.value = t.name;
  navigate("runs");
}
async function openRun(id: string) {
  previousFocus = document.activeElement as HTMLElement;
  detailController?.abort();
  const current = new AbortController();
  detailController = current;
  detailOpen.value = true;
  detail.value = null;
  detailError.value = "";
  detailLoading.value = true;
  try {
    detail.value = await api("runs/" + encodeURIComponent(id), current.signal);
  } catch (e) {
    if (!current.signal.aborted)
      detailError.value = e instanceof Error ? e.message : String(e);
  } finally {
    if (!current.signal.aborted) detailLoading.value = false;
  }
}
function closeDetail() {
  detailController?.abort();
  detailOpen.value = false;
  previousFocus?.focus();
}
watch(detailOpen, async (open) => {
  if (open) {
    await nextTick();
    dialog.value?.showModal();
  } else dialog.value?.close();
});
async function copyConfig() {
  try {
    await navigator.clipboard.writeText(mcpConfig);
    copied.value = true;
    setTimeout(() => (copied.value = false), 2000);
  } catch {
    copied.value = false;
  }
}
function connect() {
  token.value = draftToken.value;
  void refresh();
}
watch([service, hours, status, tab], () => {
  offset.value = 0;
  void refresh();
});
watch(task, () => {
  clearTimeout(debounce);
  debounce = setTimeout(() => {
    offset.value = 0;
    void refresh();
  }, 350);
});
watch(offset, () => void refresh());
function syncHash() {
  const id = location.hash.slice(1);
  if (nav.some((n) => n.id === id) && tab.value !== id) {
    tab.value = id;
    offset.value = 0;
    status.value = "";
  }
}
onMounted(() => {
  window.addEventListener("hashchange", syncHash);
  void refresh();
  timer = setInterval(() => {
    if (auto.value && !document.hidden && !loading.value) void refresh();
  }, 5000);
});
onUnmounted(() => {
  window.removeEventListener("hashchange", syncHash);
  clearInterval(timer);
  clearTimeout(debounce);
  controller?.abort();
  detailController?.abort();
});
</script>

<template>
  <div class="workspace">
    <aside class="sidebar">
      <a href="#overview" class="brand" @click.prevent="navigate('overview')"
        ><span class="brand-mark">↗</span>GorunTrace<span class="version"
          >α</span
        ></a
      >
      <div class="workspace-name">
        <span class="workspace-avatar">L</span>
        <div>Local workspace<small>Task observability</small></div>
        <span class="chevron">⌄</span>
      </div>
      <p class="nav-label">WORKSPACE</p>
      <nav aria-label="Main navigation">
        <button
          v-for="n in nav"
          :key="n.id"
          :class="{ active: tab === n.id }"
          :aria-current="tab === n.id ? 'page' : undefined"
          @click="navigate(n.id)"
        >
          <span class="nav-icon">{{ n.icon }}</span
          >{{ n.label
          }}<span
            v-if="n.id === 'errors' && summary?.failed"
            class="nav-count"
            >{{ summary.failed }}</span
          >
        </button>
      </nav>
      <div class="sidebar-bottom">
        <div class="observe-note">
          <span>↗</span>
          <p>Observe. Diagnose.<br /><strong>Understand every run.</strong></p>
        </div>
        <div class="server-indicator">
          <i :class="{ online: connected }"></i
          >{{ connected ? "Collector connected" : "Collector disconnected"
          }}<span>v0.1.0</span>
        </div>
      </div>
    </aside>
    <main>
      <div class="topbar">
        <div class="breadcrumb">
          Workspace <span>/</span>
          <strong>{{ nav.find((n) => n.id === tab)?.label }}</strong>
        </div>
        <div class="local-tag"><span></span>LOCAL ENVIRONMENT</div>
      </div>
      <div class="content">
        <header class="page-heading">
          <div>
            <p class="eyebrow">OBSERVE · DIAGNOSE · RECOVER</p>
            <h1>{{ title }}</h1>
            <p class="subtitle">
              {{
                tab === "mcp"
                  ? "Bring real task evidence into your AI workflow."
                  : tab === "settings"
                    ? "Manage the connection to your local collector."
                    : "A clear view of your background work, from start to finish."
              }}
            </p>
          </div>
          <button class="refresh" :disabled="loading" @click="refresh">
            <span :class="{ spinning: loading }">↻</span
            >{{ loading ? "Refreshing" : "Refresh" }}
          </button>
        </header>
        <div v-if="error" class="error-banner" role="alert">
          <strong>Unable to fetch task data.</strong> {{ error
          }}<button @click="navigate('settings')">Connection settings →</button>
        </div>
        <template v-if="!['mcp', 'settings'].includes(tab)">
          <section class="filters" aria-label="Filter task data">
            <div class="filter-left">
              <label class="select-wrap"
                ><span>▤</span
                ><select v-model="service" aria-label="Service">
                  <option value="">All services</option>
                  <option
                    v-if="service && !services.includes(service)"
                    :value="service"
                  >
                    {{ service }}
                  </option>
                  <option v-for="s in services" :key="s">{{ s }}</option>
                </select></label
              ><label class="select-wrap"
                ><span>◷</span
                ><select v-model="hours" aria-label="Time window">
                  <option value="1">Last hour</option>
                  <option value="24">Last 24 hours</option>
                  <option value="72">Last 3 days</option>
                </select></label
              >
            </div>
            <div class="freshness">
              <i :class="{ online: connected }"></i
              >{{
                updated
                  ? "Updated " +
                    updated.toLocaleTimeString([], {
                      hour: "2-digit",
                      minute: "2-digit",
                      second: "2-digit",
                    })
                  : "Waiting for collector"
              }}<label class="auto-label"
                ><input v-model="auto" type="checkbox" />Live</label
              >
            </div>
          </section>
          <section
            v-if="tab === 'overview'"
            class="metrics"
            aria-label="Task metrics"
          >
            <article class="metric">
              <div class="metric-title">
                Total runs<span class="metric-icon blue">↗</span>
              </div>
              <strong>{{ summary?.total.toLocaleString() ?? "—" }}</strong>
              <p>
                <span class="neutral-dot"></span
                >{{ summary?.running ?? 0 }} currently running
              </p>
            </article>
            <article class="metric">
              <div class="metric-title">
                Success rate<span class="metric-icon green">✓</span>
              </div>
              <strong>{{
                summary && summary.total - summary.running > 0
                  ? summary.success_rate.toFixed(1) + "%"
                  : "—"
              }}</strong>
              <p>Of completed runs</p>
            </article>
            <article class="metric">
              <div class="metric-title">
                Failed runs<span class="metric-icon red">!</span>
              </div>
              <strong>{{ summary?.failed.toLocaleString() ?? "—" }}</strong>
              <p>
                <button class="text-link danger" @click="navigate('errors')">
                  Inspect failures <span>→</span>
                </button>
              </p>
            </article>
            <article class="metric">
              <div class="metric-title">
                Avg. duration<span class="metric-icon purple">◷</span>
              </div>
              <strong>{{
                summary && summary.total - summary.running > 0
                  ? duration(summary.avg_duration_ms)
                  : "—"
              }}</strong>
              <p>
                p95
                <b>{{
                  summary && summary.total - summary.running > 0
                    ? duration(summary.p95_duration_ms)
                    : "—"
                }}</b>
              </p>
            </article>
          </section>
          <section v-if="tab === 'overview'" class="panel chart-panel">
            <div class="panel-heading">
              <div>
                <h2>Run activity</h2>
                <p>Executions per hour</p>
              </div>
              <div class="chart-legend">
                <span><i class="success-square"></i>Other runs</span
                ><span><i class="failure-square"></i>Failed</span>
              </div>
            </div>
            <div
              class="chart"
              role="img"
              :aria-label="`${summary?.total ?? 0} runs in the selected time window`"
            >
              <div class="chart-grid">
                <span></span><span></span><span></span>
              </div>
              <div class="bars">
                <div
                  v-for="(b, i) in chart"
                  :key="i"
                  class="bar-slot"
                  :title="`${date(new Date(b.time).toISOString())}: ${b.runs} runs, ${b.failures} failed`"
                >
                  <div class="bar" :style="{ height: b.height + '%' }">
                    <div :style="{ height: b.failedHeight + '%' }"></div>
                  </div>
                </div>
              </div>
              <span v-if="!summary?.total" class="chart-empty"
                >Your task activity will appear here</span
              >
            </div>
            <div class="chart-axis">
              <span>{{
                chart[0] ? date(new Date(chart[0].time).toISOString()) : ""
              }}</span
              ><span>Hourly · local time</span><span>Now</span>
            </div>
          </section>
          <section v-if="tab === 'tasks'" class="panel">
            <div class="panel-heading">
              <div>
                <h2>
                  Discovered tasks <span class="count">{{ tasks.total }}</span>
                </h2>
                <p>Tasks observed in the selected window</p>
              </div>
              <input
                v-model="task"
                class="search"
                placeholder="Exact task name…"
                aria-label="Exact task name"
              />
            </div>
            <div class="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>Task / service</th>
                    <th>Runs</th>
                    <th>Failures</th>
                    <th>Last observed</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="t in tasks.items" :key="t.id">
                    <td>
                      <button class="task-link" @click="selectTask(t)">
                        {{ t.name }}</button
                      ><small class="cell-sub">{{ t.service }}</small>
                    </td>
                    <td>{{ t.runs }}</td>
                    <td>
                      <span :class="{ failureText: t.failures }">{{
                        t.failures
                      }}</span>
                    </td>
                    <td class="mono muted">{{ date(t.last_seen) }}</td>
                    <td>
                      <button
                        class="row-action"
                        :aria-label="'View runs for ' + t.name"
                        @click="selectTask(t)"
                      >
                        →
                      </button>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div v-if="!tasks.items.length" class="empty">
              <span>▦</span>
              <h3>No tasks found</h3>
              <p>Connect a Go service or adjust your filters.</p>
              <button @click="clear">Clear filters</button>
            </div>
          </section>
          <section v-else class="panel runs-panel">
            <div class="panel-heading">
              <div>
                <h2>
                  {{
                    tab === "overview"
                      ? "Recent task runs"
                      : tab === "errors"
                        ? "Failed task runs"
                        : "Execution history"
                  }}
                  <span class="count">{{ runs.total }}</span>
                </h2>
                <p>
                  {{
                    tab === "errors"
                      ? "Failures, timeouts and panics · click a run to inspect the evidence"
                      : "Every execution has a story. Open a run to follow it."
                  }}
                </p>
              </div>
              <button
                v-if="tab === 'overview'"
                class="text-link"
                @click="navigate('runs')"
              >
                View all runs →
              </button>
            </div>
            <div v-if="tab !== 'overview'" class="table-toolbar">
              <input
                v-model="task"
                class="search"
                placeholder="Exact task name…"
                aria-label="Exact task name"
              /><select
                v-if="tab !== 'errors'"
                v-model="status"
                aria-label="Run status"
              >
                <option value="">All statuses</option>
                <option
                  v-for="s in [
                    'started',
                    'succeeded',
                    'failed',
                    'timed_out',
                    'canceled',
                    'panicked',
                  ]"
                  :key="s"
                  :value="s"
                >
                  {{ label(s) }}
                </option></select
              ><button
                v-if="task || status || service"
                class="text-link"
                @click="clear"
              >
                Clear filters
              </button>
            </div>
            <div class="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>Task / run ID</th>
                    <th>Service</th>
                    <th>Status</th>
                    <th>Duration</th>
                    <th>Started</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="r in runs.items" :key="r.run_id">
                    <td>
                      <button class="task-link" @click="openRun(r.run_id)">
                        {{ r.task_name }}</button
                      ><small class="cell-sub mono">{{
                        r.run_id.slice(0, 12)
                      }}</small>
                    </td>
                    <td class="service-cell">{{ r.service }}</td>
                    <td>
                      <span class="status" :class="r.status"
                        ><i></i>{{ label(r.status) }}</span
                      >
                    </td>
                    <td class="mono">
                      {{
                        r.status === "started" ? "—" : duration(r.duration_ms)
                      }}
                    </td>
                    <td class="mono muted nowrap">{{ date(r.started_at) }}</td>
                    <td>
                      <button
                        class="row-action"
                        :aria-label="'Inspect run ' + r.run_id"
                        @click="openRun(r.run_id)"
                      >
                        ↗
                      </button>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div v-if="!runs.items.length" class="empty">
              <span>⌁</span>
              <h3>
                {{
                  tab === "errors"
                    ? "No failures in this view"
                    : "No executions yet"
                }}
              </h3>
              <p>
                {{
                  task || status || service
                    ? "Try a different filter or time window."
                    : "Run the included Go example to send your first real task events."
                }}
              </p>
              <code v-if="!task && !status && !service && tab !== 'errors'"
                >go run ./examples/basic</code
              ><button v-else @click="clear">Clear filters</button>
            </div>
          </section>
          <div class="pagination" v-if="total > 10">
            <span
              >Showing {{ offset + 1 }}–{{ Math.min(offset + 10, total) }} of
              {{ total }} {{ tab === "tasks" ? "tasks" : "runs" }}</span
            >
            <div>
              <button
                :disabled="offset === 0"
                @click="offset = Math.max(0, offset - 10)"
              >
                ← Previous</button
              ><button :disabled="offset + 10 >= total" @click="offset += 10">
                Next →
              </button>
            </div>
          </div>
          <section
            v-if="tab === 'overview' || tab === 'errors'"
            class="diagnosis panel"
          >
            <div class="diagnosis-mark">⌘</div>
            <div>
              <div class="diagnosis-heading">
                <h2>Diagnostic evidence</h2>
                <span class="evidence-label">MEASURED DATA</span>
              </div>
              <template v-if="summary?.errors.length"
                ><p>
                  {{ summary.failed }} failed
                  {{ summary.failed === 1 ? "execution" : "executions" }} in
                  this window. Most frequent error:
                </p>
                <button
                  class="error-excerpt"
                  @click="openRun(summary.errors[0].example_run_id)"
                >
                  {{ summary.errors[0].summary || "(no error summary)" }}
                  <span>×{{ summary.errors[0].count }} ↗</span>
                </button>
                <p class="diagnosis-caption">
                  Inspect the source run, or connect an MCP client to
                  investigate further.
                </p></template
              >
              <p v-else>
                No failure patterns to inspect in the current window. Connect
                MCP to explore task evidence with your AI tools.
              </p>
            </div>
            <button class="text-link" @click="navigate('mcp')">
              MCP setup →
            </button>
          </section>
        </template>
        <template v-else-if="tab === 'mcp'"
          ><section class="panel setup-intro">
            <span class="setup-symbol">⌘</span>
            <div>
              <h2>Your tasks. Your AI tools. Real evidence.</h2>
              <p>
                GorunTrace exposes five read-only tools through the official MCP
                Go SDK. Query execution history, inspect failures and explore
                patterns from your preferred MCP client.
              </p>
              <div class="tags">
                <span>STDIO TRANSPORT</span><span>READ ONLY</span
                ><span>LOCAL FIRST</span>
              </div>
            </div>
          </section>
          <div class="setup-grid">
            <section class="panel">
              <div class="panel-heading">
                <div>
                  <h2>01 / Build the MCP server</h2>
                  <p>Run from the project root</p>
                </div>
              </div>
              <pre>go build -o bin/goruntrace-mcp ./cmd/mcp</pre>
              <div class="panel-heading">
                <div>
                  <h2>02 / Configure your client</h2>
                  <p>Example TOML · use an absolute executable path</p>
                </div>
                <button class="small-button" @click="copyConfig">
                  {{ copied ? "Copied" : "Copy" }}
                </button>
              </div>
              <pre>{{ mcpConfig }}</pre>
              <div class="panel-heading">
                <div>
                  <h2>03 / Ask a question</h2>
                  <p class="example-prompt">
                    “Inspect failed sync-orders runs from the last 24 hours.
                    Cite the run IDs and distinguish evidence from possible
                    causes.”
                  </p>
                </div>
              </div>
            </section>
            <section class="panel tool-list">
              <div class="panel-heading">
                <h2>Available tools <span class="count">5</span></h2>
              </div>
              <div
                v-for="t in [
                  {
                    name: 'list_tasks',
                    desc: 'Discover tasks by service and time window.',
                  },
                  {
                    name: 'get_task_runs',
                    desc: 'Filter and paginate execution history.',
                  },
                  {
                    name: 'get_failed_runs',
                    desc: 'Find failures, timeouts and panics.',
                  },
                  {
                    name: 'get_run_details',
                    desc: 'Inspect a run and its event timeline.',
                  },
                  {
                    name: 'analyze_task',
                    desc: 'Measure failure rates and error patterns.',
                  },
                ]"
                :key="t.name"
                class="tool-item"
              >
                <span>↗</span>
                <div>
                  <code>{{ t.name }}</code>
                  <p>{{ t.desc }}</p>
                </div>
              </div>
              <p class="tool-note">
                Task data is evidence, not instructions. Tools cannot retry,
                schedule or modify business tasks.
              </p>
            </section>
          </div></template
        >
        <template v-else
          ><section class="panel settings-panel">
            <div class="panel-heading">
              <div>
                <h2>Collector connection</h2>
                <p>The Dashboard connects to the API on the same origin.</p>
              </div>
              <span class="status" :class="connected ? 'succeeded' : 'failed'"
                ><i></i>{{ connected ? "Connected" : "Disconnected" }}</span
              >
            </div>
            <form @submit.prevent="connect">
              <label
                >API endpoint<input
                  :value="apiOrigin + '/api/v1'"
                  readonly /></label
              ><label
                >Bearer token
                <span class="optional">optional in loopback mode</span
                ><input
                  v-model="draftToken"
                  type="password"
                  autocomplete="off"
                  placeholder="Enter GORUNTRACE_TOKEN if configured"
              /></label>
              <p class="form-note">
                The token stays in this page’s memory and is cleared on reload.
                Start the collector with GORUNTRACE_TOKEN to require
                authentication.
              </p>
              <button class="primary-button" type="submit">
                Connect to collector →
              </button>
            </form>
          </section>
          <section class="panel settings-panel">
            <div class="panel-heading"><h2>Start observing</h2></div>
            <pre>
go run ./cmd/server
# In another terminal:
go run ./examples/basic</pre>
            <p class="settings-note">
              SQLite stores observed runs locally. Data is collected only when
              you explicitly wrap a function with the Go SDK. No task arguments
              are collected.
            </p>
          </section></template
        >
        <footer>
          <span
            ><span class="footer-mark">↗</span>GorunTrace
            <span class="footer-divider">/</span> Task observability for
            Go</span
          ><span
            >{{ connected ? "Live collector data" : "Awaiting collector" }} ·
            {{ hours }}h window · Local time</span
          >
        </footer>
      </div>
    </main>
    <dialog
      ref="dialog"
      class="run-dialog"
      @cancel.prevent="closeDetail"
      @click="
        (e) => {
          if (e.target === dialog) closeDetail();
        }
      "
    >
      <div class="detail-heading">
        <div>
          <p class="eyebrow">EXECUTION DETAIL</p>
          <h2>{{ detail?.run.task_name || "Inspect task run" }}</h2>
        </div>
        <button
          class="close-button"
          aria-label="Close run details"
          @click="closeDetail"
        >
          ×
        </button>
      </div>
      <div v-if="detailLoading" class="empty" role="status">
        Loading event timeline…
      </div>
      <div v-else-if="detailError" class="error-banner" role="alert">
        {{ detailError }}
      </div>
      <template v-else-if="detail"
        ><span class="status" :class="detail.run.status"
          ><i></i>{{ label(detail.run.status) }}</span
        >
        <dl class="detail-meta">
          <dt>Run ID</dt>
          <dd class="mono break">{{ detail.run.run_id }}</dd>
          <dt>Service</dt>
          <dd>{{ detail.run.service }}</dd>
          <dt>Started</dt>
          <dd>{{ date(detail.run.started_at) }}</dd>
          <dt>Ended</dt>
          <dd>
            {{
              detail.run.ended_at
                ? date(detail.run.ended_at)
                : "No terminal event observed"
            }}
          </dd>
          <dt>Duration / attempt</dt>
          <dd>
            {{
              detail.run.status === "started"
                ? "—"
                : duration(detail.run.duration_ms)
            }}
            / {{ detail.run.attempt }}
          </dd>
        </dl>
        <div v-if="detail.run.error_summary" class="detail-error">
          <h3>Error summary</h3>
          <pre>{{ detail.run.error_summary }}</pre>
        </div>
        <h3 class="timeline-title">
          Event timeline <span class="count">{{ detail.events.length }}</span>
        </h3>
        <ol class="timeline">
          <li v-for="e in detail.events" :key="e.event_id">
            <span class="timeline-dot" :class="e.type"></span>
            <div>
              <strong>{{ label(e.type) }}</strong
              ><time>{{ date(e.occurred_at) }}</time>
              <p v-if="e.error_summary">{{ e.error_summary }}</p>
              <small class="mono muted">{{ e.event_id }}</small>
            </div>
          </li>
        </ol>
        <p class="detail-footnote">
          An active status means no terminal event was received. SDK delivery is
          best effort; missing events do not prove a task is still executing.
        </p></template
      >
    </dialog>
  </div>
</template>

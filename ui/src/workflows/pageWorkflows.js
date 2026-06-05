// Workflows admin tab: read-only observability over the OpenWorkflow engine
// (/api/ow/v1) plus a "New Run" create panel and Cancel for non-terminal runs.
// The run-detail view is a side panel driven by the `run` query param
// (collections-style), so back/forward and deep-links work.

import { isTerminal, statusClass, statusLabel, toDisplayDatetime } from "./workflowUtils";
import "./workflowRunCreatePanel";
import "./workflowRunDetailPanel";

const NAMESPACE = "default";
const PER_PAGE = 50;
const RUN_QUERY_KEY = "run";
const CARD_ORDER = ["pending", "running", "completed", "failed", "canceled"];

export function pageWorkflows(route) {
    app.store.title = "Workflows";

    const data = store({
        runs: [],
        next: null,
        loading: false,
        counts: null,
        statusFilter: "",
        nameFilter: "",
        activeRunId: route.query[RUN_QUERY_KEY]?.[0] || "",
        get canLoadMore() {
            return !!data.next;
        },
        get hasFilters() {
            return !!data.statusFilter || !!data.nameFilter.trim();
        },
    });

    // The name filter reloads on a short debounce so typing doesn't fire a
    // request per keystroke.
    let nameDebounce = null;

    function apiBase() {
        return `/api/ow/v1/${encodeURIComponent(NAMESPACE)}`;
    }

    function replaceRun(run) {
        const i = data.runs.findIndex((r) => r.id === run.id);
        if (i >= 0) {
            data.runs[i] = run;
            data.runs = data.runs.slice(); // reassign to trigger reactivity
        }
    }

    async function loadCounts() {
        try {
            const res = await app.pb.send(`${apiBase()}/runs/counts`, { method: "GET", requestKey: "ow_counts" });
            data.counts = res.counts;
        } catch (err) {
            if (!err.isAbort) app.checkApiError(err);
        }
    }

    async function loadRuns(reset = true) {
        data.loading = true;
        try {
            const query = { limit: PER_PAGE };
            // Filters are not encoded in the cursor, so they must be resent on
            // every page request.
            if (data.statusFilter) {
                query.status = data.statusFilter;
            }
            const name = data.nameFilter.trim();
            if (name) {
                query.workflowName = name;
            }
            if (!reset && data.next) {
                query.after = data.next;
            }
            const res = await app.pb.send(`${apiBase()}/runs`, { method: "GET", query, requestKey: "ow_runs" });
            data.runs = reset ? res.data || [] : data.runs.concat(res.data || []);
            data.next = res.pagination?.next || null;
        } catch (err) {
            if (!err.isAbort) app.checkApiError(err);
        }
        data.loading = false;
    }

    // Filter changes always restart from the first page -- an existing cursor
    // was issued against the previous filter set.
    function toggleStatusFilter(status) {
        data.statusFilter = data.statusFilter === status ? "" : status;
        data.next = null;
        loadRuns(true);
    }

    function setNameFilter(value) {
        data.nameFilter = value;
        clearTimeout(nameDebounce);
        nameDebounce = setTimeout(() => {
            data.next = null;
            loadRuns(true);
        }, 300);
    }

    function clearFilters() {
        clearTimeout(nameDebounce);
        data.statusFilter = "";
        data.nameFilter = "";
        data.next = null;
        loadRuns(true);
    }

    function cancelRun(run) {
        app.modals.confirm(`Do you really want to cancel workflow run "${run.workflowName}"?`, async () => {
            try {
                const res = await app.pb.send(`${apiBase()}/runs/${run.id}/cancel`, { method: "POST", body: {} });
                replaceRun(res.run);
                loadCounts();
            } catch (err) {
                app.checkApiError(err);
            }
        });
    }

    function openNewRun() {
        app.modals.openWorkflowRunCreate({
            namespace: NAMESPACE,
            onsave: () => {
                loadRuns(true);
                loadCounts();
            },
        });
    }

    // Drive the run-detail side panel from the `run` query param so deep-links
    // and browser back/forward work (the modal infra auto-closes on popstate).
    const watchers = [
        watch(
            () => data.activeRunId,
            (newVal, oldVal) => {
                if (newVal === oldVal) {
                    return;
                }

                if (!data.activeRunId) {
                    app.utils.replaceHashQueryParams({ [RUN_QUERY_KEY]: null });
                    return;
                }

                const openedRunId = data.activeRunId;
                app.utils.replaceHashQueryParams({ [RUN_QUERY_KEY]: openedRunId });

                // force-close any previously open panel before opening the next
                app.modals.close(null, true);

                app.modals.openWorkflowRunDetail({
                    namespace: NAMESPACE,
                    runId: openedRunId,
                    onnavigate: (id) => {
                        data.activeRunId = id;
                    },
                    oncancel: (run) => {
                        replaceRun(run);
                        loadCounts();
                    },
                    onafterclose: () => {
                        // only clear if this is still the active run (avoids
                        // clobbering a parent/child navigation swap)
                        if (data.activeRunId === openedRunId) {
                            data.activeRunId = "";
                        }
                    },
                });
            },
        ),
    ];

    // initial load
    loadRuns(true);
    loadCounts();

    return t.div(
        {
            pbEvent: "pageWorkflows",
            className: "page page-workflows",
            onunmount: () => {
                clearTimeout(nameDebounce);
                app.pb.cancelRequest("ow_counts");
                app.pb.cancelRequest("ow_runs");
                watchers.forEach((w) => w?.unwatch());
            },
        },
        t.div(
            { className: "page-content full-height" },
            t.header(
                { className: "page-header" },
                t.nav({ className: "breadcrumbs" }, t.div({ className: "breadcrumb-item" }, "Workflows")),
                t.div({ className: "flex-fill" }),
                t.div(
                    { className: "ow-name-filter" },
                    t.i({ className: "ri-search-line" }),
                    t.input({
                        type: "text",
                        placeholder: "Filter by workflow name",
                        value: () => data.nameFilter,
                        oninput: (e) => setNameFilter(e.target.value),
                    }),
                ),
                () =>
                    data.hasFilters
                        ? t.button(
                            {
                                type: "button",
                                className: "btn secondary",
                                onclick: clearFilters,
                            },
                            t.i({ className: "ri-close-line" }),
                            t.span({ className: "txt" }, "Clear filters"),
                        )
                        : t.div({}),
                t.button(
                    {
                        type: "button",
                        className: "btn secondary",
                        onclick: () => {
                            loadRuns(true);
                            loadCounts();
                        },
                    },
                    t.i({ className: "ri-refresh-line" }),
                    t.span({ className: "txt" }, "Refresh"),
                ),
                t.button(
                    { type: "button", className: "btn", onclick: openNewRun },
                    t.i({ className: "ri-add-line" }),
                    t.span({ className: "txt" }, "New Run"),
                ),
            ),
            () =>
                data.counts
                    ? t.div(
                        { className: "ow-cards" },
                        // The cards double as the status filter. Counts stay
                        // unfiltered so they keep reading as totals.
                        ...CARD_ORDER.map((k) =>
                            t.div(
                                {
                                    className: () =>
                                        "ow-card ow-card-" + k + (data.statusFilter === k ? " ow-card-active" : ""),
                                    "html-role": "button",
                                    tabIndex: 0,
                                    title: `Show only ${statusLabel(k).toLowerCase()} runs`,
                                    "html-aria-pressed": () => (data.statusFilter === k ? "true" : "false"),
                                    onclick: () => toggleStatusFilter(k),
                                    onkeydown: (e) => {
                                        if (e.key === "Enter" || e.key === " ") {
                                            e.preventDefault();
                                            toggleStatusFilter(k);
                                        }
                                    },
                                },
                                t.span({ className: "ow-card-value txt-mono" }, "" + (data.counts[k] ?? 0)),
                                t.span({ className: "ow-card-label" }, statusLabel(k)),
                            )
                        ),
                    )
                    : t.div({}),
            t.table(
                { className: "table" },
                t.thead(
                    {},
                    t.tr(
                        {},
                        t.th({}, "Workflow"),
                        t.th({}, "Status"),
                        t.th({}, "Attempts"),
                        t.th({}, "Created"),
                        t.th({}, ""),
                    ),
                ),
                t.tbody({}, () =>
                    data.runs.length
                        ? data.runs.map((run) =>
                            t.tr(
                                { className: "ow-run-row", onclick: () => (data.activeRunId = run.id) },
                                t.td({}, t.span({ className: "txt-mono" }, run.workflowName)),
                                t.td(
                                    {},
                                    t.span({ className: "label " + statusClass(run.status) }, statusLabel(run.status)),
                                ),
                                t.td({}, "" + run.attempts),
                                t.td(
                                    {},
                                    app.components.formattedDate({
                                        value: () => toDisplayDatetime(run.createdAt),
                                        short: true,
                                    }),
                                ),
                                t.td(
                                    {},
                                    !isTerminal(run.status)
                                        ? t.button(
                                            {
                                                type: "button",
                                                className: "btn sm danger",
                                                onclick: (e) => {
                                                    e.stopPropagation();
                                                    cancelRun(run);
                                                },
                                            },
                                            t.span({ className: "txt" }, "Cancel"),
                                        )
                                        : t.span({}),
                                ),
                            )
                        )
                        : t.tr(
                            {},
                            t.td(
                                { colSpan: 5 },
                                t.span(
                                    { className: "txt-hint" },
                                    data.loading
                                        ? "Loading..."
                                        : data.hasFilters
                                        ? "No workflow runs match the current filters."
                                        : "No workflow runs yet.",
                                ),
                            ),
                        )),
            ),
            () =>
                data.canLoadMore
                    ? t.div(
                        { className: "ow-loadmore" },
                        t.button(
                            {
                                type: "button",
                                className: "btn secondary expanded",
                                onclick: () => loadRuns(false),
                            },
                            t.span({ className: "txt" }, "Load more"),
                        ),
                    )
                    : t.div({}),
            t.footer({ className: "page-footer" }, app.components.credits()),
        ),
    );
}

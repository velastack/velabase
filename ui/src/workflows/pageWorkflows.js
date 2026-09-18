// Workflows admin tab: read-only observability over the OpenWorkflow engine
// (/api/ow/v1) plus a "New Run" create panel and Cancel for non-terminal runs.
// The run-detail view is a side panel driven by the `run` query param
// (collections-style), so back/forward and deep-links work.

import {
    computeDuration,
    isTerminal,
    startPolling,
    statusClass,
    statusLabel,
    toDisplayDatetime,
} from "./workflowUtils";
import "./workflowRunCreatePanel";
import "./workflowRunDetailPanel";

const NAMESPACE = "default";
// Page sizes and cursor Prev/Next paging mirror @openworkflow/dashboard.
const PAGE_SIZES = [25, 50, 100];
const POLL_INTERVAL = 5000;
const RUN_QUERY_KEY = "run";
const CARD_ORDER = ["pending", "running", "completed", "failed", "canceled"];

export function pageWorkflows(route) {
    app.store.title = "Workflows";

    const data = store({
        runs: [],
        // cursors of the adjacent pages, as returned with the current page
        prev: null,
        next: null,
        // the current page position -- at most one of after/before is set
        // (both empty means the first page); persisted in the url together
        // with the page size and the filters so a page is deep-linkable
        after: route.query.after?.[0] || "",
        before: route.query.after?.[0] ? "" : route.query.before?.[0] || "",
        pageSize: resolvePageSize(route.query.limit?.[0]),
        loading: false,
        counts: null,
        statusFilter: CARD_ORDER.includes(route.query.status?.[0]) ? route.query.status[0] : "",
        nameFilter: route.query.name?.[0] || "",
        activeRunId: route.query[RUN_QUERY_KEY]?.[0] || "",
        get hasFilters() {
            return !!data.statusFilter || !!data.nameFilter.trim();
        },
    });

    // Per-instance request keys: on a same-route navigation (e.g. a deep-link
    // with different query params) the new page is mounted before the old one
    // is unmounted, so a shared key would let the old page's cleanup abort the
    // new page's initial requests.
    const uid = app.utils.randomString(6);
    const runsKey = "ow_runs_" + uid;
    const countsKey = "ow_counts_" + uid;

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
            const res = await app.pb.send(`${apiBase()}/runs/counts`, { method: "GET", requestKey: countsKey });
            data.counts = res.counts;
        } catch (err) {
            if (!err.isAbort) app.checkApiError(err);
        }
    }

    // (Re)loads the current page. `silent` is used by the background polling:
    // no loading state and no error toasts.
    async function loadRuns(silent = false) {
        if (!silent) {
            data.loading = true;
        }
        try {
            const query = { limit: data.pageSize };
            // Filters are not encoded in the cursor, so they must be resent on
            // every page request.
            if (data.statusFilter) {
                query.status = data.statusFilter;
            }
            const name = data.nameFilter.trim();
            if (name) {
                query.workflowNameContains = name;
            }
            if (data.after) {
                query.after = data.after;
            } else if (data.before) {
                query.before = data.before;
            }
            const res = await app.pb.send(`${apiBase()}/runs`, { method: "GET", query, requestKey: runsKey });
            const runs = res.data || [];
            // skip the table rerender when a poll brought nothing new
            if (!silent || JSON.stringify(runs) !== JSON.stringify(data.runs)) {
                data.runs = runs;
            }
            data.prev = res.pagination?.prev || null;
            data.next = res.pagination?.next || null;
        } catch (err) {
            if (!err.isAbort && !silent) app.checkApiError(err);
        }
        if (!silent) {
            data.loading = false;
        }
    }

    function syncQueryParams() {
        app.utils.replaceHashQueryParams({
            limit: data.pageSize === PAGE_SIZES[0] ? null : data.pageSize,
            after: data.after,
            before: data.before,
            status: data.statusFilter,
            name: data.nameFilter.trim(),
        });
    }

    function goToPage(after, before) {
        data.after = after || "";
        data.before = before || "";
        syncQueryParams();
        loadRuns();
    }

    // Filter and page size changes always restart from the first page -- an
    // existing cursor was issued against the previous filter set.
    function toggleStatusFilter(status) {
        data.statusFilter = data.statusFilter === status ? "" : status;
        goToPage();
    }

    function setNameFilter(value) {
        data.nameFilter = value;
        clearTimeout(nameDebounce);
        nameDebounce = setTimeout(() => goToPage(), 300);
    }

    function clearFilters() {
        clearTimeout(nameDebounce);
        data.statusFilter = "";
        data.nameFilter = "";
        goToPage();
    }

    function setPageSize(size) {
        data.pageSize = resolvePageSize(size);
        goToPage();
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
                // jump to the first page where the new run is listed
                goToPage();
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
    loadRuns();
    loadCounts();

    const stopPolling = startPolling(() => {
        loadRuns(true);
        loadCounts();
    }, POLL_INTERVAL);

    return t.div(
        {
            pbEvent: "pageWorkflows",
            className: "page page-workflows",
            onunmount: () => {
                clearTimeout(nameDebounce);
                stopPolling();
                app.pb.cancelRequest(countsKey);
                app.pb.cancelRequest(runsKey);
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
                            loadRuns();
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
                        t.th({}, "Duration"),
                        t.th({}, ""),
                    ),
                ),
                t.tbody({}, () =>
                    data.runs.length
                        ? data.runs.map((run) =>
                            t.tr(
                                { className: "ow-run-row", onclick: () => (data.activeRunId = run.id) },
                                t.td(
                                    {},
                                    t.span({ className: "txt-mono" }, run.workflowName),
                                    run.version ? t.span({ className: "label m-l-10" }, run.version) : null,
                                ),
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
                                    t.span({ className: "txt-mono" }, computeDuration(run.startedAt, run.finishedAt)),
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
                                { colSpan: 6 },
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
            // also kept for a non-default page size, otherwise a size that fits
            // all runs in one page would hide the control to change it back
            () =>
                data.prev || data.next || data.pageSize !== PAGE_SIZES[0]
                    ? t.div(
                        { className: "ow-pagination" },
                        t.span(
                            { className: "txt-hint" },
                            () => `Showing ${data.runs.length} run${data.runs.length === 1 ? "" : "s"}`,
                        ),
                        t.div({ className: "flex-fill" }),
                        t.span({ className: "txt-hint" }, "Page size"),
                        t.div(
                            { className: "field ow-page-size" },
                            app.components.select({
                                options: PAGE_SIZES.map((size) => ({ value: size, label: "" + size })),
                                required: true,
                                value: () => data.pageSize,
                                onchange: (selected) => {
                                    const size = selected?.[0]?.value;
                                    if (size && size !== data.pageSize) {
                                        setPageSize(size);
                                    }
                                },
                            }),
                        ),
                        t.button(
                            {
                                type: "button",
                                className: "btn sm secondary",
                                disabled: () => !data.prev || data.loading,
                                onclick: () => goToPage(null, data.prev),
                            },
                            t.i({ className: "ri-arrow-left-s-line" }),
                            t.span({ className: "txt" }, "Previous"),
                        ),
                        t.button(
                            {
                                type: "button",
                                className: "btn sm secondary",
                                disabled: () => !data.next || data.loading,
                                onclick: () => goToPage(data.next, null),
                            },
                            t.span({ className: "txt" }, "Next"),
                            t.i({ className: "ri-arrow-right-s-line" }),
                        ),
                    )
                    : t.div({}),
            t.footer({ className: "page-footer" }, app.components.credits()),
        ),
    );
}

function resolvePageSize(limit) {
    limit = parseInt(limit, 10);
    return PAGE_SIZES.includes(limit) ? limit : PAGE_SIZES[0];
}

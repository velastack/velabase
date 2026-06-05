// Run-detail side panel for the Workflows tab. Opened from the `run` query
// param on #/workflows (collections-style). Fetches the run + its step attempts
// and renders: a compact meta grid, the run payloads as read-only code editors,
// and the steps as a list of accordions (step name left, status badge right;
// expand for per-step payloads + child-run link). Read-only except for Cancel.

import { computeDuration, fmtDate, isTerminal, statusClass, statusLabel, stepAttemptIndexById } from "./workflowUtils";

window.app = window.app || {};
window.app.modals = window.app.modals || {};

/**
 * Opens the run-detail side panel.
 *
 * @param {Object}   opts
 * @param {string}   [opts.namespace="default"]
 * @param {string}   opts.runId
 * @param {Function} [opts.onnavigate]    function(runId) {}  // parent/child link
 * @param {Function} [opts.oncancel]      function(run) {}     // after a successful cancel
 * @param {Function} [opts.onafterclose]  function(el) {}
 */
window.app.modals.openWorkflowRunDetail = function(opts = {}) {
    if (!opts.runId) {
        console.warn("[openWorkflowRunDetail] missing required runId");
        return;
    }

    const modal = workflowRunDetailModal(opts);
    document.body.appendChild(modal);

    app.modals.open(modal);
};

function workflowRunDetailModal(opts) {
    const namespace = opts.namespace || "default";
    const runId = opts.runId;
    const base = `/api/ow/v1/${encodeURIComponent(namespace)}`;

    let modal;

    const data = store({
        loading: true,
        run: null,
        steps: [],
        parentRunId: null,
    });

    async function load() {
        data.loading = true;
        try {
            const [runRes, stepsRes] = await Promise.all([
                app.pb.send(`${base}/runs/${runId}`, { method: "GET", requestKey: "ow_run_detail" }),
                app.pb.send(`${base}/runs/${runId}/steps`, {
                    method: "GET",
                    query: { limit: 200 },
                    requestKey: "ow_run_detail_steps",
                }),
            ]);
            data.run = runRes.run;
            data.steps = stepsRes.data || [];

            // best-effort parent run resolution via the parent step attempt
            if (data.run?.parentStepAttemptId) {
                try {
                    const pres = await app.pb.send(`${base}/steps/${data.run.parentStepAttemptId}`, {
                        method: "GET",
                        requestKey: "ow_run_detail_parent",
                    });
                    data.parentRunId = pres.stepAttempt?.workflowRunId || null;
                } catch (_) {
                    /* ignore - parent link is optional */
                }
            }
        } catch (err) {
            if (!err.isAbort) app.checkApiError(err);
        }
        data.loading = false;
    }

    function cancel() {
        if (!data.run) {
            return;
        }
        app.modals.confirm(`Do you really want to cancel workflow run "${data.run.workflowName}"?`, async () => {
            try {
                const res = await app.pb.send(`${base}/runs/${runId}/cancel`, { method: "POST", body: {} });
                data.run = res.run;
                opts.oncancel?.(res.run);
            } catch (err) {
                app.checkApiError(err);
            }
        });
    }

    function navigate(id) {
        if (id) {
            opts.onnavigate?.(id);
        }
    }

    function metaItem(label, value, mono) {
        return t.div(
            { className: "ow-meta-item" },
            t.span({ className: "ow-meta-label" }, label),
            t.span({ className: mono ? "ow-meta-value txt-mono" : "ow-meta-value" }, value ?? "-"),
        );
    }

    function runLink(label, id) {
        return t.button(
            {
                type: "button",
                className: "btn sm transparent ow-run-link",
                onclick: (e) => {
                    e.preventDefault();
                    e.stopPropagation();
                    navigate(id);
                },
            },
            t.span({ className: "txt-mono" }, label),
        );
    }

    // labeled, read-only code editor for a JSON payload (returns null when absent)
    function jsonFieldCol(label, value) {
        if (value === null || value === undefined) {
            return null;
        }
        const text = typeof value === "string" ? value : JSON.stringify(value, null, 2);
        return t.div(
            { className: "col-12" },
            t.div(
                { className: "field" },
                t.label({}, t.span({ className: "txt" }, label)),
                app.components.codeEditor({
                    language: "js",
                    disabled: true,
                    value: () => text,
                }),
            ),
        );
    }

    function metaGrid() {
        const run = data.run;
        return t.div(
            { className: "ow-detail-meta" },
            metaItem("ID", run.id, true),
            metaItem("Attempts", "" + run.attempts),
            metaItem("Duration", computeDuration(run.startedAt, run.finishedAt), true),
            metaItem("Version", run.version, true),
            metaItem("Worker", run.workerId, true),
            metaItem("Idempotency Key", run.idempotencyKey, true),
            metaItem("Created", fmtDate(run.createdAt)),
            metaItem("Started", fmtDate(run.startedAt)),
            metaItem("Finished", fmtDate(run.finishedAt)),
            metaItem("Available", fmtDate(run.availableAt)),
            metaItem("Deadline", fmtDate(run.deadlineAt)),
            () =>
                data.parentRunId
                    ? t.div(
                        { className: "ow-meta-item" },
                        t.span({ className: "ow-meta-label" }, "Parent Run"),
                        runLink(data.parentRunId, data.parentRunId),
                    )
                    : t.div({}),
        );
    }

    function runPayloads() {
        const run = data.run;
        const fields = [
            jsonFieldCol("Input", run.input),
            jsonFieldCol("Output", run.output),
            jsonFieldCol("Error", run.error),
            jsonFieldCol("Context", run.context),
        ].filter(Boolean);
        return fields.length ? t.div({ className: "grid ow-payloads" }, ...fields) : t.div({});
    }

    function stepAccordion(s, idx) {
        const bodyChildren = [
            t.div(
                { className: "col-12 ow-step-meta" },
                t.span({}, "Started: " + fmtDate(s.startedAt)),
                t.span({}, "Duration: " + computeDuration(s.startedAt, s.finishedAt)),
                s.childWorkflowRunId
                    ? runLink("child run →", s.childWorkflowRunId)
                    : t.span({}),
            ),
            jsonFieldCol("Output", s.output),
            jsonFieldCol("Error", s.error),
            jsonFieldCol("Context", s.context),
        ].filter(Boolean);

        return t.details(
            { className: "accordion ow-step-accordion" },
            t.summary(
                null,
                t.span({ className: "txt txt-mono" }, s.stepName),
                t.small(
                    { className: "txt-hint ow-step-sub" },
                    s.kind + (idx ? ` · ${idx.index}/${idx.total}` : ""),
                ),
                t.span({ className: "label m-l-auto " + statusClass(s.status) }, statusLabel(s.status)),
            ),
            t.div({ className: "grid" }, ...bodyChildren),
        );
    }

    function stepsList() {
        if (data.loading) {
            return t.div({ className: "txt-hint ow-steps-empty" }, "Loading...");
        }
        if (!data.steps.length) {
            return t.div({ className: "txt-hint ow-steps-empty" }, "No steps.");
        }
        const idxMap = stepAttemptIndexById(data.steps);
        return t.div({ className: "ow-steps" }, data.steps.map((s) => stepAccordion(s, idxMap[s.id])));
    }

    modal = t.div(
        {
            pbEvent: "workflowRunDetailPanel",
            className: "modal workflow-run-detail-panel",
            onbeforeopen: (el) => {
                load();
                return opts.onbeforeopen?.(el);
            },
            onafteropen: (el) => opts.onafteropen?.(el),
            onbeforeclose: (el) => opts.onbeforeclose?.(el),
            onafterclose: (el) => {
                opts.onafterclose?.(el);
                el?.remove();
            },
        },
        t.header(
            { className: "modal-header ow-detail-header" },
            () =>
                data.run
                    ? t.h5({ className: "modal-title txt-mono" }, data.run.workflowName)
                    : t.h5({ className: "modal-title" }, "Workflow run"),
            () =>
                data.run
                    ? t.span(
                        { className: "label m-l-10 " + statusClass(data.run.status) },
                        statusLabel(data.run.status),
                    )
                    : t.span({}),
        ),
        t.div(
            { className: "modal-content ow-detail" },
            () => {
                if (data.loading) {
                    return t.div({ className: "ow-detail-loading txt-hint" }, "Loading...");
                }
                if (!data.run) {
                    return t.div({ className: "alert alert-danger" }, "Failed to load workflow run.");
                }

                return t.div(
                    { className: "ow-detail-body" },
                    metaGrid(),
                    runPayloads(),
                    t.h6({ className: "ow-steps-title" }, "Steps"),
                    stepsList(),
                );
            },
        ),
        t.footer(
            { className: "modal-footer" },
            t.button(
                { type: "button", className: "btn transparent m-r-auto", onclick: () => app.modals.close(modal) },
                t.span({ className: "txt" }, "Close"),
            ),
            () =>
                data.run && !isTerminal(data.run.status)
                    ? t.button(
                        { type: "button", className: "btn danger", onclick: cancel },
                        t.span({ className: "txt" }, "Cancel run"),
                    )
                    : t.span({}),
        ),
    );

    return modal;
}

// Shared helpers for the Workflows tab (list page + run-detail panel).
// Status tone mapping follows the OpenWorkflow dashboard:
// running=info, completed=success, failed=danger, canceled=neutral, and
// pending=pending -- a workflows-local ".label" tone (see css/workflows.css)
// since PocketBase has no built-in yellow modifier. The returned strings are
// ".label" tone modifier classes, NOT hyphenated variants.

const TERMINAL_STATUSES = ["completed", "succeeded", "failed", "canceled"];

export function isTerminal(status) {
    return TERMINAL_STATUSES.includes(status);
}

// Maps an OpenWorkflow status to a ".label" tone modifier class
// (canceled is intentionally neutral -> no modifier).
export function statusClass(status) {
    switch (status) {
        case "completed":
        case "succeeded":
            return "success";
        case "failed":
            return "danger";
        case "running":
        case "sleeping":
            return "info";
        case "pending":
            return "pending";
        case "canceled":
            return "";
        default:
            return "";
    }
}

// Title-cases a status for the stat cards / badges ("pending" -> "Pending").
export function statusLabel(status) {
    return status ? app.utils.sentenize(status, false) : "-";
}

// Single-line local datetime for the run-detail meta grid, consistent with the
// rest of the admin (app.utils.toLocalDatetime accepts the OW RFC3339 form).
export function fmtDate(iso) {
    return iso ? app.utils.toLocalDatetime(iso) : "-";
}

// Normalizes an OW RFC3339 timestamp into the space-separated form that
// app.components.formattedDate / app.utils.toLocalDatetime expect (keeps the
// trailing Z so it's parsed as UTC). Returns "" when absent.
export function toDisplayDatetime(iso) {
    return iso ? iso.replace("T", " ").replace(/\.\d+/, "") : "";
}

// Human-readable elapsed time between startedAt and finishedAt (or now if the
// run/step is still in flight). Returns "-" when not yet started.
export function computeDuration(startedAt, finishedAt) {
    if (!startedAt) {
        return "-";
    }
    const start = new Date(startedAt).getTime();
    const end = finishedAt ? new Date(finishedAt).getTime() : Date.now();
    if (isNaN(start) || isNaN(end) || end < start) {
        return "-";
    }

    const totalSeconds = (end - start) / 1000;
    if (totalSeconds < 1) {
        return `${Math.round(end - start)}ms`;
    }
    if (totalSeconds < 60) {
        return `${totalSeconds.toFixed(1)}s`;
    }

    const totalMinutes = Math.floor(totalSeconds / 60);
    const seconds = Math.round(totalSeconds % 60);
    if (totalMinutes < 60) {
        return `${totalMinutes}m ${seconds}s`;
    }

    const hours = Math.floor(totalMinutes / 60);
    const minutes = totalMinutes % 60;
    return `${hours}h ${minutes}m`;
}

// Given a list of step attempts, returns a map of stepId -> { index, total }
// where index is the 1-based attempt number for that step name (ordered by
// createdAt). Used to render the "N/M" attempt label in the steps timeline.
export function stepAttemptIndexById(steps) {
    const groups = {};
    const sorted = (steps || []).slice().sort((a, b) => (a.createdAt || "").localeCompare(b.createdAt || ""));
    for (const s of sorted) {
        (groups[s.stepName] = groups[s.stepName] || []).push(s);
    }

    const map = {};
    for (const name in groups) {
        const arr = groups[name];
        arr.forEach((s, i) => {
            map[s.id] = { index: i + 1, total: arr.length };
        });
    }
    return map;
}

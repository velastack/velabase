// "New Run" side panel for the Workflows tab. Mirrors the record-upsert side
// panel idiom (right-side .modal opened via window.app.modals.openX, .grid form
// body, footer with Close + Create + a "Save options" dropdown, Ctrl+S) and
// posts to the frozen OpenWorkflow endpoint POST /api/ow/v1/{ns}/runs.
//
// Fields match the OW dashboard create-run form (Workflow Name, Input (JSON),
// Schedule For + Deadline, Version). config/context/idempotencyKey are
// intentionally omitted (the server defaults config to {}).

window.app = window.app || {};
window.app.modals = window.app.modals || {};

/**
 * Opens the "create workflow run" side panel.
 *
 * @param {Object}   [opts]
 * @param {string}   [opts.namespace="default"]
 * @param {Function} [opts.onsave]        function(run) {}
 * @param {Function} [opts.onafterclose]  function(el) {}
 */
window.app.modals.openWorkflowRunCreate = function(opts = {}) {
    app.store.errors = null; // reset

    const modal = workflowRunCreateModal(opts);
    document.body.appendChild(modal);

    app.modals.open(modal);
};

function workflowRunCreateModal(opts) {
    const namespace = opts.namespace || "default";
    const uniqueId = "workflow_run_create_" + app.utils.randomString();
    const formId = uniqueId + "_form";

    let modal;

    const data = store({
        workflowName: "",
        version: "",
        inputText: "",
        availableAt: "",
        deadlineAt: "",
        inputError: "",
        isSaving: false,
        get canSave() {
            return !data.isSaving && data.workflowName.trim() !== "";
        },
    });

    function resetForm() {
        data.workflowName = "";
        data.version = "";
        data.inputText = "";
        data.availableAt = "";
        data.deadlineAt = "";
        data.inputError = "";
    }

    async function save(close = true) {
        if (!data.canSave) {
            return;
        }

        const body = { workflowName: data.workflowName.trim() };

        const version = data.version.trim();
        if (version) {
            body.version = version;
        }

        const inputTrimmed = data.inputText.trim();
        if (inputTrimmed) {
            let parsed;
            try {
                parsed = JSON.parse(inputTrimmed);
            } catch (_) {
                data.inputError = "Invalid JSON.";
                return;
            }
            data.inputError = "";
            body.input = parsed;
        }

        if (data.availableAt) {
            body.availableAt = app.utils.toRFC3339Datetime(data.availableAt);
        }
        if (data.deadlineAt) {
            body.deadlineAt = app.utils.toRFC3339Datetime(data.deadlineAt);
        }

        data.isSaving = true;
        try {
            const res = await app.pb.send(`/api/ow/v1/${encodeURIComponent(namespace)}/runs`, {
                method: "POST",
                body,
            });

            app.toasts.success("Successfully created workflow run.", { key: "workflowRunCreate" });
            opts.onsave?.(res.run);

            data.isSaving = false;

            if (close) {
                app.modals.close(modal, true);
            } else {
                // "save and continue" -> keep the panel open to add another run
                resetForm();
            }
        } catch (err) {
            if (!err?.isAbort) {
                data.isSaving = false;
                app.checkApiError(err);
            }
        }
    }

    function textField(label, key, attrs = {}) {
        const id = uniqueId + "_" + key;
        return t.div(
            { className: "field" },
            t.label({ htmlFor: id }, t.span({ className: "txt" }, label)),
            t.input({
                id,
                type: "text",
                value: () => data[key],
                oninput: (e) => (data[key] = e.target.value),
                ...attrs,
            }),
        );
    }

    function datetimeField(label, key, help) {
        const id = uniqueId + "_" + key;
        // help text is rendered as a sibling of .field (not inside it) so it sits
        // on the panel background rather than the input background, matching the
        // built-in date field component.
        return t.div(
            { className: "ow-field" },
            t.div(
                { className: "field" },
                t.label({ htmlFor: id }, t.span({ className: "txt" }, label)),
                t.input({
                    id,
                    step: 1,
                    type: "datetime-local",
                    value: () => data[key],
                    onchange: (e) => (data[key] = e.target.value),
                }),
            ),
            help ? t.div({ className: "field-help" }, help) : t.div({}),
        );
    }

    modal = t.div(
        {
            pbEvent: "workflowRunCreatePanel",
            className: "modal workflow-run-create-panel",
            onbeforeopen: (el) => {
                resetForm();
                data.isSaving = false;
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
            { className: "modal-header" },
            t.h5({ className: "modal-title" }, "New workflow run"),
        ),
        t.form(
            {
                id: formId,
                className: "modal-content",
                onsubmit: (e) => {
                    e.preventDefault();
                    save(true);
                },
            },
            t.div(
                {
                    className: "grid",
                    inert: () => data.isSaving,
                    onmount: (el) => {
                        el._quickSaveHandler = (e) => {
                            if ((e.ctrlKey || e.metaKey) && e.code == "KeyS") {
                                e.preventDefault();
                                save(false);
                            }
                        };
                        window.addEventListener("keydown", el._quickSaveHandler);
                    },
                    onunmount: (el) => {
                        if (el?._quickSaveHandler) {
                            window.removeEventListener("keydown", el._quickSaveHandler);
                        }
                    },
                },
                t.div(
                    { className: "col-12" },
                    textField("Workflow Name", "workflowName", {
                        required: true,
                        placeholder: "e.g. hello-world",
                        autofocus: true,
                    }),
                ),
                t.div(
                    { className: "col-12" },
                    t.div(
                        { className: "field" },
                        t.label(
                            { htmlFor: uniqueId + "_input" },
                            t.span({ className: "txt" }, "Input (JSON)"),
                            t.span(
                                {
                                    hidden: () => isValidStringifiedJSON(data.inputText.trim()),
                                    className: "json-state",
                                    ariaDescription: app.attrs.tooltip("Invalid JSON", "left"),
                                },
                                t.i({ className: "ri-error-warning-fill txt-danger", ariaHidden: true }),
                            ),
                            t.span(
                                {
                                    hidden: () => !isValidStringifiedJSON(data.inputText.trim()),
                                    className: "json-state",
                                    ariaDescription: app.attrs.tooltip("Valid JSON", "left"),
                                },
                                t.i({ className: "ri-checkbox-circle-fill txt-success", ariaHidden: true }),
                            ),
                        ),
                        app.components.codeEditor({
                            language: "js",
                            id: uniqueId + "_input",
                            value: () => data.inputText,
                            oninput: (val) => {
                                data.inputText = val;
                                data.inputError = "";
                            },
                        }),
                        () => (data.inputError
                            ? t.div({ className: "help-block help-block-error" }, data.inputError)
                            : t.div({})),
                    ),
                ),
                t.div(
                    { className: "col-12 col-sm-6" },
                    datetimeField("Schedule For", "availableAt", "Leave empty to run immediately."),
                ),
                t.div(
                    { className: "col-12 col-sm-6" },
                    datetimeField("Deadline", "deadlineAt", "Leave empty for no deadline."),
                ),
                t.div(
                    { className: "col-12" },
                    textField("Version", "version", { placeholder: "optional" }),
                ),
            ),
        ),
        t.footer(
            { className: "modal-footer" },
            t.button(
                {
                    type: "button",
                    className: "btn transparent m-r-auto",
                    disabled: () => data.isSaving,
                    onclick: () => app.modals.close(modal),
                },
                t.span({ className: "txt" }, "Close"),
            ),
            t.div(
                { className: "btns" },
                t.button(
                    {
                        type: "submit",
                        "html-form": formId,
                        className: () => `btn expanded-lg ${data.isSaving ? "loading" : ""}`,
                        disabled: () => !data.canSave,
                    },
                    t.span({ className: "txt" }, "Create"),
                ),
                t.button(
                    {
                        type: "button",
                        className: "btn p-5",
                        title: "Save options",
                        disabled: () => !data.canSave,
                        "html-popovertarget": uniqueId + "save_options",
                    },
                    t.i({ className: "ri-arrow-up-s-line", ariaHidden: true }),
                ),
                t.div(
                    { id: uniqueId + "save_options", className: "dropdown nowrap", popover: "auto" },
                    t.button(
                        {
                            type: "button",
                            className: "dropdown-item",
                            onclick: (e) => {
                                e.target.closest(".dropdown").hidePopover();
                                save(false);
                            },
                        },
                        t.span({ className: "txt" }, "Save and continue"),
                        t.small({ className: "txt-hint" }, "(Ctrl+S)"),
                    ),
                    t.hr(),
                    t.button(
                        {
                            type: "button",
                            className: "dropdown-item",
                            onclick: (e) => {
                                e.target.closest(".dropdown").hidePopover();
                                resetForm();
                            },
                        },
                        t.span({ className: "txt" }, "Reset form"),
                    ),
                ),
            ),
        ),
    );

    return modal;
}

function isValidStringifiedJSON(val) {
    if (val === "") {
        return true;
    }
    try {
        JSON.parse(val);
        return true;
    } catch (_) {
        return false;
    }
}

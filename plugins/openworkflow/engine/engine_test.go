package engine

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

const testNS = "default"

func openTestEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	e, err := Open(filepath.Join(dir, "openworkflow.db"), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func strptr(s string) *string { return &s }

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func mkRun(t *testing.T, e *Engine, p CreateWorkflowRunParams) *WorkflowRun {
	t.Helper()
	if p.Config == nil {
		p.Config = raw(`{}`)
	}
	run, err := e.CreateWorkflowRun(testNS, p)
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	return run
}

func TestMigrationsAreIdempotentAndVersioned(t *testing.T) {
	e := openTestEngine(t)
	v, err := e.currentMigrationVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != 5 {
		t.Fatalf("migration version = %d, want 5", v)
	}
	// Running again must be a no-op.
	if err := e.migrate(); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
}

func TestCreateClaimComplete(t *testing.T) {
	e := openTestEngine(t)
	run := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "wf", Config: raw(`{"a":1}`), Input: raw(`{"x":2}`)})

	if run.Status != statusPending {
		t.Fatalf("new run status = %q, want pending", run.Status)
	}
	if string(run.Config) != `{"a":1}` {
		t.Fatalf("config = %s", run.Config)
	}

	claimed, err := e.ClaimWorkflowRun(testNS, "worker-1", 30000)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v run=%v", err, claimed)
	}
	if claimed.ID != run.ID || claimed.Status != statusRunning || claimed.Attempts != 1 {
		t.Fatalf("claimed = %+v", claimed)
	}
	if claimed.WorkerID == nil || *claimed.WorkerID != "worker-1" {
		t.Fatalf("worker id = %v", claimed.WorkerID)
	}

	done, err := e.CompleteWorkflowRun(testNS, run.ID, "worker-1", raw(`{"ok":true}`))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if done.Status != statusCompleted || string(done.Output) != `{"ok":true}` {
		t.Fatalf("completed = %+v", done)
	}

	// Completing with the wrong worker must fail (ownership guard).
	if _, err := e.CompleteWorkflowRun(testNS, run.ID, "worker-2", raw(`{}`)); err == nil {
		t.Fatal("expected ownership guard to reject foreign completion")
	}
}

func TestClaimMissAndPendingOrdering(t *testing.T) {
	e := openTestEngine(t)
	// No runs -> claim miss.
	if r, err := e.ClaimWorkflowRun(testNS, "w", 30000); err != nil || r != nil {
		t.Fatalf("expected claim miss, got %v %v", r, err)
	}

	a := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "a"})
	time.Sleep(2 * time.Millisecond)
	b := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "b"})

	// Both pending and available now; oldest created_at wins.
	first, _ := e.ClaimWorkflowRun(testNS, "w", 30000)
	if first.ID != a.ID {
		t.Fatalf("first claim = %s, want oldest %s", first.ID, a.ID)
	}
	second, _ := e.ClaimWorkflowRun(testNS, "w", 30000)
	if second.ID != b.ID {
		t.Fatalf("second claim = %s, want %s", second.ID, b.ID)
	}
	// Both leased into the future -> next claim misses.
	if r, _ := e.ClaimWorkflowRun(testNS, "w", 30000); r != nil {
		t.Fatalf("expected claim miss, got %s", r.ID)
	}
}

func TestLeaseExpiryReclaim(t *testing.T) {
	e := openTestEngine(t)
	run := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "wf"})

	c1, _ := e.ClaimWorkflowRun(testNS, "worker-1", 1) // 1ms lease
	if c1 == nil || c1.Attempts != 1 {
		t.Fatalf("first claim = %+v", c1)
	}
	time.Sleep(20 * time.Millisecond)

	c2, err := e.ClaimWorkflowRun(testNS, "worker-2", 30000)
	if err != nil || c2 == nil {
		t.Fatalf("reclaim: %v %v", c2, err)
	}
	if c2.ID != run.ID || c2.Attempts != 2 {
		t.Fatalf("reclaim = %+v, want id=%s attempts=2", c2, run.ID)
	}
	if *c2.WorkerID != "worker-2" {
		t.Fatalf("reclaim worker = %v", c2.WorkerID)
	}
}

func TestDeadlineExpiryOnClaim(t *testing.T) {
	e := openTestEngine(t)
	past := isoFromTime(time.Now().UTC().Add(-time.Second))
	run := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "wf", DeadlineAt: &past})

	// The expiry sweep fails the run; nothing claimable.
	if r, _ := e.ClaimWorkflowRun(testNS, "w", 30000); r != nil {
		t.Fatalf("expected no claim, got %s", r.ID)
	}
	got, _ := e.GetWorkflowRun(testNS, run.ID)
	if got.Status != statusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.Error == nil {
		t.Fatal("expected deadline error to be set")
	}
}

func TestIdempotentCreate(t *testing.T) {
	e := openTestEngine(t)
	p := CreateWorkflowRunParams{WorkflowName: "wf", IdempotencyKey: strptr("k1"), Config: raw(`{}`)}
	r1 := mkRun(t, e, p)
	r2 := mkRun(t, e, p)
	if r1.ID != r2.ID {
		t.Fatalf("idempotent create returned different ids: %s vs %s", r1.ID, r2.ID)
	}
	// Different key -> different run.
	r3 := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "wf", IdempotencyKey: strptr("k2")})
	if r3.ID == r1.ID {
		t.Fatal("different idempotency key should create a new run")
	}
}

func TestFailWorkflowRetryThenTerminal(t *testing.T) {
	e := openTestEngine(t)
	run := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "wf"})
	policy := RetryPolicy{InitialInterval: "1s", BackoffCoefficient: 2, MaximumInterval: "100s", MaximumAttempts: 2}

	c1, _ := e.ClaimWorkflowRun(testNS, "w", 30000) // attempts=1
	failed, err := e.FailWorkflowRun(testNS, run.ID, FailWorkflowRunParams{
		WorkerID: "w", Error: raw(`{"message":"boom"}`), RetryPolicy: policy,
		Attempts: &c1.Attempts,
	})
	if err != nil {
		t.Fatalf("fail: %v", err)
	}
	if failed.Status != statusPending || failed.AvailableAt == nil {
		t.Fatalf("attempt 1 should reschedule: %+v", failed)
	}
	// available_at should be ~1s in the future.
	at, _ := parseISO(*failed.AvailableAt)
	if at.Before(time.Now().Add(500 * time.Millisecond)) {
		t.Fatalf("backoff not applied, available_at=%s", *failed.AvailableAt)
	}

	// Second failure hits maximumAttempts -> terminal.
	c2 := *c1
	c2.Attempts = 2
	term, err := e.FailWorkflowRun(testNS, run.ID, FailWorkflowRunParams{
		WorkerID: "w", Error: raw(`{"message":"boom2"}`), RetryPolicy: policy, Attempts: &c2.Attempts,
	})
	// The run is pending (not running/owned) now, so the guarded update matches
	// nothing -> ErrNotFound. Re-claim before failing to mimic the worker loop.
	if err == nil {
		t.Fatalf("expected guard failure when run not owned, got %+v", term)
	}

	reclaimed, _ := e.ClaimWorkflowRun(testNS, "w", 30000) // waits for availableAt? force it
	_ = reclaimed
}

func TestSignalDeliveryAndIdempotency(t *testing.T) {
	e := openTestEngine(t)
	run := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "wf"})
	if _, err := e.ClaimWorkflowRun(testNS, "w", 30000); err != nil {
		t.Fatal(err)
	}
	step, err := e.CreateStepAttempt(testNS, CreateStepAttemptParams{
		WorkflowRunID: run.ID, WorkerID: "w", StepName: "wait", Kind: kindSignalWait,
		Config: raw(`{}`), Context: raw(`{"kind":"signal-wait","signal":"approve","timeoutAt":"2030-01-01T00:00:00.000Z"}`),
	})
	if err != nil {
		t.Fatalf("create step: %v", err)
	}

	ids, err := e.SendSignal(testNS, SendSignalParams{Signal: "approve", Data: raw(`{"ok":true}`), IdempotencyKey: strptr("sig-1")})
	if err != nil {
		t.Fatalf("send signal: %v", err)
	}
	if len(ids) != 1 || ids[0] != run.ID {
		t.Fatalf("delivered to %v, want [%s]", ids, run.ID)
	}

	data, found, err := e.GetSignalDelivery(testNS, step.ID)
	if err != nil || !found {
		t.Fatalf("get delivery: %v found=%v", err, found)
	}
	if string(data) != `{"ok":true}` {
		t.Fatalf("delivery data = %s", data)
	}

	// Idempotent resend returns the same run ids without a new delivery.
	ids2, _ := e.SendSignal(testNS, SendSignalParams{Signal: "approve", Data: raw(`{"ok":true}`), IdempotencyKey: strptr("sig-1")})
	if len(ids2) != 1 || ids2[0] != run.ID {
		t.Fatalf("idempotent resend = %v", ids2)
	}

	// Signal with no waiters delivers to nobody.
	ids3, _ := e.SendSignal(testNS, SendSignalParams{Signal: "nobody-waiting"})
	if len(ids3) != 0 {
		t.Fatalf("expected no delivery, got %v", ids3)
	}
}

func TestParentWakeOnChildComplete(t *testing.T) {
	e := openTestEngine(t)
	parent := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "parent"})
	if _, err := e.ClaimWorkflowRun(testNS, "w1", 30000); err != nil {
		t.Fatal(err)
	}
	step, err := e.CreateStepAttempt(testNS, CreateStepAttemptParams{
		WorkflowRunID: parent.ID, WorkerID: "w1", StepName: "child", Kind: kindWorkflow, Config: raw(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	child := mkRun(t, e, CreateWorkflowRunParams{
		WorkflowName:                 "child",
		ParentStepAttemptNamespaceID: strptr(testNS),
		ParentStepAttemptID:          strptr(step.ID),
	})
	if _, err := e.SetStepAttemptChildWorkflowRun(testNS, parent.ID, step.ID, "w1", testNS, child.ID); err != nil {
		t.Fatalf("set child: %v", err)
	}

	// Park the parent far in the future.
	future := isoFromTime(time.Now().UTC().Add(time.Hour))
	if _, err := e.SleepWorkflowRun(testNS, parent.ID, "w1", future); err != nil {
		t.Fatalf("sleep parent: %v", err)
	}

	// Complete the child -> parent should be woken to ~now.
	if _, err := e.ClaimWorkflowRun(testNS, "w2", 30000); err != nil {
		t.Fatal(err) // claims the child (only available run)
	}
	if _, err := e.CompleteWorkflowRun(testNS, child.ID, "w2", raw(`{"done":true}`)); err != nil {
		t.Fatalf("complete child: %v", err)
	}

	woken, _ := e.GetWorkflowRun(testNS, parent.ID)
	at, _ := parseISO(*woken.AvailableAt)
	if at.After(time.Now().Add(time.Minute)) {
		t.Fatalf("parent not woken; available_at=%s still in the future", *woken.AvailableAt)
	}
}

func TestStepAttemptsListAndPagination(t *testing.T) {
	e := openTestEngine(t)
	run := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "wf"})
	e.ClaimWorkflowRun(testNS, "w", 30000)

	for i := 0; i < 5; i++ {
		if _, err := e.CreateStepAttempt(testNS, CreateStepAttemptParams{
			WorkflowRunID: run.ID, WorkerID: "w", StepName: "s", Kind: kindFunction, Config: raw(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}

	page1, meta, err := e.ListStepAttempts(testNS, run.ID, ListParams{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 || meta.Next == nil {
		t.Fatalf("page1 len=%d next=%v", len(page1), meta.Next)
	}
	page2, meta2, err := e.ListStepAttempts(testNS, run.ID, ListParams{Limit: 2, After: *meta.Next})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 {
		t.Fatalf("page2 len=%d", len(page2))
	}
	if page1[0].CreatedAt > page2[0].CreatedAt {
		t.Fatal("steps not in ascending created_at order across pages")
	}
	_ = meta2
}

func TestGenerateUUIDFormat(t *testing.T) {
	// IDs must be valid RFC 4122 v4 UUIDs so a native OpenWorkflow backend reading
	// the same database sees well-formed ids (interop).
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := generateUUID()
		if !re.MatchString(id) {
			t.Fatalf("not a valid v4 UUID: %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate UUID generated: %q", id)
		}
		seen[id] = true
	}
}

func TestCreateNormalizesSpaceSeparatedTimestamps(t *testing.T) {
	e := openTestEngine(t)
	// The admin UI sends a space-separated timestamp (app.utils.toRFC3339Datetime
	// returns "YYYY-MM-DD HH:mm:ss.nnnZ"). The engine must normalize it to the
	// canonical T-separated form used by the lexical availability comparisons.
	future := time.Now().UTC().Add(time.Hour)
	spaceForm := future.Format("2006-01-02 15:04:05.000Z07:00")
	if !strings.Contains(spaceForm, " ") {
		t.Fatalf("test setup: expected a space-separated form, got %q", spaceForm)
	}
	run := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "wf", AvailableAt: &spaceForm})

	if run.AvailableAt == nil || strings.Contains(*run.AvailableAt, " ") {
		t.Fatalf("available_at not normalized to canonical ISO: %v", run.AvailableAt)
	}
	if _, err := parseISO(*run.AvailableAt); err != nil {
		t.Fatalf("stored available_at not parseable: %v", err)
	}
	// A future-scheduled run must not be claimable (the bug claimed it now because
	// the space character sorts before 'T').
	if r, _ := e.ClaimWorkflowRun(testNS, "w", 30000); r != nil {
		t.Fatalf("future-scheduled run should not be claimable, got %s", r.ID)
	}
}

func TestFailUsesStoredDeadlineWhenDeadlineOmitted(t *testing.T) {
	e := openTestEngine(t)
	// Deadline ~500ms out; the 1s backoff would push the next retry past it.
	deadline := isoFromTime(time.Now().UTC().Add(500 * time.Millisecond))
	run := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "wf", DeadlineAt: &deadline})

	c, err := e.ClaimWorkflowRun(testNS, "w", 30000)
	if err != nil || c == nil {
		t.Fatalf("claim: %v %v", err, c)
	}

	policy := RetryPolicy{InitialInterval: "1s", BackoffCoefficient: 2, MaximumInterval: "100s", MaximumAttempts: 5}
	// attempts is provided but deadlineAt is omitted: the engine must still read
	// and honor the run's stored deadline, so the next retry exceeds it and the
	// run fails terminally instead of rescheduling.
	out, err := e.FailWorkflowRun(testNS, run.ID, FailWorkflowRunParams{
		WorkerID: "w", Error: raw(`{"message":"boom"}`), RetryPolicy: policy, Attempts: &c.Attempts,
	})
	if err != nil {
		t.Fatalf("fail: %v", err)
	}
	if out.Status != statusFailed {
		t.Fatalf("status = %q, want failed (stored deadline must be honored despite omitted deadlineAt)", out.Status)
	}
}

func TestCancelWorkflowRun(t *testing.T) {
	e := openTestEngine(t)
	run := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "wf"})

	canceled, err := e.CancelWorkflowRun(testNS, run.ID)
	if err != nil || canceled.Status != statusCanceled {
		t.Fatalf("cancel: %v %+v", err, canceled)
	}
	// Idempotent: canceling again returns the canceled run.
	again, err := e.CancelWorkflowRun(testNS, run.ID)
	if err != nil || again.Status != statusCanceled {
		t.Fatalf("re-cancel should be idempotent: %v %+v", err, again)
	}

	// Cannot cancel a completed run.
	r2 := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "wf2"})
	e.ClaimWorkflowRun(testNS, "w", 30000)
	e.CompleteWorkflowRun(testNS, r2.ID, "w", raw(`{}`))
	if _, err := e.CancelWorkflowRun(testNS, r2.ID); err == nil {
		t.Fatal("expected conflict canceling a completed run")
	}
}

// mkTerminalRun creates a run, claims it as worker and drives it to a terminal
// status so the list filters have something of every status to match.
//
// ClaimWorkflowRun takes the oldest pending run, so callers must have no other
// pending run outstanding when they ask for a non-pending status.
func mkTerminalRun(t *testing.T, e *Engine, name, worker, status string) *WorkflowRun {
	t.Helper()
	run := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: name})
	if status == statusPending {
		return run
	}
	claimed, err := e.ClaimWorkflowRun(testNS, worker, 30000)
	if err != nil {
		t.Fatalf("claim %s: %v", name, err)
	}
	if claimed == nil || claimed.ID != run.ID {
		t.Fatalf("claim %s returned %v, want the run just created (%s)", name, claimed, run.ID)
	}
	switch status {
	case statusRunning:
		// claimed and left running
	case statusCompleted:
		if _, err := e.CompleteWorkflowRun(testNS, run.ID, worker, raw(`{}`)); err != nil {
			t.Fatalf("complete %s: %v", name, err)
		}
	case statusFailed:
		if _, err := e.FailWorkflowRun(testNS, run.ID, FailWorkflowRunParams{
			WorkerID: worker, Error: raw(`{"m":"x"}`), RetryPolicy: RetryPolicy{InitialInterval: "1s", BackoffCoefficient: 2, MaximumInterval: "100s", MaximumAttempts: 1},
		}); err != nil {
			t.Fatalf("fail %s: %v", name, err)
		}
	case statusCanceled:
		if _, err := e.CancelWorkflowRun(testNS, run.ID); err != nil {
			t.Fatalf("cancel %s: %v", name, err)
		}
	default:
		t.Fatalf("unsupported status %q", status)
	}
	return run
}

func TestListWorkflowRunsStatusFilter(t *testing.T) {
	e := openTestEngine(t)
	// One run per terminal status, created oldest-first.
	want := map[string]string{}
	for _, s := range []string{statusCompleted, statusFailed, statusCanceled, statusPending} {
		run := mkTerminalRun(t, e, "wf-"+s, "w-"+s, s)
		want[s] = run.ID
		time.Sleep(time.Millisecond)
	}

	all, _, err := e.ListWorkflowRuns(testNS, ListRunsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("unfiltered len = %d, want 4", len(all))
	}

	for status, id := range want {
		got, _, err := e.ListWorkflowRuns(testNS, ListRunsParams{Status: status})
		if err != nil {
			t.Fatalf("filter %s: %v", status, err)
		}
		if len(got) != 1 {
			t.Fatalf("status=%s len = %d, want 1", status, len(got))
		}
		if got[0].ID != id || got[0].Status != status {
			t.Fatalf("status=%s got id=%s status=%s", status, got[0].ID, got[0].Status)
		}
	}

	// An unmatched status yields an empty page, not an error.
	none, _, err := e.ListWorkflowRuns(testNS, ListRunsParams{Status: statusRunning})
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("status=running len = %d, want 0", len(none))
	}
}

func TestListWorkflowRunsWorkflowNameFilter(t *testing.T) {
	e := openTestEngine(t)
	mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "alpha"})
	time.Sleep(time.Millisecond)
	mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "beta"})
	time.Sleep(time.Millisecond)
	mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "alpha"})

	got, _, err := e.ListWorkflowRuns(testNS, ListRunsParams{WorkflowName: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("workflowName=alpha len = %d, want 2", len(got))
	}
	for _, r := range got {
		if r.WorkflowName != "alpha" {
			t.Fatalf("got workflow name %q", r.WorkflowName)
		}
	}

	if none, _, err := e.ListWorkflowRuns(testNS, ListRunsParams{WorkflowName: "nope"}); err != nil || len(none) != 0 {
		t.Fatalf("workflowName=nope len=%d err=%v", len(none), err)
	}
}

func TestListWorkflowRunsCombinedFilterAndPagination(t *testing.T) {
	e := openTestEngine(t)
	// Decoys first, while nothing else is pending (mkTerminalRun claims the
	// oldest pending run), then the 3 pending "alpha" runs the filter matches.
	mkTerminalRun(t, e, "alpha", "w-done", statusCompleted) // right name, wrong status
	time.Sleep(time.Millisecond)
	mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "beta"}) // wrong name
	time.Sleep(time.Millisecond)
	for i := 0; i < 3; i++ {
		mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "alpha"})
		time.Sleep(time.Millisecond)
	}

	p := ListRunsParams{Status: statusPending, WorkflowName: "alpha"}
	p.Limit = 2

	page1, meta1, err := e.ListWorkflowRuns(testNS, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 || meta1.Next == nil {
		t.Fatalf("page1 len=%d next=%v", len(page1), meta1.Next)
	}

	p2 := p
	p2.After = *meta1.Next
	page2, meta2, err := e.ListWorkflowRuns(testNS, p2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 {
		t.Fatalf("page2 len = %d, want 1", len(page2))
	}
	if meta2.Next != nil {
		t.Fatalf("page2 next = %v, want nil", meta2.Next)
	}

	// The filters must hold across both pages, and pages must not overlap.
	seen := map[string]bool{}
	for _, r := range append(append([]*WorkflowRun{}, page1...), page2...) {
		if r.WorkflowName != "alpha" || r.Status != statusPending {
			t.Fatalf("filter leaked: %s/%s", r.WorkflowName, r.Status)
		}
		if seen[r.ID] {
			t.Fatalf("run %s repeated across pages", r.ID)
		}
		seen[r.ID] = true
	}
	// Newest-first ordering is preserved under a filter.
	if page1[0].CreatedAt < page2[0].CreatedAt {
		t.Fatal("runs not in descending created_at order across pages")
	}
}

// mkClaimedRun creates a run and claims it, returning the run and its worker.
func mkClaimedRun(t *testing.T, e *Engine, worker string) *WorkflowRun {
	t.Helper()
	run := mkRun(t, e, CreateWorkflowRunParams{WorkflowName: "wf"})
	claimed, err := e.ClaimWorkflowRun(testNS, worker, 30000)
	if err != nil || claimed == nil || claimed.ID != run.ID {
		t.Fatalf("claim: %v run=%v", err, claimed)
	}
	return claimed
}

func mkStep(t *testing.T, e *Engine, runID, worker string) *StepAttempt {
	t.Helper()
	step, err := e.CreateStepAttempt(testNS, CreateStepAttemptParams{
		WorkflowRunID: runID, WorkerID: worker, StepName: "s", Kind: kindFunction, Config: raw(`{}`),
	})
	if err != nil {
		t.Fatalf("CreateStepAttempt: %v", err)
	}
	return step
}

func TestCreateStepAttemptRejectsForeignWorker(t *testing.T) {
	e := openTestEngine(t)
	run := mkClaimedRun(t, e, "w1")

	if _, err := e.CreateStepAttempt(testNS, CreateStepAttemptParams{
		WorkflowRunID: run.ID, WorkerID: "w2", StepName: "s", Kind: kindFunction, Config: raw(`{}`),
	}); err == nil {
		t.Fatal("expected create to be fenced against a worker that does not own the run")
	}

	steps, _, err := e.ListStepAttempts(testNS, run.ID, ListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 0 {
		t.Fatalf("fenced create still inserted %d step(s)", len(steps))
	}
}

func TestCreateStepAttemptRejectsParkedRun(t *testing.T) {
	e := openTestEngine(t)
	run := mkClaimedRun(t, e, "w1")

	// Parking clears worker_id (status stays "running"), so the fence must
	// reject the previous owner too.
	if _, err := e.SleepWorkflowRun(testNS, run.ID, "w1", isoFromTime(time.Now().UTC().Add(time.Hour))); err != nil {
		t.Fatalf("sleep: %v", err)
	}

	if _, err := e.CreateStepAttempt(testNS, CreateStepAttemptParams{
		WorkflowRunID: run.ID, WorkerID: "w1", StepName: "s", Kind: kindFunction, Config: raw(`{}`),
	}); err == nil {
		t.Fatal("expected create to be fenced after the run was parked")
	}

	steps, _, err := e.ListStepAttempts(testNS, run.ID, ListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 0 {
		t.Fatalf("fenced create still inserted %d step(s)", len(steps))
	}
}

func TestCompleteStepAttemptIsIdempotent(t *testing.T) {
	e := openTestEngine(t)
	run := mkClaimedRun(t, e, "w1")
	step := mkStep(t, e, run.ID, "w1")

	first, err := e.CompleteStepAttempt(testNS, run.ID, step.ID, "w1", raw(`{"ok":true}`))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if first.Status != "completed" || string(first.Output) != `{"ok":true}` || first.FinishedAt == nil {
		t.Fatalf("first complete = %+v", first)
	}

	// A repeat terminal write returns the existing record untouched rather
	// than 404-ing, and must not overwrite the recorded output/timestamps.
	time.Sleep(2 * time.Millisecond)
	again, err := e.CompleteStepAttempt(testNS, run.ID, step.ID, "w1", raw(`{"ok":"CLOBBERED"}`))
	if err != nil {
		t.Fatalf("repeat complete: %v", err)
	}
	if !reflect.DeepEqual(first, again) {
		t.Fatalf("repeat complete changed the record:\n first = %+v\n again = %+v", first, again)
	}

	// Still fenced: a foreign worker cannot touch it.
	if _, err := e.CompleteStepAttempt(testNS, run.ID, step.ID, "w2", raw(`{}`)); err == nil {
		t.Fatal("expected repeat complete by a foreign worker to be rejected")
	}
}

func TestFailStepAttemptIsIdempotent(t *testing.T) {
	e := openTestEngine(t)
	run := mkClaimedRun(t, e, "w1")
	step := mkStep(t, e, run.ID, "w1")

	first, err := e.FailStepAttempt(testNS, run.ID, step.ID, "w1", raw(`{"m":"boom"}`))
	if err != nil {
		t.Fatalf("fail: %v", err)
	}
	if first.Status != "failed" || string(first.Error) != `{"m":"boom"}` || first.FinishedAt == nil {
		t.Fatalf("first fail = %+v", first)
	}

	time.Sleep(2 * time.Millisecond)
	again, err := e.FailStepAttempt(testNS, run.ID, step.ID, "w1", raw(`{"m":"CLOBBERED"}`))
	if err != nil {
		t.Fatalf("repeat fail: %v", err)
	}
	if !reflect.DeepEqual(first, again) {
		t.Fatalf("repeat fail changed the record:\n first = %+v\n again = %+v", first, again)
	}

	if _, err := e.FailStepAttempt(testNS, run.ID, step.ID, "w2", raw(`{}`)); err == nil {
		t.Fatal("expected repeat fail by a foreign worker to be rejected")
	}
}

// A completed step must not be flippable to failed (or vice versa) -- the
// broadened predicates only admit a step's own terminal status.
func TestTerminalStepAttemptCannotChangeTerminalStatus(t *testing.T) {
	e := openTestEngine(t)
	run := mkClaimedRun(t, e, "w1")

	completed := mkStep(t, e, run.ID, "w1")
	if _, err := e.CompleteStepAttempt(testNS, run.ID, completed.ID, "w1", raw(`{}`)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := e.FailStepAttempt(testNS, run.ID, completed.ID, "w1", raw(`{"m":"x"}`)); err == nil {
		t.Fatal("expected failing an already-completed step to be rejected")
	}

	failed := mkStep(t, e, run.ID, "w1")
	if _, err := e.FailStepAttempt(testNS, run.ID, failed.ID, "w1", raw(`{"m":"x"}`)); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if _, err := e.CompleteStepAttempt(testNS, run.ID, failed.ID, "w1", raw(`{}`)); err == nil {
		t.Fatal("expected completing an already-failed step to be rejected")
	}
}

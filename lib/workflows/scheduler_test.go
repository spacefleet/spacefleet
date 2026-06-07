package workflows

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// recordingState collects the (id, status) transitions onState reports, so a test
// can assert what was persisted. Concurrency-safe.
type recordingState struct {
	mu     sync.Mutex
	states map[uuid.UUID][]string
}

func newRecordingState() *recordingState {
	return &recordingState{states: map[uuid.UUID][]string{}}
}

func (r *recordingState) on(id uuid.UUID, status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states[id] = append(r.states[id], status)
}

func (r *recordingState) last(id uuid.UUID) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.states[id]
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}

func (r *recordingState) saw(id uuid.UUID, status string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.states[id] {
		if s == status {
			return true
		}
	}
	return false
}

// outcomeRunFn returns a runFn that reports each node's terminal status from a map
// keyed by id (default succeeded), recording into the state. It is deterministic.
func outcomeRunFn(outcomes map[uuid.UUID]string, ran *recordingState) func(context.Context, schedNode) nodeResult {
	return func(_ context.Context, n schedNode) nodeResult {
		ran.on(n.ID, "ran")
		status := statusSucceeded
		if o, ok := outcomes[n.ID]; ok {
			status = o
		}
		return nodeResult{Status: status}
	}
}

func TestScheduleEmptyGraph(t *testing.T) {
	t.Parallel()
	got := schedule(context.Background(), nil, 4, func(context.Context, schedNode) nodeResult {
		t.Fatal("runFn should not be called for an empty graph")
		return nodeResult{}
	}, nil)
	if got != runSucceeded {
		t.Fatalf("empty graph: got %q, want %q", got, runSucceeded)
	}
}

func TestScheduleLinearChain(t *testing.T) {
	t.Parallel()
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	nodes := []schedNode{
		{ID: a},
		{ID: b, DependsOn: []uuid.UUID{a}},
		{ID: c, DependsOn: []uuid.UUID{b}},
	}
	var order []uuid.UUID
	var mu sync.Mutex
	st := newRecordingState()
	runFn := func(_ context.Context, n schedNode) nodeResult {
		mu.Lock()
		order = append(order, n.ID)
		mu.Unlock()
		return nodeResult{Status: statusSucceeded}
	}
	got := schedule(context.Background(), nodes, 4, runFn, st.on)
	if got != runSucceeded {
		t.Fatalf("linear chain: got %q, want succeeded", got)
	}
	want := []uuid.UUID{a, b, c}
	if len(order) != 3 || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Fatalf("linear chain ran out of order: %v", order)
	}
}

func TestScheduleDiamondParallelMiddle(t *testing.T) {
	t.Parallel()
	top := uuid.New()
	left, right := uuid.New(), uuid.New()
	bottom := uuid.New()
	nodes := []schedNode{
		{ID: top},
		{ID: left, DependsOn: []uuid.UUID{top}},
		{ID: right, DependsOn: []uuid.UUID{top}},
		{ID: bottom, DependsOn: []uuid.UUID{left, right}},
	}

	var active, maxActive int32
	// A two-party barrier: each middle node arrives and waits until both have, so
	// they are forced to overlap if the scheduler runs them concurrently (and the
	// test times out into failure-by-low-maxActive if it serializes them).
	var arrived int32
	barrier := make(chan struct{})
	runFn := func(_ context.Context, n schedNode) nodeResult {
		cur := atomic.AddInt32(&active, 1)
		for {
			m := atomic.LoadInt32(&maxActive)
			if cur <= m || atomic.CompareAndSwapInt32(&maxActive, m, cur) {
				break
			}
		}
		if n.ID == left || n.ID == right {
			if atomic.AddInt32(&arrived, 1) == 2 {
				close(barrier)
			}
			select {
			case <-barrier:
			case <-time.After(time.Second):
			}
		}
		atomic.AddInt32(&active, -1)
		return nodeResult{Status: statusSucceeded}
	}
	got := schedule(context.Background(), nodes, 4, runFn, nil)
	if got != runSucceeded {
		t.Fatalf("diamond: got %q, want succeeded", got)
	}
	if maxActive < 2 {
		t.Fatalf("diamond middle did not run concurrently: maxActive=%d", maxActive)
	}
}

func TestScheduleHardFailureSkipsDependents(t *testing.T) {
	t.Parallel()
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	nodes := []schedNode{
		{ID: a},
		{ID: b, DependsOn: []uuid.UUID{a}}, // depends on the failing node
		{ID: c, DependsOn: []uuid.UUID{b}}, // transitive dependent
	}
	st := newRecordingState()
	ran := newRecordingState()
	runFn := outcomeRunFn(map[uuid.UUID]string{a: statusFailed}, ran)
	got := schedule(context.Background(), nodes, 4, runFn, st.on)
	if got != runFailed {
		t.Fatalf("hard failure: got %q, want failed", got)
	}
	if ran.saw(b, "ran") || ran.saw(c, "ran") {
		t.Fatalf("dependents of a hard failure must not run: b=%v c=%v", ran.saw(b, "ran"), ran.saw(c, "ran"))
	}
	if st.last(b) != statusSkipped || st.last(c) != statusSkipped {
		t.Fatalf("dependents must be skipped: b=%q c=%q", st.last(b), st.last(c))
	}
}

func TestScheduleContinueOnFailureLetsDependentsRun(t *testing.T) {
	t.Parallel()
	a, b := uuid.New(), uuid.New()
	nodes := []schedNode{
		{ID: a, ContinueOnFailure: true},
		{ID: b, DependsOn: []uuid.UUID{a}},
	}
	st := newRecordingState()
	ran := newRecordingState()
	runFn := outcomeRunFn(map[uuid.UUID]string{a: statusFailed}, ran)
	got := schedule(context.Background(), nodes, 4, runFn, st.on)
	if got != runPartial {
		t.Fatalf("continue-on-failure: got %q, want partial", got)
	}
	if !ran.saw(b, "ran") {
		t.Fatal("dependent of a continue-on-failure node must still run")
	}
	if st.last(b) != statusSucceeded {
		t.Fatalf("dependent should have succeeded: %q", st.last(b))
	}
}

func TestScheduleIndependentNodesRunConcurrently(t *testing.T) {
	t.Parallel()
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	nodes := make([]schedNode, len(ids))
	for i, id := range ids {
		nodes[i] = schedNode{ID: id}
	}
	var active, maxActive int32
	start := make(chan struct{})
	var ready int32
	runFn := func(_ context.Context, _ schedNode) nodeResult {
		cur := atomic.AddInt32(&active, 1)
		for {
			m := atomic.LoadInt32(&maxActive)
			if cur <= m || atomic.CompareAndSwapInt32(&maxActive, m, cur) {
				break
			}
		}
		// Block all until every node has entered, proving true concurrency.
		if atomic.AddInt32(&ready, 1) == int32(len(ids)) {
			close(start)
		}
		select {
		case <-start:
		case <-time.After(time.Second):
		}
		atomic.AddInt32(&active, -1)
		return nodeResult{Status: statusSucceeded}
	}
	got := schedule(context.Background(), nodes, len(ids), runFn, nil)
	if got != runSucceeded {
		t.Fatalf("independent nodes: got %q, want succeeded", got)
	}
	if int(maxActive) != len(ids) {
		t.Fatalf("independent nodes did not all run concurrently: maxActive=%d want=%d", maxActive, len(ids))
	}
}

func TestScheduleConcurrencyBound(t *testing.T) {
	t.Parallel()
	const n = 6
	const bound = 2
	nodes := make([]schedNode, n)
	for i := range nodes {
		nodes[i] = schedNode{ID: uuid.New()}
	}
	var active, maxActive int32
	runFn := func(_ context.Context, _ schedNode) nodeResult {
		cur := atomic.AddInt32(&active, 1)
		for {
			m := atomic.LoadInt32(&maxActive)
			if cur <= m || atomic.CompareAndSwapInt32(&maxActive, m, cur) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return nodeResult{Status: statusSucceeded}
	}
	got := schedule(context.Background(), nodes, bound, runFn, nil)
	if got != runSucceeded {
		t.Fatalf("bounded: got %q, want succeeded", got)
	}
	if maxActive > bound {
		t.Fatalf("concurrency exceeded the bound: maxActive=%d bound=%d", maxActive, bound)
	}
}

func TestScheduleSkippedDepPropagates(t *testing.T) {
	t.Parallel()
	// a fails (hard) → b skipped → c (depends on b) skipped transitively, and a
	// node d depending on a skipped node is also skipped.
	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	nodes := []schedNode{
		{ID: a},
		{ID: b, DependsOn: []uuid.UUID{a}},
		{ID: c, DependsOn: []uuid.UUID{b}},
		{ID: d, DependsOn: []uuid.UUID{c}},
	}
	st := newRecordingState()
	ran := newRecordingState()
	runFn := outcomeRunFn(map[uuid.UUID]string{a: statusFailed}, ran)
	got := schedule(context.Background(), nodes, 4, runFn, st.on)
	if got != runFailed {
		t.Fatalf("got %q, want failed", got)
	}
	for _, id := range []uuid.UUID{b, c, d} {
		if ran.saw(id, "ran") {
			t.Fatalf("node %s should have been skipped, not run", id)
		}
		if st.last(id) != statusSkipped {
			t.Fatalf("node %s should be skipped: %q", id, st.last(id))
		}
	}
}

func TestScheduleAllSucceed(t *testing.T) {
	t.Parallel()
	a, b := uuid.New(), uuid.New()
	nodes := []schedNode{{ID: a}, {ID: b, DependsOn: []uuid.UUID{a}}}
	st := newRecordingState()
	got := schedule(context.Background(), nodes, 4, outcomeRunFn(nil, newRecordingState()), st.on)
	if got != runSucceeded {
		t.Fatalf("got %q, want succeeded", got)
	}
	if st.last(a) != statusSucceeded || st.last(b) != statusSucceeded {
		t.Fatalf("both should succeed: a=%q b=%q", st.last(a), st.last(b))
	}
	// running must be emitted before the terminal status for a run node.
	if !st.saw(a, statusRunning) {
		t.Fatal("expected a 'running' transition for node a")
	}
}

func TestScheduleGateParksRunSuspended(t *testing.T) {
	t.Parallel()
	// a -> b(gated) -> c. b parks; b is never run, c waits behind it. Run suspends.
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	nodes := []schedNode{
		{ID: a},
		{ID: b, DependsOn: []uuid.UUID{a}, RequiresApproval: true},
		{ID: c, DependsOn: []uuid.UUID{b}},
	}
	st := newRecordingState()
	ran := newRecordingState()
	got := schedule(context.Background(), nodes, 4, outcomeRunFn(nil, ran), st.on)
	if got != runSuspended {
		t.Fatalf("gated run: got %q, want suspended", got)
	}
	if !st.saw(b, statusAwaitingApproval) {
		t.Fatal("gated node b must emit awaiting_approval")
	}
	// awaiting_approval emitted exactly once.
	st.mu.Lock()
	cnt := 0
	for _, s := range st.states[b] {
		if s == statusAwaitingApproval {
			cnt++
		}
	}
	st.mu.Unlock()
	if cnt != 1 {
		t.Fatalf("awaiting_approval must be emitted exactly once: got %d", cnt)
	}
	if ran.saw(b, "ran") {
		t.Fatal("parked node b must not run")
	}
	if ran.saw(c, "ran") {
		t.Fatal("dependent of a parked node must not run")
	}
	if st.last(c) == statusSkipped {
		t.Fatal("dependent of a parked node must wait, not be skipped")
	}
	if st.last(a) != statusSucceeded {
		t.Fatalf("upstream node a should have succeeded: %q", st.last(a))
	}
}

func TestScheduleSiblingsDrainBeforeSuspend(t *testing.T) {
	t.Parallel()
	// gate(gated) and sib are independent. sib must fully run even though gate parks.
	gate, sib := uuid.New(), uuid.New()
	nodes := []schedNode{
		{ID: gate, RequiresApproval: true},
		{ID: sib},
	}
	st := newRecordingState()
	ran := newRecordingState()
	got := schedule(context.Background(), nodes, 4, outcomeRunFn(nil, ran), st.on)
	if got != runSuspended {
		t.Fatalf("got %q, want suspended", got)
	}
	if !ran.saw(sib, "ran") {
		t.Fatal("sibling must drain even though another branch parked")
	}
	if st.last(sib) != statusSucceeded {
		t.Fatalf("sibling should have succeeded: %q", st.last(sib))
	}
	if ran.saw(gate, "ran") {
		t.Fatal("gated node must not run")
	}
}

func TestScheduleHardFailureOutranksSuspend(t *testing.T) {
	t.Parallel()
	// A parallel branch hard-fails while another branch parks: the run is failed.
	gate, boom := uuid.New(), uuid.New()
	nodes := []schedNode{
		{ID: gate, RequiresApproval: true},
		{ID: boom},
	}
	ran := newRecordingState()
	got := schedule(context.Background(), nodes, 4, outcomeRunFn(map[uuid.UUID]string{boom: statusFailed}, ran), nil)
	if got != runFailed {
		t.Fatalf("hard failure must outrank suspend: got %q, want failed", got)
	}
}

func TestScheduleApprovedGateLaunchesAndProceeds(t *testing.T) {
	t.Parallel()
	// Models a resume: the same DAG re-scheduled with Approved=true on the gated node.
	// b now launches and its dependent c proceeds — the run succeeds.
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	nodes := []schedNode{
		{ID: a},
		{ID: b, DependsOn: []uuid.UUID{a}, RequiresApproval: true, Approved: true},
		{ID: c, DependsOn: []uuid.UUID{b}},
	}
	st := newRecordingState()
	ran := newRecordingState()
	got := schedule(context.Background(), nodes, 4, outcomeRunFn(nil, ran), st.on)
	if got != runSucceeded {
		t.Fatalf("approved gate: got %q, want succeeded", got)
	}
	if !ran.saw(b, "ran") {
		t.Fatal("an approved gated node must run")
	}
	if !ran.saw(c, "ran") {
		t.Fatal("downstream of an approved gate must run")
	}
	if st.saw(b, statusAwaitingApproval) {
		t.Fatal("an approved gate must not park")
	}
	if st.last(c) != statusSucceeded {
		t.Fatalf("downstream should have succeeded: %q", st.last(c))
	}
}

func TestScheduleNotRequiredApprovalRunsAsToday(t *testing.T) {
	t.Parallel()
	// Approved=false but RequiresApproval=false: behaves exactly as an ungated node.
	a, b := uuid.New(), uuid.New()
	nodes := []schedNode{
		{ID: a, RequiresApproval: false, Approved: false},
		{ID: b, DependsOn: []uuid.UUID{a}},
	}
	st := newRecordingState()
	ran := newRecordingState()
	got := schedule(context.Background(), nodes, 4, outcomeRunFn(nil, ran), st.on)
	if got != runSucceeded {
		t.Fatalf("ungated run: got %q, want succeeded", got)
	}
	if !ran.saw(a, "ran") || !ran.saw(b, "ran") {
		t.Fatal("ungated nodes must run")
	}
	if st.saw(a, statusAwaitingApproval) {
		t.Fatal("an ungated node must not park")
	}
}

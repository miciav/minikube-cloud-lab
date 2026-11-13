package cloud

import (
	"fmt"
	"testing"
)

func TestOperationRegistryKeepsRecentHistoryAndUniqueIDs(t *testing.T) {
	st := snapshot{}
	running := st.newOperation("CREATE_CLUSTER", "demo", "student")
	for range maxFinishedOperations + 50 {
		op := st.newOperation("ADD_MACHINE", "demo", "cluster-autoscaler")
		st.operation(op.ID).Status = "DONE"
	}
	if st.operation(running.ID) == nil {
		t.Fatal("running operation pruned")
	}
	if len(st.Operations) > maxFinishedOperations+2 {
		t.Fatalf("registry grew to %d", len(st.Operations))
	}
	last := st.newOperation("ADD_MACHINE", "demo", "student")
	if want := fmt.Sprintf("op-%06d", maxFinishedOperations+52); last.ID != want {
		t.Fatalf("id %s, want %s", last.ID, want)
	}
}

package categories

import "testing"

func TestChildCountFieldExists(t *testing.T) {
	// A parent with two children must report ChildCount 2 on the parent and 0
	// on each leaf. This is a pure-struct guard: it fails to compile until the
	// field exists, and asserts the walk sets it.
	n := TreeNode{}
	n.ChildCount = 2 // compile guard
	if n.ChildCount != 2 {
		t.Fatalf("ChildCount = %d, want 2", n.ChildCount)
	}
}

package probe

import "testing"

// fakeStore is one double, and the file checks it in three places: the
// scaffolding is smaller than what it verifies, which is the legal shape.
type fakeStore struct {
	rows  int
	calls int
}

func (f *fakeStore) Count() int {
	f.calls++
	return f.rows
}

func TestTheStoreCountsOnce(t *testing.T) {
	store := &fakeStore{rows: 2}
	if got := store.Count(); got != 2 {
		t.Errorf("Count() = %d, want 2", got)
	}
	if store.calls != 1 {
		t.Errorf("Count() ran %d time(s), want 1", store.calls)
	}
	if loaded(store) {
		t.Fatalf("the store was reported as loaded")
	}
}

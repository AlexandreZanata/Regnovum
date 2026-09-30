package probe

import "testing"

// fakeStore, stubClock and mockLedger are three doubles, and the file makes a
// single assertion: the scaffolding is then what the file is about.
type fakeStore struct{ rows int }

type stubClock struct{ at int64 }

type mockLedger struct{ entries int }

func (f *fakeStore) Count() int { return f.rows }

func TestTheStoreCounts(t *testing.T) {
	if (&fakeStore{rows: 1}).Count() != 1 {
		t.Errorf("the store counted the wrong number of rows")
	}
}

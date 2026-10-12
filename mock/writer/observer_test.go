package writer_test

import (
	"errors"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/mock/writer"
)

func TestObserverFake_RecordsFlushesInOrder(t *testing.T) {
	boom := errors.New("boom")
	o := writer.NewObserverFake()

	o.ObserveFlush(2, time.Second, nil)
	o.ObserveFlush(5, time.Minute, boom)

	got := o.Flushes()
	want := []writer.Flush{{Size: 2, Duration: time.Second}, {Size: 5, Duration: time.Minute, Err: boom}}
	if len(got) != len(want) {
		t.Fatalf("flushes = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("flush %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

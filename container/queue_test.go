package container

import (
	"testing"
)

func TestServiceContainer_Queue(t *testing.T) {
	Load = NewServiceContainer()
	if db := Load.Database(); db != nil {
		defer func() { _ = db.Close() }()
	}

	q := Load.Queue()
	if q == nil {
		t.Skip("postgres not available")
	}
}

func TestServiceContainer_QueueCached(t *testing.T) {
	Load = NewServiceContainer()
	if db := Load.Database(); db != nil {
		defer func() { _ = db.Close() }()
	}

	first := Load.Queue()
	if first == nil {
		t.Skip("postgres not available")
	}
	second := Load.Queue()
	if first != second {
		t.Fatal("expected cached queue instance")
	}
}

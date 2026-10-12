package container

import (
	"testing"
)

func TestServiceContainer_DatabaseDriver(t *testing.T) {
	Load = NewServiceContainer()

	driver := Load.Database()
	if driver == nil {
		t.Skip("postgres not available")
	}
	defer func() { _ = driver.Close() }()
}

func TestServiceContainer_DatabaseDriverCached(t *testing.T) {
	Load = NewServiceContainer()

	first := Load.Database()
	if first == nil {
		t.Skip("postgres not available")
	}
	second := Load.Database()
	defer func() { _ = second.Close() }()
	if first != second {
		t.Fatal("expected cached driver instance")
	}
}

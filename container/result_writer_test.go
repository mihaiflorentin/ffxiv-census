package container

import "testing"

func TestServiceContainer_ResultWriterIsASharedPerProcessInstance(t *testing.T) {
	Load = NewServiceContainer()
	if Load.Database() == nil {
		t.Skip("postgres not available")
	}

	w := Load.ResultWriter()
	if w == nil {
		t.Fatal("ResultWriter nil")
	}
	if Load.ResultWriter() != w {
		t.Fatal("ResultWriter must return the one per-process instance")
	}
}

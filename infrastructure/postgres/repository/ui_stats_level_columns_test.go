package repository

import "testing"

func TestLevelCountColumnsWithoutBandsIsAnError(t *testing.T) {
	cols, args, err := levelCountColumns(nil, 3)
	if err == nil {
		t.Fatalf("levelCountColumns(no bands) = %q, %v, want an error", cols, args)
	}
}

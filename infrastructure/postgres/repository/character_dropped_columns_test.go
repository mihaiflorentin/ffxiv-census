package repository_test

import (
	"context"
	"testing"
)

// Item level and gear were never filled (the parser never read them), so
// migration 00032 drops them.
func TestMigrations_DropItemLevelAndGear(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()

	var columns int
	row, err := driver.FetchOne(ctx,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_name = 'characters' AND column_name = 'item_level'`)
	if err != nil {
		t.Fatal(err)
	}
	if err := row.Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 0 {
		t.Error("characters.item_level still exists")
	}

	var gear bool
	row, err = driver.FetchOne(ctx, `SELECT to_regclass('character_gear') IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	if err := row.Scan(&gear); err != nil {
		t.Fatal(err)
	}
	if gear {
		t.Error("table character_gear still exists")
	}
}

package main

import "testing"

func TestDemoRuns(t *testing.T) {
	if err := run(nil); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"nope"}); err == nil {
		t.Fatal("unknown scenario accepted")
	}
}

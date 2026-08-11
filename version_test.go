package main

import "testing"

func TestBuildVersionNotEmpty(t *testing.T) {
	if buildVersion() == "" {
		t.Fatal("build version is empty")
	}
}

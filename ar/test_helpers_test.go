package ar_test

import (
	"errors"
	"reflect"
	"testing"
)

func mustNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func mustEqual(t *testing.T, want, got interface{}) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("values differ: want %#v, got %#v", want, got)
	}
}

func mustError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
}

func mustErrorIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error %v does not match %v", err, target)
	}
}

func mustNotNil(t *testing.T, value interface{}) {
	t.Helper()
	if value == nil || (reflect.ValueOf(value).Kind() == reflect.Ptr && reflect.ValueOf(value).IsNil()) {
		t.Fatal("unexpected nil value")
	}
}

func mustLen(t *testing.T, value interface{}, want int) {
	t.Helper()
	got := reflect.ValueOf(value).Len()
	if got != want {
		t.Fatalf("length differs: want %d, got %d", want, got)
	}
}

func mustNotPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("unexpected panic: %v", recovered)
		}
	}()
	fn()
}

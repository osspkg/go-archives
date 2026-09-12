package zst_test

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

func mustNil(t *testing.T, value interface{}) {
	t.Helper()
	if !isNil(value) {
		t.Fatalf("expected nil, got %#v", value)
	}
}

func isNil(value interface{}) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func mustLessOrEqual(t *testing.T, got, want int) {
	t.Helper()
	if got > want {
		t.Fatalf("value %d is greater than %d", got, want)
	}
}

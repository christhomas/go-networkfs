package cgo

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestJSONMarshalBasic(t *testing.T) {
	out := JSONMarshal(map[string]int{"a": 1, "b": 2})
	var got map[string]int
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("produced invalid JSON: %v (%s)", err, out)
	}
	if got["a"] != 1 || got["b"] != 2 {
		t.Fatalf("round-trip lost data: %+v", got)
	}
}

func TestJSONMarshalFallbackOnUnmarshalable(t *testing.T) {
	// Channels aren't marshalable; helper must return an {"error":...}
	// doc instead of panicking.
	out := JSONMarshal(map[string]interface{}{"ch": make(chan int)})
	var got map[string]string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("fallback is not valid JSON: %s", out)
	}
	if _, ok := got["error"]; !ok {
		t.Fatalf("expected error field in fallback, got %v", got)
	}
}

func TestJSONUnmarshal(t *testing.T) {
	var dst map[string]int
	if err := JSONUnmarshal([]byte(`{"x":3}`), &dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dst["x"] != 3 {
		t.Fatalf("got %v", dst)
	}

	if err := JSONUnmarshal([]byte(`not json`), &dst); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestErrorResult(t *testing.T) {
	out := ErrorResult(42, "bad things")
	s := string(out)
	if !strings.Contains(s, `"code":42`) {
		t.Errorf("missing code: %s", s)
	}
	if !strings.Contains(s, `"message":"bad things"`) {
		t.Errorf("missing message: %s", s)
	}
	if !strings.Contains(s, `"success":false`) {
		t.Errorf("missing success flag: %s", s)
	}
}

func TestSuccessResult(t *testing.T) {
	if string(SuccessResult()) != `{"success":true}` {
		t.Fatalf("unexpected success payload: %s", SuccessResult())
	}
}

func TestJSONMarshalNumberPrecision(t *testing.T) {
	// Make sure the helper doesn't lose precision on large numbers.
	out := JSONMarshal(map[string]int64{"n": math.MaxInt64})
	if !strings.Contains(string(out), "9223372036854775807") {
		t.Fatalf("lost int64 precision: %s", out)
	}
}

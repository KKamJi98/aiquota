package main

import (
	"testing"
	"time"
)

func TestNormalizeInterval(t *testing.T) {
	cases := []struct {
		sec  int
		want time.Duration
	}{
		{0, 60 * time.Second},    // unset -> default
		{-5, 60 * time.Second},   // negative -> default
		{1, 2 * time.Second},     // below floor -> floor
		{2, 2 * time.Second},     // floor
		{3, 3 * time.Second},     // passthrough
		{300, 300 * time.Second}, // passthrough
	}
	for _, c := range cases {
		if got := normalizeInterval(c.sec); got != c.want {
			t.Errorf("normalizeInterval(%d) = %v, want %v", c.sec, got, c.want)
		}
	}
}

package main

import (
	"testing"
	"time"
)

func f(v float64) *float64 { return &v }

func TestToMeasurement(t *testing.T) {
	now := time.Date(2026, 10, 1, 18, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		in      liveMeasurement
		ok      bool
		l2, l3  float64
		wantObs string
	}{
		{"dreiphasig", liveMeasurement{Timestamp: "2026-10-01T20:48:47.000+02:00", V1: f(231.2), V2: f(232.1), V3: f(231)}, true, 232.1, 231, "2026-10-01T18:48:47Z"},
		{"einphasig", liveMeasurement{V1: f(231.2)}, true, -1, -1, "2026-10-01T18:00:00Z"},
		{"ohne L1", liveMeasurement{V2: f(232.1), V3: f(231)}, false, 0, 0, ""},
		{"L1 zu hoch", liveMeasurement{V1: f(480), V2: f(232.1), V3: f(231)}, false, 0, 0, ""},
		{"L3 zu niedrig", liveMeasurement{V1: f(231), V2: f(232.1), V3: f(0)}, false, 0, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := toMeasurement(tt.in, 52.520, 13.405, now)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if got.L2V != tt.l2 || got.L3V != tt.l3 || got.ObservedAt != tt.wantObs {
				t.Fatalf("got L2=%v L3=%v observed_at=%s", got.L2V, got.L3V, got.ObservedAt)
			}
		})
	}
}

package api

import (
	"testing"
	"time"
)

func TestMakeParticipationRateSparklineKeepsMissingBucketsNull(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	got := makeParticipationRateSparkline([]rateObservation{
		{At: start, Participated: true},
		{At: start.Add(11 * time.Hour), Participated: false},
	})
	if len(got) != 12 {
		t.Fatalf("sparkline length = %d, want 12", len(got))
	}
	if got[0].Value == nil || *got[0].Value != 100 {
		t.Fatalf("first rate = %v, want 100", got[0].Value)
	}
	if got[1].Value != nil {
		t.Fatalf("empty bucket value = %v, want null", *got[1].Value)
	}
	if got[11].Value == nil || *got[11].Value != 0 {
		t.Fatalf("last rate = %v, want recorded 0", got[11].Value)
	}
}

func TestMakeParticipationRateSparklineAggregatesRecordedDenominator(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	got := makeParticipationRateSparkline([]rateObservation{
		{At: start, Participated: true},
		{At: start, Participated: false},
		{At: start, Participated: false},
	})
	if len(got) != 1 {
		t.Fatalf("same-time sparkline length = %d, want 1", len(got))
	}
	if got[0].Value == nil || *got[0].Value != 33 {
		t.Fatalf("same-time rate = %v, want rounded 33", got[0].Value)
	}
}

func TestMakeParticipationRateSparklineReturnsEmptyForNoOpportunity(t *testing.T) {
	if got := makeParticipationRateSparkline(nil); got == nil || len(got) != 0 {
		t.Fatalf("empty sparkline = %#v, want explicit empty array", got)
	}
}

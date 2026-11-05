package qoder

import (
	"context"
	"time"
)

func fillTurnTokens(turns []TurnMetric, seg *SegmentStats) {
	for i := range turns {
		tps, tokens := seg.TokenRateWindow(turns[i].StartedAt, turns[i].EndedAt)
		turns[i].TokensPerSec = tps
		turns[i].OutputTokens = tokens
	}
}

func computeAverages(metrics *Metrics) {
	var ttftSum, postSum time.Duration
	var ttftCount, postCount int
	var tpsSum float64
	var tpsCount int
	for _, turn := range metrics.Recent {
		if turn.TTFT > 0 && !turn.ApproxTTFT {
			ttftSum += turn.TTFT
			ttftCount++
		}
		if turn.PostFirst > 0 {
			postSum += turn.PostFirst
			postCount++
		}
		if turn.TokensPerSec > 0 {
			tpsSum += turn.TokensPerSec
			tpsCount++
		}
	}
	if ttftCount > 0 {
		metrics.AvgTTFT = ttftSum / time.Duration(ttftCount)
	}
	if postCount > 0 {
		metrics.AvgPostFirst = postSum / time.Duration(postCount)
	}
	if tpsCount > 0 {
		metrics.AvgTPS = tpsSum / float64(tpsCount)
	}
}

func buildMetrics(ctx context.Context, store *Store, sessionID string, state State, control ControlState, seg *SegmentStats, limit int) Metrics {
	var turns []TurnMetric
	if store != nil {
		if recent, err := store.TurnMetrics(ctx, sessionID, limit); err == nil {
			turns = recent
		}
	}
	fillTurnTokens(turns, seg)
	metrics := Metrics{Recent: turns}
	computeAverages(&metrics)

	if !state.Active() || control.ActiveTurnID == "" {
		return metrics
	}
	start, ok := seg.LatestTurnStart()
	if !ok {
		start = control.At
	}
	if start.IsZero() {
		return metrics
	}
	now := time.Now()
	live := TurnMetric{
		TurnID:     control.ActiveTurnID,
		StartedAt:  start,
		EndedAt:    now,
		ApproxTTFT: true,
	}
	for _, turn := range turns {
		if turn.TTFT <= 0 || turn.ApproxTTFT {
			continue
		}
		if diff := turn.StartedAt.Sub(start); diff < -2*time.Second || diff > 2*time.Second {
			continue
		}
		first := turn.StartedAt.Add(turn.TTFT)
		live.TTFT = turn.TTFT
		live.PostFirst = now.Sub(first)
		live.ApproxTTFT = false
		break
	}
	if live.ApproxTTFT {
		live.PostFirst = now.Sub(start)
	}
	live.TokensPerSec, live.OutputTokens = seg.TokenRateWindow(start, now)
	metrics.Live = &live
	return metrics
}

// SPDX-License-Identifier: GPL-2.0-only

package ws

import (
	"context"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

func TestPeriodicFeedHonorsDuePeriodForceAndStopOnFakeClock(t *testing.T) {
	fake := clock.NewFake(time.Unix(1_700_000_000, 0))
	ctx, cancel := context.WithCancel(t.Context())
	sent := make(chan MessageType, 8)
	feed := newPeriodicFeed(ctx, fake, MessageSessions, func(context.Context) (any, error) {
		return []string{"session"}, nil
	}, func(messageType MessageType, _ any) bool {
		sent <- messageType
		return true
	}, cancel)

	feed.start(periodicInterval{due: 100 * time.Millisecond, period: time.Second})
	fake.Advance(99 * time.Millisecond)
	assertNoPeriodicMessage(t, feed, sent)
	fake.Advance(time.Millisecond)
	assertPeriodicMessage(t, feed, sent, MessageSessions)

	feed.changed(false)
	assertNoPeriodicMessage(t, feed, sent)
	fake.Advance(999 * time.Millisecond)
	assertNoPeriodicMessage(t, feed, sent)
	fake.Advance(time.Millisecond)
	assertPeriodicMessage(t, feed, sent, MessageSessions)

	feed.changed(true)
	assertPeriodicMessage(t, feed, sent, MessageSessions)
	feed.stop()
	fake.Advance(10 * time.Second)
	assertNoPeriodicMessage(t, feed, sent)

	cancel()
	feed.wait()
}

// settlePeriodicFeed returns after the run loop finishes its current
// iteration. A send resets the timer after the message leaves, so the test
// must not advance the fake clock before that reset.
func settlePeriodicFeed(t *testing.T, feed *periodicFeed) {
	t.Helper()
	ack := make(chan struct{})
	select {
	case feed.control <- periodicControl{ack: ack}:
	case <-feed.done:
		t.Fatal("periodic feed stopped")
	}
	select {
	case <-ack:
	case <-feed.done:
		t.Fatal("periodic feed stopped")
	}
}

func assertPeriodicMessage(t *testing.T, feed *periodicFeed, messages <-chan MessageType, want MessageType) {
	t.Helper()
	select {
	case got := <-messages:
		if got != want {
			t.Fatalf("periodic message = %q, want %q", got, want)
		}
	case <-feed.done:
		t.Fatalf("periodic message = none, want %q", want)
	}
	settlePeriodicFeed(t, feed)
}

func assertNoPeriodicMessage(t *testing.T, feed *periodicFeed, messages <-chan MessageType) {
	t.Helper()
	settlePeriodicFeed(t, feed)
	select {
	case got := <-messages:
		t.Fatalf("unexpected periodic message %q", got)
	default:
	}
}

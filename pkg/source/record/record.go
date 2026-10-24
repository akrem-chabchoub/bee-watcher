// Package record writes observations to an NDJSON file and replays them.
// A recording captures exactly what the adapters saw, so a session from the
// live cluster can be investigated later or used as a deterministic test
// fixture.
package record

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"

	"beescope/pkg/model"
)

// Tee forwards every observation from in to out and appends it to w.
func Tee(ctx context.Context, in <-chan model.Observation, out chan<- model.Observation, w io.Writer) error {
	bw := bufio.NewWriterSize(w, 256<<10)
	defer bw.Flush()
	enc := json.NewEncoder(bw)
	flush := time.NewTicker(time.Second)
	defer flush.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-flush.C:
			if err := bw.Flush(); err != nil {
				return fmt.Errorf("flush recording: %w", err)
			}
		case o, ok := <-in:
			if !ok {
				close(out)
				return nil
			}
			if err := enc.Encode(o); err != nil {
				return fmt.Errorf("write recording: %w", err)
			}
			select {
			case out <- o:
			case <-ctx.Done():
				return nil
			}
		}
	}
}

// Clock is the virtual clock of a replay: the time of the newest replayed
// observation.
type Clock struct{ ns atomic.Int64 }

// Now returns the virtual time.
func (c *Clock) Now() time.Time {
	ns := c.ns.Load()
	if ns == 0 {
		return time.Now()
	}
	return time.Unix(0, ns)
}

// Replay emits observations from path, paced by their original timing
// divided by speed (speed <= 0 replays as fast as possible). out is closed
// at the end.
func Replay(ctx context.Context, path string, speed float64, out chan<- model.Observation, clock *Clock) error {
	defer close(out)
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open recording: %w", err)
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReaderSize(f, 256<<10))
	var prev time.Time
	for {
		var o model.Observation
		if err := dec.Decode(&o); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("decode recording: %w", err)
		}
		if speed > 0 && !prev.IsZero() && o.ObservedAt.After(prev) {
			wait := time.Duration(float64(o.ObservedAt.Sub(prev)) / speed)
			if wait > 5*time.Second {
				wait = 5 * time.Second
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(wait):
			}
		}
		if o.ObservedAt.After(prev) {
			prev = o.ObservedAt
			clock.ns.Store(prev.UnixNano())
		}
		select {
		case out <- o:
		case <-ctx.Done():
			return nil
		}
	}
}

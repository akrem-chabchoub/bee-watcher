// Package anomaly detects suspicious patterns in the collected state and
// explains them with evidence. Rules are pure functions of an Input; the
// Tracker turns their findings into issues with a lifecycle.
package anomaly

import "time"

// Point is one sample of a series.
type Point struct {
	T time.Time
	V float64
}

// Series is a bounded time series.
type Series struct {
	pts []Point
	max int
}

// NewSeries returns a series that keeps at most max points.
func NewSeries(max int) *Series { return &Series{max: max} }

// Add appends a point.
func (s *Series) Add(t time.Time, v float64) {
	s.pts = append(s.pts, Point{t, v})
	if len(s.pts) > s.max {
		s.pts = append(s.pts[:0], s.pts[len(s.pts)-s.max:]...)
	}
}

// Last returns the newest point.
func (s *Series) Last() (Point, bool) {
	if s == nil || len(s.pts) == 0 {
		return Point{}, false
	}
	return s.pts[len(s.pts)-1], true
}

// Since returns points newer than t.
func (s *Series) Since(t time.Time) []Point {
	if s == nil {
		return nil
	}
	i := len(s.pts)
	for i > 0 && s.pts[i-1].T.After(t) {
		i--
	}
	return s.pts[i:]
}

// Points returns all points.
func (s *Series) Points() []Point {
	if s == nil {
		return nil
	}
	return s.pts
}

// Delta returns the increase of a counter over the window ending now, and the
// actual span covered. Counter resets restart the accumulation.
func (s *Series) Delta(now time.Time, window time.Duration) (delta float64, span time.Duration, ok bool) {
	pts := s.Since(now.Add(-window))
	if len(pts) < 2 {
		return 0, 0, false
	}
	for i := 1; i < len(pts); i++ {
		d := pts[i].V - pts[i-1].V
		if d > 0 {
			delta += d
		}
	}
	return delta, pts[len(pts)-1].T.Sub(pts[0].T), true
}

// Changes counts value changes in the window and returns the first change.
func (s *Series) Changes(now time.Time, window time.Duration) (n int, first time.Time) {
	pts := s.Since(now.Add(-window))
	for i := 1; i < len(pts); i++ {
		if pts[i].V != pts[i-1].V {
			if n == 0 {
				first = pts[i].T
			}
			n++
		}
	}
	return n, first
}

// NodeSeries holds the series the rules need for one node.
type NodeSeries map[string]*Series

// Get returns (and lazily creates) a series.
func (ns NodeSeries) Get(name string) *Series {
	s, ok := ns[name]
	if !ok {
		s = NewSeries(720)
		ns[name] = s
	}
	return s
}

// Series names recorded by the engine.
const (
	SPullsyncRate    = "pullsync_rate"
	SRadius          = "storage_radius"
	SSyncedHist      = "synced_historical"
	SSyncedLive      = "synced_live"
	SPullerWorkers   = "puller_workers"
	SToPush          = "pusher_to_push"
	SPushSynced      = "pusher_synced"
	SPushErrors      = "pusher_errors"
	SPushAttempts    = "pushsync_send_attempts"
	SPushFailed      = "pushsync_failed_attempts"
	SShallow         = "pushsync_shallow"
	SRetrReq         = "retrieval_requests"
	SRetrFail        = "retrieval_failures"
	SAPIReq          = "api_requests"
	SAPI5xx          = "api_5xx"
	SDisconnects     = "libp2p_disconnects"
	SLogErrors       = "log_errors"
	SConnected       = "connected_peers"
	SReserveInRadius = "reserve_in_radius"
)

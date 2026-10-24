package engine

import (
	"context"
	"sort"

	"beescope/pkg/model"
)

// PeerView is one /topology peer with its cluster identity resolved.
type PeerView struct {
	model.KadPeer
	Node string `json:"node,omitempty"`
}

// NodeDetail is what the inspector shows for one node beyond the snapshot.
type NodeDetail struct {
	Name    string            `json:"name"`
	Peers   []PeerView        `json:"peers"`
	RawRefs map[string]uint64 `json:"rawRefs"`
	Series  map[string][]Pt   `json:"series"`
}

// Pt is a compact series point (unix ms, value).
type Pt [2]float64

// NodeDetail returns detail for a node.
func (e *Engine) NodeDetail(ctx context.Context, name string) (*NodeDetail, bool, error) {
	var out *NodeDetail
	err := e.Do(ctx, func() {
		n, ok := e.nodes[name]
		if !ok {
			return
		}
		d := &NodeDetail{Name: name, RawRefs: map[string]uint64{}, Series: map[string][]Pt{}}
		for k, v := range n.raw {
			d.RawRefs[k] = v
		}
		if n.topo != nil {
			for _, p := range n.topo.Peers {
				d.Peers = append(d.Peers, PeerView{KadPeer: p, Node: e.byOverlay[p.Address]})
			}
			sort.Slice(d.Peers, func(i, j int) bool {
				if d.Peers[i].Bin != d.Peers[j].Bin {
					return d.Peers[i].Bin > d.Peers[j].Bin
				}
				return d.Peers[i].Address < d.Peers[j].Address
			})
		}
		for name, s := range n.series {
			pts := s.Points()
			out := make([]Pt, len(pts))
			for i, p := range pts {
				out[i] = Pt{float64(p.T.UnixMilli()), p.V}
			}
			d.Series[name] = out
		}
		out = d
	})
	return out, out != nil, err
}

// Overlays returns the overlay → node map.
func (e *Engine) Overlays(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	err := e.Do(ctx, func() {
		for k, v := range e.byOverlay {
			out[k] = v
		}
	})
	return out, err
}

// Record adds an event produced outside the engine (for example an audit
// entry for a user-confirmed action) to the timeline.
func (e *Engine) Record(ctx context.Context, ev model.Event) error {
	return e.Do(ctx, func() { e.add(ev) })
}

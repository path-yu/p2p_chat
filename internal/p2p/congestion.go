package p2p

import "time"

// congestion keeps a pipe from flooding a link it shares with everything
// else on the machine: a window that grows while packets are acknowledged
// and shrinks when one is lost, the shape TCP uses (RFC 5681), counted in
// packets of maxPayload bytes.
type congestion struct {
	cwnd     float64 // packets allowed in flight
	ssthresh float64 // above this the growth slows to one per round trip
	srtt     time.Duration
	rttvar   time.Duration
	dupAcks  int
	lastAck  uint32
}

const (
	initialCwnd = 16.0 // a slow start that still fills a short path fast
	maxCwnd     = 1024.0
	minRTO      = 200 * time.Millisecond
	maxRTO      = 3 * time.Second
	initialRTO  = 600 * time.Millisecond
)

func newCongestion() *congestion {
	return &congestion{cwnd: initialCwnd, ssthresh: 256}
}

// limit is how many packets may be in flight now.
func (cc *congestion) limit() int {
	n := int(cc.cwnd)
	if n < 1 {
		n = 1
	}
	if n > int(maxCwnd) {
		n = int(maxCwnd)
	}
	return n
}

// rto is how long a packet waits for its acknowledgement.
func (cc *congestion) rto() time.Duration {
	if cc.srtt == 0 {
		return initialRTO
	}
	rto := cc.srtt + 4*cc.rttvar
	if rto < minRTO {
		return minRTO
	}
	if rto > maxRTO {
		return maxRTO
	}
	return rto
}

// sample folds one measurement in, smoothed as Jacobson and Karels do.
func (cc *congestion) sample(rtt time.Duration) {
	if rtt <= 0 {
		return
	}
	if cc.srtt == 0 {
		cc.srtt, cc.rttvar = rtt, rtt/2
		return
	}
	diff := cc.srtt - rtt
	if diff < 0 {
		diff = -diff
	}
	cc.rttvar = (3*cc.rttvar + diff) / 4
	cc.srtt = (7*cc.srtt + rtt) / 8
}

// onAck grows the window: doubling while it is below the threshold, then a
// packet per round trip.
func (cc *congestion) onAck(n int) {
	if n <= 0 {
		return
	}
	if cc.cwnd < cc.ssthresh {
		cc.cwnd += float64(n)
	} else {
		cc.cwnd += float64(n) / cc.cwnd
	}
	if cc.cwnd > maxCwnd {
		cc.cwnd = maxCwnd
	}
}

// onLoss halves the window: a lost packet says the link is fuller than the
// window believed.
func (cc *congestion) onLoss() {
	half := cc.cwnd / 2
	if half < 4 {
		half = 4
	}
	cc.ssthresh = half
	cc.cwnd = half
}

// onTimeout starts again from a single packet, the way a retransmission
// timeout does.
func (cc *congestion) onTimeout() {
	half := cc.cwnd / 2
	if half < 2 {
		half = 2
	}
	cc.ssthresh = half
	cc.cwnd = 1
}

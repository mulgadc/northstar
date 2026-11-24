package backend

import "time"

// SetClock replaces the clock the upstream health uses.
func SetClock(u *Upstream, now func() time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.now = now
}

// Fails is the consecutive-failure score of the forwarder at addr.
func Fails(u *Upstream, addr string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, s := range u.servers {
		if s.Address == addr {
			return s.fails
		}
	}
	return -1
}

// SetHealth sets the score and cooldown end of the forwarder at addr.
func SetHealth(u *Upstream, addr string, fails int, downUntil time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, s := range u.servers {
		if s.Address == addr {
			s.fails, s.downUntil = fails, downUntil
		}
	}
}

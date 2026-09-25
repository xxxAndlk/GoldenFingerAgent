package scheduler

import (
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
)

var cst = time.FixedZone("CST", 8*3600)

func at(hour, min int) time.Time {
	return time.Date(2026, 3, 5, hour, min, 0, 0, cst)
}

func TestInDND(t *testing.T) {
	p := DefaultPolicy()
	cases := []struct {
		time time.Time
		want bool
	}{
		{at(21, 59), false},
		{at(22, 0), true},
		{at(23, 30), true},
		{at(0, 30), true},
		{at(6, 59), true},
		{at(7, 0), false},
		{at(12, 0), false},
	}
	for _, c := range cases {
		if got := p.InDND(c.time, cst); got != c.want {
			t.Errorf("InDND(%v) = %v, want %v", c.time, got, c.want)
		}
	}
}

func TestDNDDefersLowLevels(t *testing.T) {
	p := DefaultPolicy()
	user := &store.User{UserType: store.UserGeneral, TZ: "Asia/Shanghai"}

	// Level 1 at 23:00 defers to 07:00 next day.
	allow, deferTo := p.AllowFire(user, 1, at(23, 0))
	if allow {
		t.Error("level 1 inside DND must defer")
	}
	want := time.Date(2026, 3, 6, 7, 0, 0, 0, cst)
	if !deferTo.Equal(want) {
		t.Errorf("deferTo = %v, want %v", deferTo, want)
	}

	// Outside DND fires as-is.
	allow, _ = p.AllowFire(user, 1, at(10, 0))
	if !allow {
		t.Error("daytime must fire")
	}
}

func TestUrgentBreaksDNDForAdults(t *testing.T) {
	p := DefaultPolicy()
	elder := &store.User{UserType: store.UserElder, TZ: "Asia/Shanghai"}
	allow, _ := p.AllowFire(elder, 3, at(23, 0))
	if !allow {
		t.Error("urgent level-3 for elder must break DND")
	}
	general := &store.User{UserType: store.UserGeneral, TZ: "Asia/Shanghai"}
	allow, _ = p.AllowFire(general, 3, at(23, 0))
	if !allow {
		t.Error("urgent level-3 for general must break DND")
	}
}

func TestChildNightSilenceAbsolute(t *testing.T) {
	p := DefaultPolicy()
	child := &store.User{UserType: store.UserChild, TZ: "Asia/Shanghai"}
	// Even level 3 must not break the window for children.
	allow, deferTo := p.AllowFire(child, 3, at(23, 0))
	if allow {
		t.Error("child night silence is absolute — nothing breaks DND")
	}
	if deferTo.Hour() != 7 {
		t.Errorf("child deferTo = %v, want 07:00", deferTo)
	}
}

func TestEscalationChain(t *testing.T) {
	p := DefaultPolicy()
	if next, ok := p.NextLevel(1); !ok || next != 2 {
		t.Errorf("1→2 failed: %d %v", next, ok)
	}
	if next, ok := p.NextLevel(2); !ok || next != 3 {
		t.Errorf("2→3 failed: %d %v", next, ok)
	}
	if _, ok := p.NextLevel(3); ok {
		t.Error("escalation must stop at max level")
	}
}

func TestChannelForLevel(t *testing.T) {
	p := DefaultPolicy()
	if p.ChannelForLevel(1) != "app" || p.ChannelForLevel(2) != "push" || p.ChannelForLevel(3) != "sms" {
		t.Error("channel mapping wrong")
	}
}

func TestPushBudget(t *testing.T) {
	p := DefaultPolicy()
	if p.OverPushBudget(2) {
		t.Error("under budget must pass")
	}
	if !p.OverPushBudget(3) {
		t.Error("3/day must be over budget")
	}
}

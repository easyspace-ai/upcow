package velocitypairlock

import (
	"testing"
	"time"
)

func TestPickPrimaryByVelocity_Positive(t *testing.T) {
	s := &Strategy{}
	s.Config.Enabled = true
	s.Config.WindowSeconds = 10
	s.Config.MinMoveCents = 3
	s.Config.MinVelocityCentsPerSec = 0.3
	s.Config.VelocityDirectionMode = "positive"
	s.Config.PrimaryPickMode = "max_velocity"
	_ = s.Defaults()

	now := time.Now()
	// UP: 10s 内从 50 -> 60（+10c / 10s = +1.0 c/s）
	s.st.upVel.Add(now.Add(-10*time.Second), 50)
	s.st.upVel.Add(now, 60)
	// DOWN: 10s 内从 50 -> 49（-1c / 10s = -0.1 c/s，不触发 positive）
	s.st.downVel.Add(now.Add(-10*time.Second), 50)
	s.st.downVel.Add(now, 49)

	primary, ok := s.pickPrimaryByVelocityLocked()
	if !ok {
		t.Fatalf("expected trigger ok=true")
	}
	if primary != "up" {
		t.Fatalf("expected primary=up, got %s", primary)
	}
}

func TestPickPrimaryByVelocity_Positive_BothPickMax(t *testing.T) {
	s := &Strategy{}
	s.Config.Enabled = true
	s.Config.WindowSeconds = 10
	s.Config.MinMoveCents = 3
	s.Config.MinVelocityCentsPerSec = 0.3
	s.Config.VelocityDirectionMode = "positive"
	s.Config.PrimaryPickMode = "max_velocity"
	_ = s.Defaults()

	now := time.Now()
	// UP: +6c / 10s = +0.6 c/s
	s.st.upVel.Add(now.Add(-10*time.Second), 50)
	s.st.upVel.Add(now, 56)
	// DOWN: +9c / 10s = +0.9 c/s（更大，应选 DOWN）
	s.st.downVel.Add(now.Add(-10*time.Second), 50)
	s.st.downVel.Add(now, 59)

	primary, ok := s.pickPrimaryByVelocityLocked()
	if !ok {
		t.Fatalf("expected trigger ok=true")
	}
	if primary != "down" {
		t.Fatalf("expected primary=down, got %s", primary)
	}
}


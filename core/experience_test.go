package core

import "testing"

func TestExperienceScoreExplainsConstraint(t *testing.T) {
	healthy := CalculateExperience(ExperienceInput{CPU: 30, GPU: 20, Memory: 40, Disk: 5, NetworkLatency: 18, FPS: 120, OnePercentLow: 110, P95Display: 12, FrameSamples: 500, GameActive: true})
	if healthy.Overall < 90 || healthy.Grade != "Excellent" || !healthy.FrameMeasured {
		t.Fatalf("unexpected healthy score: %+v", healthy)
	}
	strained := CalculateExperience(ExperienceInput{CPU: 99, Memory: 96, Disk: 98, DiskLatency: 80, NetworkLatency: 180, Jitter: 40, PacketLoss: 5, RecentErrors: 3})
	if strained.Overall >= 55 || strained.Grade != "Strained" {
		t.Fatalf("unexpected strained score: %+v", strained)
	}
}

func TestExperienceScoreBoundsConnectivityImpact(t *testing.T) {
	local := ExperienceInput{CPU: 30, GPU: 20, Memory: 40, Disk: 5, DiskLatency: 4}
	healthy := CalculateExperience(local)
	local.NetworkLatency, local.Jitter, local.PacketLoss = 400, 120, 10
	badNetwork := CalculateExperience(local)
	if healthy.Overall-badNetwork.Overall > 8 {
		t.Fatalf("connectivity exceeded its 8-point weight: healthy=%+v bad=%+v", healthy, badNetwork)
	}
	if badNetwork.Connectivity != 0 || badNetwork.BiggestFactor == "Connectivity" {
		t.Fatalf("connectivity should remain visible without taking over the headline: %+v", badNetwork)
	}
}

package core

import "math"

type ExperienceInput struct {
	CPU, GPU, Memory, Disk                  float64
	DiskLatency, NetworkLatency, Jitter     float64
	PacketLoss                              float64
	FPS, OnePercentLow, P95Display, Dropped float64
	FrameSamples                            int
	RecentWarnings, RecentErrors            int
	GameActive                              bool
}

type ExperienceScore struct {
	Overall, Responsiveness, Smoothness, Headroom, Stability, Connectivity int
	Grade, BiggestFactor, Detail, WeightSummary                            string
	FrameMeasured                                                          bool
}

const (
	experienceWeightResponsiveness = 0.28
	experienceWeightSmoothness     = 0.26
	experienceWeightHeadroom       = 0.24
	experienceWeightStability      = 0.14
	experienceWeightConnectivity   = 0.08
)

func CalculateExperience(input ExperienceInput) ExperienceScore {
	headroomPenalty := math.Max(over(input.CPU, 65, 35)*80, over(input.Memory, 72, 28)*90)
	headroomPenalty = math.Max(headroomPenalty, over(input.Disk, 55, 45)*85)
	if !input.GameActive {
		headroomPenalty = math.Max(headroomPenalty, over(input.GPU, 75, 25)*60)
	}
	headroom := scoreFromPenalty(headroomPenalty)

	// Responsiveness describes the local machine. Connectivity is scored
	// separately so a poor Wi-Fi moment cannot masquerade as a slow PC.
	responsivenessPenalty := over(input.DiskLatency, 12, 55) * 70
	responsivenessPenalty += over(input.CPU, 90, 10)*20 + over(input.Memory, 92, 8)*10
	responsiveness := scoreFromPenalty(responsivenessPenalty)

	frameMeasured := input.FrameSamples >= 30 && input.FPS > 0
	smoothnessPenalty := over(input.CPU, 88, 12)*35 + over(input.GPU, 92, 8)*25 + over(input.DiskLatency, 20, 60)*20
	if frameMeasured {
		ratio := input.OnePercentLow / input.FPS
		smoothnessPenalty = math.Max(0, (0.82-ratio)*180)
		smoothnessPenalty += math.Min(45, input.Dropped*5)
		smoothnessPenalty += over(input.P95Display, 28, 90) * 25
	}
	smoothness := scoreFromPenalty(smoothnessPenalty)
	stability := scoreFromPenalty(float64(input.RecentWarnings*3 + input.RecentErrors*14))
	connectivityPenalty := over(input.NetworkLatency, 35, 165)*45 + over(input.Jitter, 6, 34)*30 + math.Min(50, input.PacketLoss*12)
	connectivity := scoreFromPenalty(connectivityPenalty)
	// A DEX-style weighted arithmetic composite keeps every domain visible
	// without letting a single outlier become a hidden weakest-link formula.
	// Connectivity is intentionally bounded to eight points of the total.
	overall := int(math.Round(
		float64(responsiveness)*experienceWeightResponsiveness +
			float64(smoothness)*experienceWeightSmoothness +
			float64(headroom)*experienceWeightHeadroom +
			float64(stability)*experienceWeightStability +
			float64(connectivity)*experienceWeightConnectivity,
	))
	if overall < 0 {
		overall = 0
	} else if overall > 100 {
		overall = 100
	}
	grade := "Excellent"
	if overall < 55 {
		grade = "Strained"
	} else if overall < 75 {
		grade = "Uneven"
	} else if overall < 90 {
		grade = "Good"
	}
	factor, impact := "Responsiveness", float64(100-responsiveness)*experienceWeightResponsiveness
	for name, candidate := range map[string]float64{
		"Smoothness": float64(100-smoothness) * experienceWeightSmoothness,
		"Headroom":   float64(100-headroom) * experienceWeightHeadroom,
		"Stability":  float64(100-stability) * experienceWeightStability,
	} {
		if candidate > impact {
			factor, impact = name, candidate
		}
	}
	// Connectivity remains an explicitly weighted subscore, but a transient
	// route or Wi-Fi problem must never take over the PC-health headline.
	detail := "Local experience signals are balanced."
	if impact >= 2 {
		detail = factor + " is the largest current constraint."
	}
	return ExperienceScore{
		Overall: overall, Responsiveness: responsiveness, Smoothness: smoothness,
		Headroom: headroom, Stability: stability, Connectivity: connectivity,
		Grade: grade, BiggestFactor: factor, Detail: detail,
		WeightSummary: "R28 · S26 · H24 · T14 · N8", FrameMeasured: frameMeasured,
	}
}

func over(value, threshold, span float64) float64 {
	if value <= threshold || span <= 0 {
		return 0
	}
	return math.Min(1, (value-threshold)/span)
}

func scoreFromPenalty(penalty float64) int {
	return int(math.Round(math.Max(0, math.Min(100, 100-penalty))))
}

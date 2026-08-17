package eval

import (
	"fmt"
	"sort"
	"strings"
)

// recallTolerance is how far mean Recall@K may fall below the baseline before it
// gates. Documented, intentional slack for harmless ranking jitter.
const recallTolerance = 0.05

// Baseline is the versioned gate reference. It is only valid for the policy/schema
// versions it was captured under; an intentional policy change must update both
// the version stamp and the baseline (and note it in the PR).
type Baseline struct {
	Versions             Versions           `json:"versions"`
	MinTargetResolution  float64            `json:"minTargetResolution"`
	MinMeanRecallAtK     float64            `json:"minMeanRecallAtK"`
	MaxGroundingInvalid  int                `json:"maxGroundingInvalid"`
	MaxBudgetViolations  int                `json:"maxBudgetViolations"`
	MaxForbiddenHits     int                `json:"maxForbiddenHits"`
	RequireDeterminism   bool               `json:"requireDeterminism"`
	MinSurfaceScores     map[string]float64 `json:"minSurfaceScores"`
	MaxSpeculationHits   map[string]int     `json:"maxSpeculationHits"`
	MaxExternalNodeRatio map[string]float64 `json:"maxExternalNodeRatio,omitempty"`
}

// NewBaseline derives a gate reference from a (passing) report: exact target
// resolution, the observed recall as the floor, and zero tolerance for grounding,
// budget and forbidden-path violations.
func NewBaseline(report Report) Baseline {
	baseline := Baseline{
		Versions:             report.Versions,
		MinTargetResolution:  report.Summary.TargetResolutionRate,
		MinMeanRecallAtK:     report.Summary.MeanRecallAtK,
		MaxGroundingInvalid:  0,
		MaxBudgetViolations:  0,
		MaxForbiddenHits:     0,
		RequireDeterminism:   true,
		MinSurfaceScores:     make(map[string]float64),
		MaxSpeculationHits:   make(map[string]int),
		MaxExternalNodeRatio: make(map[string]float64),
	}
	for _, surface := range report.Quality.Surfaces {
		baseline.MinSurfaceScores[surface.Surface] = surface.Score
		baseline.MaxSpeculationHits[surface.Surface] = len(surface.SpeculationHits)
		if surface.Surface == "codemap" {
			baseline.MaxExternalNodeRatio[surface.Surface] = surface.ExternalNodeRatio
		}
	}
	return baseline
}

// Gate compares a report against the baseline and returns every violation. An
// empty result means all gates pass.
func Gate(report Report, baseline Baseline) []string {
	var violations []string
	if report.Versions != baseline.Versions {
		violations = append(violations, "policy/schema versions changed since baseline — update eval/baseline.json (with -update-baseline) and note it in the PR")
	}
	if report.Summary.TargetResolutionRate < baseline.MinTargetResolution {
		violations = append(violations, fmt.Sprintf("target resolution %.3f < baseline %.3f", report.Summary.TargetResolutionRate, baseline.MinTargetResolution))
	}
	if report.Summary.TotalGroundingInvalid > baseline.MaxGroundingInvalid {
		violations = append(violations, fmt.Sprintf("grounding-invalid %d > %d", report.Summary.TotalGroundingInvalid, baseline.MaxGroundingInvalid))
	}
	if report.Summary.TotalBudgetViolations > baseline.MaxBudgetViolations {
		violations = append(violations, fmt.Sprintf("budget violations %d > %d", report.Summary.TotalBudgetViolations, baseline.MaxBudgetViolations))
	}
	if report.Summary.TotalForbiddenHits > baseline.MaxForbiddenHits {
		violations = append(violations, fmt.Sprintf("forbidden-path hits %d > %d", report.Summary.TotalForbiddenHits, baseline.MaxForbiddenHits))
	}
	if baseline.RequireDeterminism && !report.Summary.AllDeterministic {
		violations = append(violations, "non-deterministic ContextPackHash across repeated builds")
	}
	if report.Summary.MeanRecallAtK < baseline.MinMeanRecallAtK-recallTolerance {
		violations = append(violations, fmt.Sprintf("mean Recall@K %.3f below baseline %.3f (tolerance %.2f)", report.Summary.MeanRecallAtK, baseline.MinMeanRecallAtK, recallTolerance))
	}
	qualityBySurface := make(map[string]SurfaceScore, len(report.Quality.Surfaces))
	for _, surface := range report.Quality.Surfaces {
		qualityBySurface[surface.Surface] = surface
	}
	trackedSurfaces := make([]string, 0, len(baseline.MinSurfaceScores))
	for surface := range baseline.MinSurfaceScores {
		trackedSurfaces = append(trackedSurfaces, surface)
	}
	sort.Strings(trackedSurfaces)
	for _, surface := range trackedSurfaces {
		minimum := baseline.MinSurfaceScores[surface]
		actual, ok := qualityBySurface[surface]
		if !ok {
			violations = append(violations, fmt.Sprintf("quality surface %s is missing", surface))
			continue
		}
		if actual.Score < minimum {
			violations = append(violations, fmt.Sprintf("quality score %s %.2f < baseline %.2f", surface, actual.Score, minimum))
		}
		if len(actual.SpeculationHits) > baseline.MaxSpeculationHits[surface] {
			violations = append(violations, fmt.Sprintf("speculation hits %s %d > baseline %d", surface, len(actual.SpeculationHits), baseline.MaxSpeculationHits[surface]))
		}
		if maximum, tracked := baseline.MaxExternalNodeRatio[surface]; tracked && actual.ExternalNodeRatio > maximum {
			violations = append(violations, fmt.Sprintf("external-node ratio %s %.3f > baseline %.3f", surface, actual.ExternalNodeRatio, maximum))
		}
	}
	// Per-case errors (e.g. minNodes not met) are always violations.
	for _, metrics := range report.Cases {
		for _, problem := range metrics.Errors {
			violations = append(violations, fmt.Sprintf("case %s: %s", metrics.ID, problem))
		}
	}
	return violations
}

// RenderMarkdown produces a human summary of a report and any violations.
func RenderMarkdown(report Report, violations []string) string {
	var b strings.Builder
	b.WriteString("# Eval report\n\n")
	fmt.Fprintf(&b, "- cases: %d\n", report.Summary.TotalCases)
	fmt.Fprintf(&b, "- target resolution: %.1f%%\n", report.Summary.TargetResolutionRate*100)
	fmt.Fprintf(&b, "- mean Recall@K: %.3f\n", report.Summary.MeanRecallAtK)
	fmt.Fprintf(&b, "- grounding invalid: %d | budget violations: %d | forbidden hits: %d\n", report.Summary.TotalGroundingInvalid, report.Summary.TotalBudgetViolations, report.Summary.TotalForbiddenHits)
	fmt.Fprintf(&b, "- deterministic: %v\n", report.Summary.AllDeterministic)
	fmt.Fprintf(&b, "- versions: hover=%s seeMore=%s codemap=%s deepWiki=%s\n\n", report.Versions.Hover, report.Versions.SeeMore, report.Versions.Codemap, report.Versions.DeepWiki)

	b.WriteString("| case | feature | target | recall@K | evidence | dupRate | budgetViol | grounding | hashStable |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	for _, metrics := range report.Cases {
		fmt.Fprintf(&b, "| %s | %s | %v | %.2f | %d | %.2f | %d | %d | %v |\n",
			metrics.ID, metrics.Feature, metrics.TargetResolved, metrics.RecallAtK, metrics.EvidenceCount,
			metrics.DuplicateRate, metrics.BudgetViolations, metrics.GroundingInvalid, metrics.HashStable)
	}
	b.WriteString("\n")
	b.WriteString("## Output quality scorecard\n\n")
	b.WriteString("| surface | score | citations | code blocks | structure | speculation | external ratio | pages |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, surface := range report.Quality.Surfaces {
		fmt.Fprintf(&b, "| %s | %.2f | %.2f | %d | %.2f | %d | %.2f | %d |\n",
			surface.Surface, surface.Score, surface.CitationDensity, surface.CodeBlocks,
			surface.StructureCoverage, len(surface.SpeculationHits), surface.ExternalNodeRatio, surface.PageCount)
	}
	b.WriteString("\n## Golden comparisons\n\n")
	b.WriteString("| fixture | surface | historical | reference | gap | historical speculation |\n")
	b.WriteString("|---|---|---:|---:|---:|---|\n")
	for _, golden := range report.Quality.Goldens {
		fmt.Fprintf(&b, "| %s | %s | %.2f | %.2f | %.2f | %s |\n",
			golden.ID, golden.Surface, golden.Current.Score, golden.Reference.Score, golden.Delta,
			strings.Join(golden.Current.SpeculationHits, ", "))
	}
	fmt.Fprintf(&b, "\n- optional LLM judge: %s", report.Quality.Judge.Status)
	if report.Quality.Judge.Reason != "" {
		fmt.Fprintf(&b, " (%s)", report.Quality.Judge.Reason)
	}
	b.WriteString("\n\n")
	if len(violations) == 0 {
		b.WriteString("**All gates passed.**\n")
	} else {
		b.WriteString("## Gate violations\n\n")
		for _, violation := range violations {
			fmt.Fprintf(&b, "- %s\n", violation)
		}
	}
	return b.String()
}

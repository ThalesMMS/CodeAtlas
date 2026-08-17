// Command eval runs the offline evaluation harness over the versioned corpus and
// cases, writes local report artifacts (gitignored), and compares the result to a
// versioned baseline. It exits non-zero when a gate fails, so `make eval` can gate
// CI without any network or LLM.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/ai"
	"github.com/ThalesMMS/CodeAtlas/internal/eval"
)

func main() {
	repoRoot := flag.String("root", "..", "repository root (contains eval/ and examples/)")
	updateBaseline := flag.Bool("update-baseline", false, "overwrite eval/baseline.json with the current run")
	llmJudge := flag.Bool("llm-judge", false, "optionally score current outputs against references using the configured LLM endpoint")
	flag.Parse()

	if err := run(*repoRoot, *updateBaseline, *llmJudge); err != nil {
		fmt.Fprintln(os.Stderr, "eval:", err)
		os.Exit(1)
	}
}

func run(repoRoot string, updateBaseline, llmJudge bool) error {
	evalDir := filepath.Join(repoRoot, "eval")
	cases, err := loadCases(filepath.Join(evalDir, "cases.json"))
	if err != nil {
		return err
	}
	eval.SortCases(cases)

	corpusRoots := []string{
		filepath.Join(repoRoot, "examples", "tinycommerce"),
		filepath.Join(repoRoot, "examples", "swiftcommerce"),
		filepath.Join(repoRoot, "examples", "pythoncommerce"),
		filepath.Join(repoRoot, "examples", "rustcommerce"),
		filepath.Join(evalDir, "corpus", "ambiguous"),
		filepath.Join(evalDir, "corpus", "adversarial"),
	}
	var judge ai.Provider = ai.Disabled{}
	if llmJudge {
		judge = ai.NewOpenAICompatible(ai.Options{
			BaseURL: os.Getenv("CODEATLAS_LLM_BASE_URL"), APIKey: os.Getenv("CODEATLAS_LLM_API_KEY"),
			Model: os.Getenv("CODEATLAS_LLM_MODEL"), RequestTimeout: 2 * time.Minute,
		})
	}
	report, err := eval.RunWithOptions(context.Background(), corpusRoots, cases, eval.QualityOptions{
		FixturesDir: filepath.Join(evalDir, "fixtures"), Judge: judge, JudgeRequested: llmJudge,
	})
	if err != nil {
		return err
	}

	if err := writeReports(evalDir, report); err != nil {
		return err
	}
	if updateBaseline {
		if err := writeJSON(filepath.Join(evalDir, "baseline.json"), eval.NewBaseline(report)); err != nil {
			return err
		}
		fmt.Println("baseline updated:", filepath.Join(evalDir, "baseline.json"))
		return nil
	}

	baseline, err := loadBaseline(filepath.Join(evalDir, "baseline.json"))
	if err != nil {
		return err
	}
	violations := eval.Gate(report, baseline)
	fmt.Print(eval.RenderMarkdown(report, violations))
	if len(violations) > 0 {
		return fmt.Errorf("%d gate violation(s)", len(violations))
	}
	return nil
}

func loadCases(path string) ([]eval.Case, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []eval.Case
	if err := json.Unmarshal(data, &cases); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return cases, nil
}

func loadBaseline(path string) (eval.Baseline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return eval.Baseline{}, fmt.Errorf("read baseline (run with -update-baseline to create): %w", err)
	}
	var baseline eval.Baseline
	if err := json.Unmarshal(data, &baseline); err != nil {
		return eval.Baseline{}, err
	}
	return baseline, nil
}

func writeReports(evalDir string, report eval.Report) error {
	if err := writeJSON(filepath.Join(evalDir, "report.json"), report); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(evalDir, "report.md"), []byte(eval.RenderMarkdown(report, nil)), 0o644)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

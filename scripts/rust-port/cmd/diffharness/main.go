// Command diffharness compares two binaries as isolated black boxes.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	portdiff "github.com/danieljustus/symaira-desktop/scripts/rust-port/internal/diff"
)

func main() {
	left := flag.String("left", "", "fallback path to reference binary")
	right := flag.String("right", "", "fallback path to candidate binary")
	symdeskLeft := flag.String("symdesk-left", "", "path to reference symdesk binary")
	symdeskRight := flag.String("symdesk-right", "", "path to candidate symdesk binary")
	symroomLeft := flag.String("symroom-left", "", "path to reference symroom binary")
	symroomRight := flag.String("symroom-right", "", "path to candidate symroom binary")
	allowSameBinary := flag.Bool("allow-same-binary", false, "allow identical left/right binaries for the explicit Go harness self-test")
	casesPath := flag.String("cases", "testdata/port/cli/cases.json", "path to the case suite")
	stage := flag.String("stage", "", "run only cases assigned to this migration stage")
	flag.Parse()

	if *symdeskLeft == "" && *left != "" {
		*symdeskLeft = *left
	}
	if *symdeskRight == "" && *right != "" {
		*symdeskRight = *right
	}
	if *symroomLeft == "" && *left != "" {
		*symroomLeft = *left
	}
	if *symroomRight == "" && *right != "" {
		*symroomRight = *right
	}
	if !*allowSameBinary {
		for name, pair := range map[string][2]string{
			"symdesk": {*symdeskLeft, *symdeskRight},
			"symroom": {*symroomLeft, *symroomRight},
		} {
			if pair[0] == "" || pair[1] == "" {
				continue
			}
			same, err := sameBinary(pair[0], pair[1])
			if err != nil {
				fatal("inspect %s binaries: %v", name, err)
			}
			if same {
				fatal("%s reference and candidate resolve to the same file; use --allow-same-binary only for differential-go-selftest", name)
			}
		}
	}

	content, err := os.ReadFile(*casesPath)
	if err != nil {
		fatal("read cases: %v", err)
	}
	var suite portdiff.Suite
	if err := json.Unmarshal(content, &suite); err != nil {
		fatal("decode cases: %v", err)
	}
	if suite.SchemaVersion != 1 {
		fatal("unsupported case schema_version %d", suite.SchemaVersion)
	}
	if len(suite.Cases) == 0 {
		fatal("case suite is empty")
	}

	seen := make(map[string]bool, len(suite.Cases))
	passed := 0
	for _, testCase := range suite.Cases {
		if testCase.ID == "" || seen[testCase.ID] {
			fatal("case IDs must be non-empty and unique: %q", testCase.ID)
		}
		seen[testCase.ID] = true
		if *stage != "" && testCase.Stage != *stage {
			continue
		}

		targetLeft := *symdeskLeft
		targetRight := *symdeskRight
		if testCase.TargetBinary() == "symroom" {
			targetLeft = *symroomLeft
			targetRight = *symroomRight
		}

		if targetLeft == "" || targetRight == "" {
			fatal("case %q requires binary %q: left=%q right=%q", testCase.ID, testCase.TargetBinary(), targetLeft, targetRight)
		}

		leftResult, err := portdiff.Run(targetLeft, testCase)
		if err != nil {
			fatal("%s left run: %v", testCase.ID, err)
		}
		rightResult, err := portdiff.Run(targetRight, testCase)
		if err != nil {
			fatal("%s right run: %v", testCase.ID, err)
		}
		if err := portdiff.Compare(testCase, leftResult, rightResult); err != nil {
			fatal("%s: %v", testCase.ID, err)
		}
		fmt.Printf("PASS %s\n", testCase.ID)
		passed++
	}
	if passed == 0 {
		fatal("no cases selected for stage %q", *stage)
	}
	fmt.Printf("PASS all %d selected differential cases\n", passed)
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}

func sameBinary(left, right string) (bool, error) {
	leftInfo, err := os.Stat(left)
	if err != nil {
		return false, err
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		return false, err
	}
	return os.SameFile(leftInfo, rightInfo), nil
}

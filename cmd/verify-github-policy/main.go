// Command verify-github-policy evaluates the effective GitHub rulesets that
// authorize software-factory autonomous merges.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"

	"github.com/0x63616c/software-factory/internal/githubpolicy"
	"github.com/0x63616c/software-factory/internal/work"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("GitHub policy is not ready", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	flags := flag.NewFlagSet("verify-github-policy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	appID := flags.String("app-id", "", "GitHub App numeric id")
	branch := flags.String("branch", "main", "deployment branch whose effective rules are verified")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing GitHub policy flags: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected GitHub policy arguments: %v", flags.Args())
	}
	parsedAppID, err := strconv.ParseInt(*appID, 10, 64)
	if err != nil || parsedAppID <= 0 {
		return errors.New("--app-id must be a positive integer")
	}
	var rulesets []githubpolicy.Ruleset
	if err := json.NewDecoder(stdin).Decode(&rulesets); err != nil {
		return fmt.Errorf("decoding detailed GitHub rulesets: %w", err)
	}
	report := githubpolicy.Verify(rulesets, parsedAppID, *branch, work.DefaultTargetRunPolicy().RequiredChecks)
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		return fmt.Errorf("writing GitHub policy report: %w", err)
	}
	if !report.Ready {
		return errors.New("GitHub policy is not ready for autonomous merge")
	}
	return nil
}

// Command canary-responses performs one explicit call through the real direct
// Responses client. It is only invoked by the protected manual canary workflow.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/0x63616c/software-factory/internal/clients/codexresponses"
	"github.com/0x63616c/software-factory/internal/config"
	"github.com/0x63616c/software-factory/internal/work"
)

const expectedAnswer = "SOFTWARE_FACTORY_CANARY_OK"

type staticCredential struct {
	credential codexresponses.Credential
}

func (s staticCredential) Credential(context.Context) (codexresponses.Credential, error) {
	return s.credential, nil
}

func run(ctx context.Context) error {
	cfg, err := config.LoadCanaryResponses()
	if err != nil {
		return err
	}
	client, err := codexresponses.New(
		&http.Client{Timeout: 45 * time.Second},
		cfg.Endpoint,
		staticCredential{credential: codexresponses.Credential{
			AccessToken: work.NewCredential(cfg.AccessToken),
			AccountID:   cfg.AccountID,
		}},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		return err
	}
	result, err := client.Turn(ctx, codexresponses.TurnRequest{
		Model:         cfg.Model,
		Instructions:  "Return only the exact text requested by the user.",
		Input:         []codexresponses.InputItem{codexresponses.UserText("Reply with exactly " + expectedAnswer)},
		ToolChoice:    codexresponses.ToolChoiceNone,
		TextVerbosity: codexresponses.TextVerbosityLow,
	}, nil)
	if err != nil {
		return err
	}
	if result.Outcome != codexresponses.OutcomeFinalText || strings.TrimSpace(result.Text) != expectedAnswer {
		return fmt.Errorf("unexpected Responses canary result: outcome=%s text=%q", result.Outcome, result.Text)
	}
	fmt.Println(expectedAnswer)
	return nil
}

func realMain() int {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Responses canary failed:", err)
		return 1
	}
	return 0
}

func main() { os.Exit(realMain()) }

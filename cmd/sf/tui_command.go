package main

import (
	"fmt"

	"github.com/0x63616c/software-factory/internal/sf/tui"
	"github.com/spf13/cobra"
)

func addTUICommand(root *cobra.Command) {
	tuiCmd := &cobra.Command{
		Use:   "tui",
		Args:  cobra.NoArgs,
		Short: "Open the sf full-screen interface",
		RunE: func(cmd *cobra.Command, _ []string) error {
			runtime, err := buildRuntime(cmd)
			if err != nil {
				return err
			}
			if runtime.Config.Name == "" {
				return fmt.Errorf("no active sf context")
			}
			return tui.Run(runtime.Actions, runtime.Config.PollInterval, runtime.Config.Timeout, runtime.Out)
		},
	}
	root.AddCommand(tuiCmd)
}

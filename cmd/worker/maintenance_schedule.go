package main

import (
	"context"
	"fmt"

	temporalapi "github.com/0x63616c/software-factory/internal/clients/temporal"
)

func ensureMaintainFactorySchedule(ctx context.Context, temporal temporalapi.Client) error {
	if err := temporalapi.EnsureMaintainFactorySchedule(ctx, temporal.ScheduleClient()); err != nil {
		return fmt.Errorf("reconciling the MaintainFactory schedule: %w", err)
	}
	return nil
}

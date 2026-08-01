package temporal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/0x63616c/software-factory/internal/work"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	temporalerrors "go.temporal.io/sdk/temporal"
)

const maintainFactoryInterval = 5 * time.Minute

// EnsureMaintainFactorySchedule creates or replaces the stable maintenance
// Schedule. Reconciliation on every boot makes code the source of truth while
// preserving the Schedule's live paused state across definition updates.
func EnsureMaintainFactorySchedule(ctx context.Context, schedules client.ScheduleClient) error {
	if schedules == nil {
		return fmt.Errorf("maintain factory schedule client is required")
	}
	desired := maintainFactoryScheduleOptions()
	if _, err := schedules.Create(ctx, desired); err == nil {
		return nil
	} else {
		var exists *serviceerror.AlreadyExists
		if !errors.Is(err, temporalerrors.ErrScheduleAlreadyRunning) && !errors.As(err, &exists) {
			return fmt.Errorf("creating MaintainFactory schedule %s: %w", desired.ID, err)
		}
	}
	handle := schedules.GetHandle(ctx, desired.ID)
	if err := handle.Update(ctx, client.ScheduleUpdateOptions{DoUpdate: func(input client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
		return &client.ScheduleUpdate{Schedule: &client.Schedule{
			Action: desired.Action,
			Spec:   &desired.Spec,
			Policy: &client.SchedulePolicies{
				Overlap: desired.Overlap,
			},
			State: input.Description.Schedule.State,
		}}, nil
	}}); err != nil {
		return fmt.Errorf("updating MaintainFactory schedule %s: %w", desired.ID, err)
	}
	return nil
}

func maintainFactoryScheduleOptions() client.ScheduleOptions {
	return client.ScheduleOptions{
		ID: work.MaintainFactoryScheduleID,
		Spec: client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{
			Every: maintainFactoryInterval,
		}}},
		Action: &client.ScheduleWorkflowAction{
			ID:        work.MaintainFactoryWorkflowID,
			Workflow:  "MaintainFactory",
			TaskQueue: work.TaskQueue,
		},
		Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
	}
}

package storefake

import "github.com/0x63616c/software-factory/internal/store"

// Store satisfies every narrow interface internal/store declares, so a
// consumer can swap the real store for this fake without a second interface
// to keep in sync.
var (
	_ store.TicketCreator               = (*Store)(nil)
	_ store.TicketReader                = (*Store)(nil)
	_ store.TicketStateWriter           = (*Store)(nil)
	_ store.ReadyTicketLister           = (*Store)(nil)
	_ store.TicketDependencyWriter      = (*Store)(nil)
	_ store.TicketDependencyReader      = (*Store)(nil)
	_ store.RunStarter                  = (*Store)(nil)
	_ store.RunEnder                    = (*Store)(nil)
	_ store.RunReader                   = (*Store)(nil)
	_ store.StepRecorder                = (*Store)(nil)
	_ store.AttemptRecorder             = (*Store)(nil)
	_ store.AttemptEnder                = (*Store)(nil)
	_ store.AttemptReader               = (*Store)(nil)
	_ store.TranscriptWriter            = (*Store)(nil)
	_ store.TranscriptReader            = (*Store)(nil)
	_ store.DispatcherStateReader       = (*Store)(nil)
	_ store.DispatcherStateWriter       = (*Store)(nil)
	_ store.WebhookDeliveryRecorder     = (*Store)(nil)
	_ store.WebhookDeliveryAcknowledger = (*Store)(nil)
)

package work

const (
	// MaxTargetTranscriptUncompressedBytes bounds legacy Run Worker checkpoint
	// evidence. Target-run recovery still reads those rows while the agent
	// transcript reference path is adopted, so its database contract remains
	// deliberately independent of AgentWorkflow's blob-backed transcript.
	MaxTargetTranscriptUncompressedBytes = 320 << 10
	// MaxTargetTranscriptCompressedBytes is the matching durable-row bound.
	MaxTargetTranscriptCompressedBytes = 384 << 10
)

// Transcript is one stage attempt's whole raw event stream, as JSONL.
//
// It is retained in the legacy stage activity result wire type so Temporal can
// replay executions started before AgentWorkflow. New executions persist a
// blob-backed agent.TranscriptRef instead of carrying these bytes in history.
//
// A named byte slice rather than a plain []byte keeps that historical wire
// contract explicit.
type Transcript []byte

// Bytes returns the transcript's raw content.
func (t Transcript) Bytes() []byte { return []byte(t) }

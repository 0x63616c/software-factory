package work

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// The control plane's payloads are hand-written JSON, so an unrecognised key is
// an error rather than something to skip.
//
// encoding/json's default is the opposite: it drops a key it has no field for,
// which turns every misspelling in an UpdateConfig signal into a signal that
// succeeds and changes nothing. A Temporal signal cannot fail back to its
// sender, so "accepted and ignored" and "accepted and applied" look identical
// from the outside — the operator learns nothing until they notice the system
// still doing what they just told it to stop doing.
//
// The check lives on the types rather than at a call site because the call site
// is Temporal's data converter, which owns the unmarshal and offers no place to
// set json.Decoder.DisallowUnknownFields. A type that refuses unknown keys is
// correct no matter who decodes it; the resulting error reaches the operator as
// Status.ConfigError, which is the only channel a signal has.
func strictUnmarshal(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// UnmarshalJSON decodes an UpdateConfig payload, rejecting any key this type
// does not have a field for.
func (u *ConfigUpdate) UnmarshalJSON(data []byte) error {
	// The local type sheds ConfigUpdate's method set, so the decoder below uses
	// the struct tags rather than calling this method again.
	type plain ConfigUpdate
	var decoded plain
	if err := strictUnmarshal(data, &decoded); err != nil {
		return fmt.Errorf("config update: %w", err)
	}
	*u = ConfigUpdate(decoded)
	return nil
}

// UnmarshalJSON decodes a per-stage override set, rejecting any key that is not
// a stage. This is the case the struct-of-stages layout exists to make
// impossible in Go, restored at the JSON boundary where it is actually written.
func (m *StageModels) UnmarshalJSON(data []byte) error {
	type plain StageModels
	var decoded plain
	if err := strictUnmarshal(data, &decoded); err != nil {
		return fmt.Errorf("stage model overrides: %w", err)
	}
	*m = StageModels(decoded)
	return nil
}

// UnmarshalJSON decodes a model, rejecting any key this type does not have a
// field for — a misspelled "name" would otherwise leave the model half
// specified and the mistake invisible until Validate blamed the whole update.
func (m *Model) UnmarshalJSON(data []byte) error {
	type plain Model
	var decoded plain
	if err := strictUnmarshal(data, &decoded); err != nil {
		return fmt.Errorf("model: %w", err)
	}
	*m = Model(decoded)
	return nil
}

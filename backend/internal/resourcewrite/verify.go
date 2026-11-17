package resourcewrite

import (
	"time"

	"agent-platform/backend/internal/planning"
)

func verification(i Intent, r Receipt) (Verification, error) {
	if !r.ValidFor(i) || r.Status != "APPLIED" {
		return Verification{}, ErrInvalid
	}
	return Verification{OperationID: i.OperationID, IntentHash: i.Hash, ReceiptID: r.ID, State: "DRIFTED", Code: "resource_changed", ObservedAt: time.Now().UTC()}, nil
}
func VerifyConfig(i Intent, r Receipt, state planning.ConfigState) (Verification, error) {
	v, err := verification(i, r)
	if err != nil || i.Kind != "config" {
		return Verification{}, ErrInvalid
	}
	if !planning.ValidConfigState(state, i.Target) {
		return Verification{}, ErrUnknown
	}
	v.ResourceVersion = state.Version
	for _, item := range state.Items {
		if item.Key != i.Key {
			continue
		}
		v.ItemVersion = item.Version
		if state.Version == r.ResourceVersion && state.SaveMode == i.Effect && item.Version == r.ItemVersion && planning.Hash(item.Value) == r.ValueHash {
			v.State = "VERIFIED"
			v.Code = ""
		}
		break
	}
	return v, nil
}
func VerifyExperiment(i Intent, r Receipt, state planning.ExperimentState) (Verification, error) {
	v, err := verification(i, r)
	if err != nil || i.Kind != "experiment" {
		return Verification{}, ErrInvalid
	}
	if !planning.ValidExperimentState(state, i.Target) {
		return Verification{}, ErrUnknown
	}
	v.ResourceVersion = state.Version
	for _, item := range state.Items {
		if item.Key != i.Key {
			continue
		}
		v.ItemVersion = item.Version
		if state.Version == r.ResourceVersion && state.DraftNoTraffic && item.State == "DRAFT" && item.Version == r.ItemVersion && planning.Hash(item.Spec) == r.ValueHash {
			v.State = "VERIFIED"
			v.Code = ""
		}
		break
	}
	return v, nil
}

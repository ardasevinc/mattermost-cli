// Package stagestore persists already validated, normalized mutation plans.
//
// It is intentionally not the active-credential validation boundary. CreateInput
// and ReviseInput are persistence records for the future staging service, which
// must construct them only after credential scanning and target resolution. CLI
// and transport packages must not call Store mutation methods directly.
package stagestore

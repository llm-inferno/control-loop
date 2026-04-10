# Load Emulator Multi-Phase Feature Design

**Date:** 2026-04-10
**Status:** Approved

## Context

The loademulator currently generates a random walk (Ornstein-Uhlenbeck process) around a static nominal RPM read from Kubernetes deployment labels. This is sufficient for steady-state testing but cannot simulate realistic load scenarios such as traffic ramps, diurnal patterns, or load spike experiments.

This feature introduces the concept of a *phase sequence* — a configurable series of time-bounded segments during which the nominal RPM itself changes linearly. The OU random walk continues as before, but tracks a moving target instead of a static one. The goal is to enable repeatable, scenario-driven load experiments without modifying Kubernetes labels by hand.

## Design

### Config File Format

Phases are specified in a YAML file:

```yaml
phases:
  - duration: 5m
    ratio: 1.0    # hold flat for 5 minutes
  - duration: 10m
    ratio: 3.0    # ramp up to 3x over 10 minutes
  - duration: 5m
    ratio: 1.0    # hold at 3x (ratio relative to start of this phase, i.e. 3x)
  - duration: 10m
    ratio: 0.333  # ramp back down to ~1x
  - duration: 0s  # hold at final value forever (terminal phase)
```

**Ratio semantics — chained:** Each ratio is relative to the nominal at the *start of that phase* (the end-value of the previous phase). A ratio of `3.0` triples the current nominal; `0.333` reduces it to one-third. The cumulative multiplier is the product of all completed phase ratios.

**Duration format:** Standard Go `time.ParseDuration` syntax (`5m`, `1h30m`, `90s`, `0s`, etc.).

**Terminal phase (`duration: 0s`):** Signals "hold at current value forever." The `ratio` field is ignored and may be omitted. Must be the last entry if present.

**End-of-sequence behavior:** If all phases have finite durations and no terminal phase is specified, the emulator holds at the final cumulative multiplier indefinitely after the sequence completes.

**Backward compatibility:** The config file is optional. When `INFERNO_LOAD_PHASES` is unset or empty, the loademulator behaves exactly as before (static nominal, no phases).

### Validation at Startup

On startup (or when the env var is set), the config is parsed and validated:
- All ratios must be > 0
- A `duration: 0s` phase must be the last entry
- At least one phase must be present if the file exists

Fatal error on validation failure (prevents a misconfigured emulator from silently doing nothing useful).

### Phase Tracker

A new `PhaseTracker` struct in `pkg/loademulator/phases.go` encapsulates all phase logic.

```go
type Phase struct {
    Duration time.Duration
    Ratio    float64
}

type PhaseTracker struct {
    phases    []Phase
    startTime time.Time
    started   bool
}
```

**`GetMultiplier() (float64, int)`** — returns the current cumulative multiplier and the 1-based current phase index. Called once per loademulator cycle (each iteration of `Run()`).

Algorithm:
1. On first call, record `startTime = time.Now()`.
2. `elapsed = time.Since(startTime)`.
3. Walk phases, accumulating durations and multipliers:
   - Terminal phase (`Duration == 0`): return `cumulativeMultiplier`.
   - If `elapsed` falls within the current phase: linearly interpolate between `cumulativeMultiplier` and `cumulativeMultiplier * phase.Ratio` using `fraction = (elapsed - phaseStart) / phase.Duration`.
   - Otherwise: apply the full ratio (`cumulativeMultiplier *= phase.Ratio`) and advance.
4. If past all phases: return the final `cumulativeMultiplier`.

### Integration with Run()

In `pkg/loademulator/loademulator.go`:

- The `LoadEmulator` struct gains a `*PhaseTracker` field (nil when phases are disabled) and a `map[string]float64` for original nominal RPM per deployment (`originalNominalRPM`).
- On each cycle, for each deployment:
  1. Read the `nominal.rpm` label. If this deployment is not yet in `originalNominalRPM`, store it (baseline capture on first encounter).
  2. If tracker is active: `multiplier, phaseIdx = tracker.GetMultiplier()`. Else: `multiplier = 1.0`.
  3. `adjustedNominal = originalNominalRPM[name] * multiplier`.
  4. Update the deployment's `nominal.rpm` label to `adjustedNominal`.
  5. Pass `adjustedNominal` to `perturbLoad()` as the nominal target for the OU walk.
  6. `inTokens` and `outTokens` nominal values are unchanged (phases apply to RPM only).

**New deployment handling:** If a managed deployment appears mid-run, its nominal is captured on first encounter and the current multiplier is applied immediately. It joins the phase sequence already in progress.

### Nominal Label Updates

The deployment's `inferno.server.load.nominal.rpm` label is updated each cycle to the phase-adjusted value. This keeps the nominal label truthful (the collector and any dashboard tools see the actual current target, not the original baseline).

### Logging

- **Startup:** Log the parsed phase sequence for operator verification.
- **Phase transitions:** When `GetMultiplier()` crosses into a new phase, log: `phase N → N+1: multiplier X.XX → Y.YY, duration D`.
- **Per-cycle:** When phases are active, log the current phase index and multiplier alongside the existing per-deployment load line.

### Configuration and Deployment

**New env var:** `INFERNO_LOAD_PHASES` — path to the YAML phase config file. Default: empty (feature disabled). Constant added to `pkg/controller/defaults.go`.

**ConfigMap:** `load-phases-config` in the `inferno` namespace, mounted at `/etc/loadphases/phases.yaml` in the load-emulator pod. The mount and env var are added to `yamls/deploy/load-emulator.yaml`. The ConfigMap is optional — when absent, `INFERNO_LOAD_PHASES` is left empty.

**`kind-deploy.sh`:** Add an optional step to create the ConfigMap from `sample-data/load-phases.yaml` if that file exists.

**Sample config:** `sample-data/load-phases.yaml` with a representative scenario (hold, ramp up, hold, ramp down, terminal hold).

## Files to Create or Modify

| File | Change |
|---|---|
| `pkg/loademulator/phases.go` | **New** — `Phase`, `PhaseConfig`, `PhaseTracker`, config parsing, `GetMultiplier()` |
| `pkg/loademulator/loademulator.go` | Modify — integrate tracker, capture original nominals, update nominal labels |
| `cmd/loademulator/main.go` | Modify — read `INFERNO_LOAD_PHASES`, construct and pass tracker |
| `pkg/controller/defaults.go` | Add `LoadPhasesEnvName` constant |
| `yamls/deploy/load-emulator.yaml` | Add ConfigMap volume + mount + env var |
| `scripts/kind-deploy.sh` | Add optional ConfigMap creation step |
| `sample-data/load-phases.yaml` | **New** — sample phase config |

## Out of Scope

- Phases for `inTokens` and `outTokens` (RPM only in this design)
- Per-deployment independent phase schedules (uniform across all managed deployments)
- Hot-reloading the phase config mid-run (startup-only parsing)
- Looping phase sequences

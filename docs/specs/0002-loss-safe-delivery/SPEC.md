---
kit_metadata_version: 1
artifact: "spec"
workflow_version: 3
phase: "validate"
feature:
  id: "0002"
  slug: "loss-safe-delivery"
  dir: "0002-loss-safe-delivery"
---
# SPEC

## PURPOSE

Support opt-in best-effort embedded state and private Slack audience enforcement without adding deployment-specific configuration to the generic project.

## CONTEXT

The service currently reconciles all committed source evidence from a configured occurrence cutoff. A replacement empty ledger would re-materialize old deliveries. Private destinations need runtime verification beyond a pinned channel identifier.

## REQUIREMENTS

An opt-in from_startup startup policy persists an effective cutoff=max(configured activation,prior floor,service startup time). Each service run advances the scan floor while retained state preserves pending retries; a lost or restored ledger skips all source occurrences earlier than its service startup floor. Fail closed on corrupt existing state. Optional configured workspace and allowed user IDs enforce a private, unshared, nongeneral, non-DM channel containing only the authenticated bot and allowed humans, checked before each send. Suppress broadcast syntax. No private names, IDs, queries, credentials or deployment configuration in public artifacts.

## ACCEPTED PLAN

Implement state initialization/migration and strict destination verification in separate independent file owners; wire validated configuration, effective-cutoff scanning, inspection and metrics; add deterministic loss/restart and HTTP audience tests; run full synthetic integration and current-head CI, resolve review, and merge through issue #5 lane.

## DECISIONS

Default legacy policy retains full reconciliation behavior. Explicit from_startup sacrifices notifications during ledger loss/downtime and pre-cutoff late commits and pre-startup unseen events on retained state to avoid replay of unknown historical notifications. EBS lifecycle belongs to external deployment. Do not automatically restore old snapshots. Workspace/audience guard is mandatory when either configuration field is provided; checks are repeated per attempt with no cached approval. Public service remains configurable without customer-specific code.

## DISCOVERIES

External ECS service volumes can be replaced during task replacement. A pinned destination alone cannot verify channel privacy or members. Live infrastructure and Slack configuration are outside this generic code PR.

## VALIDATION

PASS: make check and make integration with a task-owned loopback PostgreSQL 14 database, including race tests, loss/restart/snapshot-cutoff preservation and paginated synthetic audience checks. Hosted CI pending.

## OUTCOME

Implemented startup recovery floor, private destination verification/dispatch pause, status/metrics, container liveness command and sanitized minute heartbeat. Delivery through issue #5 under explicit merge authority for generic Outpost improvements. No infrastructure or live Slack mutation is part of this PR.

## REPOSITORY MEMORY

This spec preserves behavior tradeoffs; README and operations reference will document the new configuration and limits.

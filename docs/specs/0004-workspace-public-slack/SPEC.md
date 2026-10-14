---
kit_metadata_version: 1
artifact: "spec"
workflow_version: 3
phase: "validate"
feature:
  id: "0004"
  slug: "workspace-public-slack"
  dir: "0004-workspace-public-slack"
---
# SPEC

## PURPOSE

Add an explicit opt-in for alerts in one public Slack channel within a pinned workspace. Preserve private audience controls and generic public code.

## CONTEXT

The current adapter verifies a private channel and exact user membership before every send. Configuration can omit the guard for legacy behavior. Public channels need a separate policy because treating an empty allowlist as unrestricted could silently broaden delivery.

## REQUIREMENTS

Support audience_policy values private_allowlist and workspace_public. An omitted policy retains existing behavior. Explicit policies require pinned workspace and channel IDs. Public policy rejects nonempty allowed_user_ids, private/general/archived/DM/shared channels, pending external connections, and a bot that has not joined. Missing safety evidence prevents sends. Private membership enforcement stays unchanged. Broadcast markup remains escaped. No private deployment data or live Slack writes.

## ACCEPTED PLAN

Add validated policy configuration and adapter enforcement. Reuse per-attempt authentication and channel checks; public channels require joined-bot and internal-only sharing evidence instead of human membership enumeration. Add native synthetic configuration and gateway tests. Document scope differences and audience exposure. The primary agent wires runtime configuration and owns complete validation and PR delivery.

## DECISIONS

Public channel visibility is a deliberate broader audience: any workspace member can discover or join the channel. Require explicit workspace_public and reject an allowlist rather than ignoring it. Require channels:read and chat:write with an invited bot; do not require chat:write.public. Retain private_allowlist as the default interpretation and preserve unguarded legacy configuration. No channel creation, conversion, configuration activation, or test messages belong to this generic change.

## DISCOVERIES

Slack channel metadata exposes bot membership and pending sharing state. Per-attempt verification is not atomic with message posting, so operators must control channel sharing and membership.

## VALIDATION

PASS: go test -race ./internal/slack ./internal/config. Tests cover explicit/implicit private policy compatibility, public opt-in, conflicting allowlists, missing pins, joined bot, sharing and pending-sharing metadata, channel changes before later attempts, and zero posts on rejection. All gateway tests use synthetic HTTP fixtures. Full project validation and hosted CI are owned by the primary agent and remain pending.

## OUTCOME

Implemented policy validation, per-attempt public channel verification, native tests, and operator guidance. Runtime wiring and complete PR delivery are owned by the primary agent. No private destination configuration changes or live Slack writes.

## REPOSITORY MEMORY

Update this spec, concise README configuration guidance, and operations policy/scope guidance. Keep historical specs unchanged.

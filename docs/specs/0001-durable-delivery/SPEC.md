---
kit_metadata_version: 1
artifact: "spec"
workflow_version: 3
phase: "validate"
feature:
  id: "0001"
  slug: "durable-delivery"
  dir: "0001-durable-delivery"
---
# SPEC

## PURPOSE

Outpost — Durable alerts from committed events. Build a generic standalone Go service with durable local state and narrow PostgreSQL and Slack adapters.

## CONTEXT

The initial repository contains only README and gitignore plus the merged Kit bootstrap. Design context selected bbolt, future-only occurrence cutoff, coverage-first retries and full retained-range reconciliation. Private deployment adapters and data must stay outside this public repository.

## REQUIREMENTS

Atomic enqueue/checkpoint; immutable activation and destination policy; one logical delivery per source/environment/tenant/order/placement/destination; short transactions; crash recovery and ambiguous attempts; read-only bounded PostgreSQL queries; explicit production classification; allowlisted payload; dispatch disabled by default; inspection, health and metrics. No production access, credentials, infrastructure, private schema, live messages or implementation merge.

## ACCEPTED PLAN

Implement normalized contracts, transactional bbolt state and due index, full reconciliation sweeps and dispatcher, generic PostgreSQL projection, safe Slack HTTP gateway, CLI configuration and inspection, synthetic native/integration tests, container and CI, then draft PR.

## DECISIONS

Single process and volume, enforced by bbolt file locking. Reconcile the complete retained activation range in bounded pages with a fixed upper occurrence boundary per sweep; restart from activation after every sweep to catch late commits. Retain identities indefinitely. Config policy bound to the state file. Unknown classification is counted, excluded fail-closed. No exactly-once external guarantee. License and hosting/SOP remain undecided.

## DISCOVERIES

Source retention, query cost and deployed classification completeness remain unverified. Projection owners must corroborate classification and acceptance provenance. Review found and fixed pre-activation scan cutoff deadlock, active-attempt conflict evidence loss, and binding immutable state before SQL preflight. No private implementation or terminology belongs in public artifacts.

## VALIDATION

PASS: make build, make check, make integration with an isolated loopback PostgreSQL 14 cluster, including full PostgreSQL-to-bbolt-to-HTTP test. PASS: disabled-dispatch executable smoke (readiness, metrics, graceful shutdown, inspection with zero sends, backup), local nonroot container build, and private-term scan over repository artifacts. Production delivery UNOBSERVED. Hosted CI pending draft PR.

## OUTCOME

Implemented generic adapters, durable state, reconciliation, retries, inspection, backup, metrics, executable, synthetic fixtures and PR CI. Delivery is a draft PR; no implementation merge, production access, deployment or live messages. License undecided. No deployment requirements supplied; production activation remains outside this implementation.

## REPOSITORY MEMORY

This SPEC captures rationale. README and testing reference record commands, adapter contracts, operational limits and validated behavior. Constitution remains the valid starter because this first implementation has not established recurring project-wide conventions.

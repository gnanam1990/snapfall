# daemon (owner: B)

Local runtime: supervisor · event gateway · orchestrator · agent workers · action broker ·
memory service · egress proxy · policy engine · treasury signer boundary · chain indexer. (PRD §6.3)

Language: **decide at Day-0 call (Go per PRD §6.2, or Node if team velocity wins) — then LOCKED.**

Day-1 targets (PRD §14.3 B):
- [ ] module scaffold + `store/schema.sql` applied
- [ ] supervisor boots one dummy worker
- [ ] manifest loader validates `manifests/*.yaml` (FR-ORG-006)
- [ ] typed bus + outbox table wired

Trust boundary law: **agents propose → typed actions validated → deterministic policy authorizes →
isolated treasury signs → contracts enforce.** LLM output never executes directly (FR-ACT-001).

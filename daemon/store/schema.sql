-- Snapfall local state (SQLite WAL) — PRD §8.1 core entities + §8.5 event taxonomy
PRAGMA journal_mode=WAL;

CREATE TABLE IF NOT EXISTS organizations (
  id TEXT PRIMARY KEY, owner TEXT NOT NULL, name TEXT NOT NULL,
  policy_version INTEGER NOT NULL DEFAULT 1, treasury_ref TEXT, status TEXT NOT NULL DEFAULT 'active',
  advance_rate_bps INTEGER, created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
  id TEXT PRIMARY KEY, org_id TEXT NOT NULL REFERENCES organizations(id),
  role TEXT NOT NULL, manifest_path TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'idle',
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY, org_id TEXT NOT NULL REFERENCES organizations(id),
  customer_ref TEXT, status TEXT NOT NULL,               -- Draft..Failed (FR-JOB-003)
  quote_usdc TEXT, operating_budget_usdc TEXT,
  advance_principal_usdc TEXT, advance_fee_usdc TEXT, advance_status TEXT,  -- Requested/Issued/Repaid/WrittenOff (§6.6)
  committed_spend_usdc TEXT NOT NULL DEFAULT '0', settled_spend_usdc TEXT NOT NULL DEFAULT '0',
  terms_hash TEXT, delivery_hash TEXT, vault_job_id TEXT, deadline INTEGER, created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY, job_id TEXT NOT NULL REFERENCES jobs(id),
  owner_agent TEXT, status TEXT NOT NULL, depends_on TEXT,  -- json array
  budget_usdc TEXT, inputs_json TEXT, outputs_json TEXT, created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS payment_intents (
  id TEXT PRIMARY KEY, job_id TEXT NOT NULL, task_id TEXT, agent_id TEXT NOT NULL,
  merchant TEXT NOT NULL, resource TEXT, amount_usdc TEXT NOT NULL,
  purpose TEXT, request_hash TEXT, nonce TEXT UNIQUE NOT NULL, expiry INTEGER NOT NULL,
  policy_version INTEGER NOT NULL, decision TEXT,           -- AutoApprove/HumanApprovalRequired/Deny (FR-PAY-004)
  decision_reasons_json TEXT, status TEXT NOT NULL,         -- Proposed..Reconciled (§6.6)
  receipt_json TEXT, created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS events (                          -- tamper-evident log (FR-AUD-001)
  seq INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL,
  kind TEXT NOT NULL, entity_id TEXT, actor TEXT, payload_json TEXT, payload_hash TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS outbox (                          -- transactional outbox for the typed bus
  id INTEGER PRIMARY KEY AUTOINCREMENT, topic TEXT NOT NULL, payload_json TEXT NOT NULL,
  published INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL
);

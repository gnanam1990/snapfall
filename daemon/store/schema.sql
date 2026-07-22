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

-- H1 chain handoff (A2/A3). Raw receipts, normalized events, projections and the cursor
-- commit together. Chain ordering is always (block_number, log_index), never timestamp.
CREATE TABLE IF NOT EXISTS chain_logs (
  chain_id INTEGER NOT NULL, transaction_hash TEXT NOT NULL, log_index INTEGER NOT NULL,
  block_number INTEGER NOT NULL, block_hash TEXT NOT NULL, contract_address TEXT NOT NULL,
  topic0 TEXT NOT NULL, topics_json TEXT NOT NULL, data TEXT NOT NULL,
  removed INTEGER NOT NULL DEFAULT 0, decoded INTEGER NOT NULL DEFAULT 0,
  observed_at INTEGER NOT NULL,
  PRIMARY KEY (chain_id, transaction_hash, log_index)
);

CREATE INDEX IF NOT EXISTS chain_logs_order_idx
  ON chain_logs(chain_id, block_number, log_index);

CREATE TABLE IF NOT EXISTS chain_events (
  chain_id INTEGER NOT NULL, transaction_hash TEXT NOT NULL, log_index INTEGER NOT NULL,
  block_number INTEGER NOT NULL, contract_address TEXT NOT NULL,
  kind TEXT NOT NULL, entity_id TEXT NOT NULL, actor TEXT,
  payload_json TEXT NOT NULL, h1_version TEXT NOT NULL DEFAULT '1.0',
  PRIMARY KEY (chain_id, transaction_hash, log_index),
  FOREIGN KEY (chain_id, transaction_hash, log_index)
    REFERENCES chain_logs(chain_id, transaction_hash, log_index)
);

CREATE INDEX IF NOT EXISTS chain_events_order_idx
  ON chain_events(chain_id, block_number, log_index);
CREATE INDEX IF NOT EXISTS chain_events_entity_idx
  ON chain_events(chain_id, entity_id, block_number, log_index);

-- The cursor is the NEXT inclusive chain position to request. Empty scanned blocks therefore
-- advance safely without inventing a timestamp or losing same-timestamp events.
CREATE TABLE IF NOT EXISTS chain_cursors (
  chain_id INTEGER NOT NULL, stream TEXT NOT NULL,
  next_block_number INTEGER NOT NULL, next_log_index INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (chain_id, stream)
);

CREATE TABLE IF NOT EXISTS chain_job_financials (
  chain_id INTEGER NOT NULL, job_id TEXT NOT NULL,
  funded_amount_atomic TEXT,
  advance_principal_atomic TEXT, advance_fee_atomic TEXT, advance_status TEXT,
  expense_total_atomic TEXT NOT NULL DEFAULT '0',
  delivery_hash TEXT,
  settlement_advance_repaid_atomic TEXT, operator_net_atomic TEXT,
  bond_slashed_atomic TEXT, reserve_used_atomic TEXT, socialized_atomic TEXT,
  last_block_number INTEGER NOT NULL, last_log_index INTEGER NOT NULL,
  PRIMARY KEY (chain_id, job_id)
);

CREATE TABLE IF NOT EXISTS chain_org_rates (
  chain_id INTEGER NOT NULL, org_address TEXT NOT NULL, rate_bps INTEGER NOT NULL,
  last_block_number INTEGER NOT NULL, last_log_index INTEGER NOT NULL,
  PRIMARY KEY (chain_id, org_address)
);

CREATE TABLE IF NOT EXISTS reconciliation_alerts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  chain_id INTEGER NOT NULL, job_id TEXT NOT NULL, field TEXT NOT NULL,
  local_value TEXT, chain_value TEXT, detected_at INTEGER NOT NULL,
  resolved INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS reconciliation_open_idx
  ON reconciliation_alerts(chain_id, resolved, job_id);
CREATE UNIQUE INDEX IF NOT EXISTS reconciliation_active_unique
  ON reconciliation_alerts(chain_id, job_id, field) WHERE resolved = 0;

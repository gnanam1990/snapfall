# Indexer test data

`h1-spine-logs.json` is deliberately synthetic golden input for deterministic decode,
ordering, projection, reconciliation, and replay tests. The repeated addresses, block hash,
job IDs, and sequential transaction hashes are fake.

This fixture is not an Arc testnet capture and must never be shown as explorer or
recording-integrity evidence. Live demo evidence must use receipts fetched from Arc and follow
the gate in `docs/A14-SECRET-INTEGRITY-AUDIT.md`.

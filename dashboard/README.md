# dashboard (owner: C)

Next.js + TS. Pages (FR-UI-001..006): Overview · Job (lifecycle/task graph/advance status) ·
Workforce · Approvals inbox · Float (TVL/utilization/fees/reserve/loss-waterfall) · Receipt.
Scaffold after the x402 loop is green — the loop outranks pixels.

## Event stream

Development uses the scripted H2 demo stream by default. To render a live daemon run,
point the server-side proxy at the owner API:

```sh
SNAPFALL_OWNER_API_URL=http://127.0.0.1:4010/api/v1 npm run dev
```

If the daemon was started with `SNAPFALL_OWNER_TOKEN`, provide the same value to the
dashboard process. It remains server-side and is never exposed to browser JavaScript.

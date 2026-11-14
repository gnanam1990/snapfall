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

With a daemon configured, the owner routes also need `SNAPFALL_DASHBOARD_OWNER_SECRET`
(at least 32 characters, different from the daemon token). The owner pages show a sign-in
prompt; entering the secret sets a signed, HttpOnly session cookie. Without the secret every
owner route refuses. `npm run dev` binds `127.0.0.1`. Behind a reverse proxy, pass the public
`Host` through or set `SNAPFALL_DASHBOARD_ORIGIN`; see `docs/HOSTING.md` for the full owner-access
setup, including nginx and Caddy examples.

## Float page

`/float` reads the selected deployment's `FloatPool` directly through the server-side
`/api/float` adapter. `SNAPFALL_NETWORK` picks the deployment at build time (`arc-testnet`
when unset, or `arc-mainnet`; see `lib/network.ts`). The RPC override is the variable that
deployment names, `ARC_TESTNET_RPC` or `ARC_MAINNET_RPC`; `SNAPFALL_FLOAT_POOL_ADDRESS` and
`SNAPFALL_DEPLOYMENT_BLOCK` retain the deployment-config override behavior. Set
`SNAPFALL_TREASURY_ADDRESS` to select the organization whose accepted-job/write-off
history derives the displayed advance rate. Relevant H2 chain events trigger an immediate
refresh; a 15-second read-only poll keeps the page current when the daemon relay is absent.

## Workforce hiring

`/workforce` renders the reviewed worker-manifest catalog. Build Monitor is the first
hireable specialist; its activation form requires a local Git repository containing
`.snapfall/milestone.json` and a milestone quote. The server-side `/api/workforce` adapter
forwards the owner action to the daemon without exposing `SNAPFALL_OWNER_TOKEN` to browser
JavaScript. The mutation proxy requires a signed-in owner session and accepts only same-origin
JSON requests, and durable daemon activation state is restored when the page reloads.

With `SNAPFALL_OWNER_API_URL` configured as above, **Activate watcher** opens milestone 1,
records the owner confirmation, and dispatches the registered Build Monitor against the
repository. Without a daemon connection, the catalog remains visible but activation fails
explicitly with `DAEMON_UNAVAILABLE`; the dashboard never pretends a watcher started.

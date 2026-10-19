# Dashboard hosting

The repository-root Dockerfile runs the public Next.js dashboard on any Docker host.
Keep the build context at the repository root: the app imports the sibling
`deployments/arc-testnet.json`. `PORT` is supported (default 3000).

The dashboard stays on Next.js 15 with patched dependencies. Its scoped PostCSS
override keeps Next's pinned transitive version on 8.5.31; recheck the override when
upgrading Next. Validate dependency changes with the dashboard tests and production build.

## Render

Create a Blueprint from the branch containing `render.yaml`, or create a Docker Web
Service from this repository with Dockerfile `./Dockerfile` and context `.`.
Use `/overview` for the health check; it does not depend on RPC availability.
The Blueprint selects Singapore and the Free compute plan, with automatic deploys off.
Review the service name for conflicts before applying the Blueprint.

Free services sleep after 15 minutes without inbound traffic and take roughly a minute
to wake. For an always-on public demo, choose a paid compute plan after reviewing the
current price. Free instances have workspace usage limits and are not a production SLA.
Check billing/spend settings before deployment; free compute does not remove all usage limits.

References: [Blueprint specification](https://render.com/docs/blueprint-spec),
[free-service limits](https://render.com/docs/free), [pricing](https://render.com/pricing).

## Runtime configuration

No signer keys are required to serve the public dashboard. The committed Arc testnet
RPC and contract addresses are the defaults. Leave `SNAPFALL_DEMO_STREAM` unset during
the build so fabricated activity fixtures are excluded.

Optionally set `ARC_TESTNET_RPC` as a private runtime environment variable to enable
historical chain scans. The provider must support the necessary `eth_getLogs` range;
set `SNAPFALL_FLOAT_SCAN_CHUNK` to match that provider's limit. Without a private RPC,
current chain reads remain enabled and historical aggregates can be unavailable.
Do not put a credential-bearing RPC URL into Git or a `NEXT_PUBLIC_*` variable.

Leave `SNAPFALL_OWNER_API_URL` and `SNAPFALL_OWNER_TOKEN` unset for the public demo.
The current dashboard proxy forwards its configured owner credentials without
authenticating a public visitor; a same-origin request check is not owner authentication.
Connecting a private daemon requires an authenticated access boundary first.
Without a daemon, the site explicitly reports disconnected activity and hiring is unavailable.

This deploys the dashboard only. Hosting the Go daemon and payment sidecar requires a
separate setup with durable state, private networking, and controlled owner access.

## Other Docker hosts

From the repository root:

```sh
docker build -t snapfall-dashboard .
docker run --rm -p 127.0.0.1:3000:3000 snapfall-dashboard
```

On a VPS, run the container with a restart policy behind a TLS reverse proxy. Keep
the application port private to that proxy. The Float history scanner runs in the
background and caches in process, so a long-lived container fits the existing design.

## Verification after deployment

1. Open `/overview`, `/float`, and `/audit` over HTTPS.
2. Check `/api/float`: expect HTTP 200 and `x-snapfall-source: arc-testnet` when RPC is healthy.
3. Check `/api/events/stream`: with no owner API, expect a snapshot containing
   `daemonConnected: false` and no fabricated activity.
4. With a private RPC, allow the historical scan to finish and confirm the Float page
   changes from pending to complete. Restarting the process rebuilds this in-memory cache.
5. Record the new public URL before updating links elsewhere. An old Railway-owned
   subdomain cannot be redirected from another provider.

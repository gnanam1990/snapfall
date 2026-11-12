# Dashboard hosting

The repository-root Dockerfile runs the public Next.js dashboard on any Docker host.
Keep the build context at the repository root: the app imports the sibling
`deployments/arc-testnet.json`. `PORT` is supported (default 3000).

The dashboard stays on Next.js 15 with patched dependencies. Its scoped PostCSS
override keeps Next's pinned transitive version on 8.5.31; recheck the override when
upgrading Next. Validate dependency changes with the dashboard tests and production build.

## Vercel Hobby: public testnet dashboard

Import the migration branch from GitHub, select the Next.js preset, and set Root
Directory to `dashboard`. Enable inclusion of files outside the root directory so
`deployments/arc-testnet.json` is available. Node 22 is selected by package.json.
Do not set owner API credentials or enable demo fixtures on this public project.

The disconnected SSE response finishes immediately and asks EventSource to reconnect
after 60 seconds, avoiding an idle billed function. Current chain reads work normally.
On Vercel the process-local background history scan is disabled, even with a custom
RPC: historical aggregates explicitly report unavailable. A durable indexer/storage
service is needed to add historical aggregation reliably in a serverless deployment.
The Go daemon and sidecar are not deployed by this project.

Hobby is for personal non-commercial use, within its usage limits. See
[Vercel Hobby](https://vercel.com/docs/plans/hobby).

## Render (alternative Docker host)

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

The committed default RPC is `https://rpc.testnet.arc.network`. The
[official Arc endpoint reference](https://docs.arc.io/arc/references/rpc-endpoints) lists
`https://rpc.testnet.arc.io`; both served chain ID 5042002 when re-checked on 4 Nov 2026
(the older hostname briefly failed to resolve during the migration check). Contract
addresses are unchanged.

Optionally set `ARC_TESTNET_RPC` as a private runtime environment variable to enable
historical chain scans. The provider must support the necessary `eth_getLogs` range;
set `SNAPFALL_FLOAT_SCAN_CHUNK` to match that provider's limit. Without a private RPC,
current chain reads remain enabled and historical aggregates can be unavailable.
Do not put a credential-bearing RPC URL into Git or a `NEXT_PUBLIC_*` variable.

Leave `SNAPFALL_OWNER_API_URL` and `SNAPFALL_OWNER_TOKEN` unset for the public demo.
Without a daemon, the site explicitly reports disconnected activity and hiring is unavailable.

### Connecting a daemon: owner access

When a daemon is connected, `SNAPFALL_OWNER_API_URL` is the daemon's API base including
`/api/v1` (for example `https://daemon.example/api/v1`); the dashboard appends `/jobs/…`,
`/customer/…` and the other route paths to it. The daemon answers only loopback Host
values unless `SNAPFALL_OWNER_ALLOWED_HOSTS` names the host its clients use (here
`daemon.example`), and that setting requires `SNAPFALL_OWNER_TOKEN`. It also refuses
cross-origin browser requests and non-JSON request bodies; the dashboard's server-side
proxy sends neither.

The dashboard's owner routes (approvals list and decision, customer accept-link mint, workforce
state and hire, and the live event stream) act with `SNAPFALL_OWNER_TOKEN`. Each one first
requires an owner session: the owner signs in once with a dashboard-only secret, and the
dashboard sets an HttpOnly, `SameSite=Strict` cookie (also `Secure` in production builds)
holding an HMAC-signed value that expires after 12 hours. The cookie never contains the
secret. Owner mutations additionally require a same-origin `application/json` request
(`Origin` matching the dashboard's origin as below, and `Sec-Fetch-Site: same-origin` when the
browser sends it). Those headers are cross-site request forgery defence only; the session is
the authentication.

In production builds (`NODE_ENV=production`, which `npm start` and the Docker image use) the
cookie is named `__Host-snapfall_owner_session`. Browsers accept a `__Host-` cookie only when it
is `Secure`, `Path=/` and has no `Domain`, so a sibling subdomain cannot plant or shadow it.
`npm run dev` keeps the plain name `snapfall_owner_session` over plain HTTP; there, every cookie
of that name is checked, so a stray one cannot hide a valid session. Deploying this change signs
out existing production sessions once (the cookie name changed).

| Variable | Required | Purpose |
| --- | --- | --- |
| `SNAPFALL_OWNER_API_URL` | to connect a daemon | Daemon owner API base, including `/api/v1`. |
| `SNAPFALL_OWNER_TOKEN` | when the daemon has one | Daemon bearer token. Server-side only. |
| `SNAPFALL_DASHBOARD_OWNER_SECRET` | whenever `SNAPFALL_OWNER_API_URL` is set | What the owner enters to sign in. At least 32 characters and different from `SNAPFALL_OWNER_TOKEN`. Generate it with `openssl rand -hex 32` (64 hex characters, 256 bits). The length check counts characters, not entropy, so do not hand-type a long phrase. |
| `SNAPFALL_DASHBOARD_HOSTS` | optional | Comma-separated allowlist of the request host (for example `dash.example.com`) for owner and sign-in requests. Requests for any other host get 403. The request host is the `Host` header, or the proxy's `X-Forwarded-Host` under `SNAPFALL_DASHBOARD_TRUST_PROXY=1`. |
| `SNAPFALL_DASHBOARD_ORIGIN` | behind a proxy that rewrites `Host` | Comma-separated public origin(s) the owner's browser uses, exactly, for example `https://dash.example.com`. When set, an owner mutation's `Origin` must equal one of them (scheme, host and port), and `Host` plays no part in that check. A value with a path, query or credentials refuses every owner route with `503 OWNER_ACCESS_NOT_CONFIGURED`. |
| `SNAPFALL_DASHBOARD_TRUST_PROXY` | optional | `1` only when a reverse proxy you control is in front and overwrites `X-Forwarded-Host`. The request host is then the last `X-Forwarded-Host` value. Unset (or any other value), `X-Forwarded-Host` is ignored, because without a proxy overwriting it any client can send it. |

Set `SNAPFALL_DASHBOARD_OWNER_SECRET` together with the daemon variables, as a private runtime
variable, never a `NEXT_PUBLIC_*` one. If the daemon is configured but the secret is missing,
too short, or equal to the daemon token, every owner route refuses with
`503 OWNER_ACCESS_NOT_CONFIGURED`; it does not fall back to acting for an anonymous visitor.
Rotating the secret signs out every browser; the Settings page has a per-browser sign-out.
Over plain HTTP on a host other than `localhost`, a production build's `Secure` cookie is not
stored, so serve owner access over HTTPS.

The customer portal (`/pay`, `/portal`, `/api/customer/…`) and the public chain and CCTP reads
are not behind the owner session: customer routes forward the customer's own accept token.

`npm run dev` binds `127.0.0.1` only. `npm start` (and the Docker image) still binds all
interfaces, so keep its port private to a TLS reverse proxy, as below.

### Behind a reverse proxy

The owner guard compares the browser's `Origin` (the public name, `https://dash.example.com`)
with the host the request was addressed to. A proxy decides what the dashboard sees as that
host, so configure one of the following. Without any of them, a proxy that rewrites `Host` makes
every owner mutation, sign-in included, answer 403.

**Preferred: pass the public `Host` through.** Nothing else to set, and
`SNAPFALL_DASHBOARD_HOSTS=dash.example.com` works as written.

nginx (its default `proxy_pass` sends `Host: 127.0.0.1:3000`, which is the failing case):

```nginx
server {
    listen 443 ssl;
    server_name dash.example.com;
    # ssl_certificate ... ;

    location / {
        proxy_pass http://127.0.0.1:3000;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        # The owner event stream is server-sent events: do not buffer it.
        proxy_buffering off;
        proxy_read_timeout 1h;
    }
}
```

Caddy passes the original `Host` by default and overwrites `X-Forwarded-Host` itself:

```caddyfile
dash.example.com {
    reverse_proxy 127.0.0.1:3000
}
```

**If the proxy must rewrite `Host`** (some managed load balancers do), set
`SNAPFALL_DASHBOARD_ORIGIN=https://dash.example.com`. The CSRF check then compares `Origin`
against that value and ignores `Host`. Leave `SNAPFALL_DASHBOARD_HOSTS` unset, or list the
`Host` the proxy actually sends (for example `127.0.0.1:3000`), since the allowlist still checks
the request host; in this shape the proxy's own `server_name` is what rejects foreign names.

**If the proxy rewrites `Host` but sets `X-Forwarded-Host`** (`proxy_set_header X-Forwarded-Host
$host` in nginx, Caddy by default), you may instead set `SNAPFALL_DASHBOARD_TRUST_PROXY=1`. Then
both the allowlist and the CSRF check use the last `X-Forwarded-Host` value, so
`SNAPFALL_DASHBOARD_HOSTS=dash.example.com` works. Only do this when the dashboard port is
reachable from the proxy alone and the proxy overwrites the header: nginx passes a
client-supplied `X-Forwarded-Host` through unchanged unless told otherwise.

`SNAPFALL_DASHBOARD_ORIGIN` and `SNAPFALL_DASHBOARD_TRUST_PROXY` can be combined: the origin
check uses the configured origin, and the allowlist uses the forwarded host.

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
6. With a daemon connected, sign in through the public URL. A `403` with code `FORBIDDEN` on
   sign-in means the proxy's `Host` does not match the browser's `Origin`; see
   [Behind a reverse proxy](#behind-a-reverse-proxy). A `403 FORBIDDEN_HOST` means
   `SNAPFALL_DASHBOARD_HOSTS` does not list the request host.

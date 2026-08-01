# Snapfall dashboard — repo-root Dockerfile.
#
# The dashboard imports deployments/arc-testnet.json (the canonical contract addresses) from
# the repo root, so the build must see BOTH dashboard/ and deployments/ as siblings. That is
# why this builds from the repo root rather than from dashboard/. Contracts are NOT needed:
# the chain libs carry inline ABIs, so only dashboard/ and deployments/ are copied.
#
# A missing address therefore fails THIS build loudly (the JSON import can't resolve), which
# is the intended failure mode — a dashboard whose whole claim is verifiability must never
# start with silently-wrong addresses.

# ---- build ----
FROM node:22-slim AS build
WORKDIR /app

# Install deps first for layer caching (only re-runs when the lockfile changes).
COPY dashboard/package.json dashboard/package-lock.json ./dashboard/
RUN cd dashboard && npm ci

# The build needs dashboard/ and its sibling deployments/ (the canonical addresses).
COPY dashboard ./dashboard
COPY deployments ./deployments

# SNAPFALL_DEMO_STREAM is unset here, so next.config's IgnorePlugin strips lib/demoStream
# and lib/mockData: no fabricated fixtures reach the artifact.
RUN cd dashboard && npm run build

# ---- run ----
FROM node:22-slim AS run
WORKDIR /app/dashboard
ENV NODE_ENV=production

# `next start` needs .next, node_modules, package.json and next.config; there is no public/.
# deployments/ is not copied: its contents were bundled into .next at build time.
COPY --from=build /app/dashboard/package.json /app/dashboard/package-lock.json ./
COPY --from=build /app/dashboard/node_modules ./node_modules
COPY --from=build /app/dashboard/.next ./.next
COPY --from=build /app/dashboard/next.config.mjs ./

EXPOSE 3000
CMD ["npm", "start"]

/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  env: {
    // Which deployment record the build reads (lib/network.ts): arc-testnet when unset, or
    // arc-mainnet. Inlined at build time so the static pages, the client chrome and the API
    // routes all agree on one chain: the choice is made once, here.
    SNAPFALL_NETWORK: process.env.SNAPFALL_NETWORK ?? '',
  },
};

export default nextConfig;

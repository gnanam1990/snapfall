/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  // Inline the demo-replay flag at BUILD time. Next reads server process.env at runtime,
  // which would keep the dynamic `import('@/lib/demoStream')` branch alive and ship the
  // fabricated fixtures as a lazy chunk even when the flag is off. Inlining it here folds
  // `process.env.SNAPFALL_DEMO_STREAM === '1'` to a constant, so with the flag unset the
  // branch is dead code and webpack drops demoStream + lib/mockData from the bundle entirely.
  // A public deployment therefore ships no fabricated content, reachable or not.
  env: {
    SNAPFALL_DEMO_STREAM: process.env.SNAPFALL_DEMO_STREAM ?? '',
  },
  webpack: (config, { webpack }) => {
    // Inlining the flag folds the demo branch out of the executable code, but webpack still
    // emits the dynamic-import target as an (unreferenced) chunk on disk. When the flag is
    // off, refuse to emit the demo modules at all so their content — including the fabricated
    // lib/mockData fixtures — never reaches the deployment artifact, reachable or not. The
    // dead branch that would import them is never executed, so ignoring them is safe. With
    // the flag on, no plugin is added and the demo replay bundles normally.
    if (process.env.SNAPFALL_DEMO_STREAM !== '1') {
      config.plugins.push(
        new webpack.IgnorePlugin({ resourceRegExp: /(^|[\\/])lib[\\/](demoStream|mockData)$/ }),
      );
    }
    return config;
  },
};

export default nextConfig;

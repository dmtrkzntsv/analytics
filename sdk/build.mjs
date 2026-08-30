// Builds the committed bundle the collector embeds and serves at
// /js/twillingate.js. Deterministic for a given esbuild version, so CI can
// rebuild and `git diff --exit-code` the artifact.
import { build } from "esbuild";

// __TWILLINGATE_VERSION__ is substituted by the collector at serve time
// with its own build version (the release tag), so the served file names
// the release it shipped in while the committed artifact stays
// deterministic for CI's drift check.
await build({
  entryPoints: ["src/entry.ts"],
  bundle: true,
  format: "iife",
  target: "es2018",
  minify: true,
  legalComments: "none",
  banner: {
    js: "/* twillingate.js __TWILLINGATE_VERSION__ — web, product and app analytics SDK.\n * Source: sdk/ in https://github.com/dmtrkzntsv/twillingate */",
  },
  outfile: "../internal/server/twillingate.js",
});
console.log("built ../internal/server/twillingate.js");

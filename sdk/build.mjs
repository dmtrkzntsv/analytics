// Builds the committed bundle the collector embeds and serves at
// /js/twillingate.js. Deterministic for a given esbuild version, so CI can
// rebuild and `git diff --exit-code` the artifact.
import { build } from "esbuild";
import { readFileSync } from "node:fs";

const { version } = JSON.parse(readFileSync(new URL("./package.json", import.meta.url)));

await build({
  entryPoints: ["src/entry.ts"],
  bundle: true,
  format: "iife",
  target: "es2018",
  minify: true,
  legalComments: "none",
  banner: {
    js: `/* twillingate.js v${version} — web, product and app analytics SDK.\n * Source: sdk/ in https://github.com/dmtrkzntsv/twillingate */`,
  },
  outfile: "../internal/server/twillingate.js",
});
console.log(`built ../internal/server/twillingate.js (v${version})`);

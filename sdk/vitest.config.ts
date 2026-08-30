import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    globals: true,
    environment: "jsdom",
    environmentOptions: {
      // Not localhost: the SDK's ignore rules would otherwise drop every
      // event in every test.
      jsdom: { url: "https://example.com/start" },
    },
  },
});

import { defineConfig } from "vite";

// Bundle the stdio sidecar into one ESM file. Node builtins stay external, so
// the output runs unchanged under both bun and node.
export default defineConfig({
  build: {
    ssr: "src/main.ts",
    outDir: "dist",
    emptyOutDir: true,
    target: "node22",
    minify: false,
    rollupOptions: { output: { entryFileNames: "main.js", format: "es" } },
  },
});

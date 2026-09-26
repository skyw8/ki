import { defineConfig } from "vite";

// Bundle the stdio sidecar into one ESM file. Node builtins and the native
// @zvec/zvec-grep dependency stay external: the library ships prebuilt native
// artifacts (and its own model runtime), so it must resolve from the package's
// node_modules at runtime instead of being inlined.
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

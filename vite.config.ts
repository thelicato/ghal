import { fileURLToPath, URL } from "node:url"
import { writeFileSync } from "node:fs"
import { defineConfig } from "vite"
import tailwindcss from "@tailwindcss/vite"

export default defineConfig({
  root: "web",
  plugins: [tailwindcss(), {
    name: "preserve-embed-placeholder",
    closeBundle() { writeFileSync("web/dist/.gitkeep", "") },
  }],
  resolve: { alias: { "@": fileURLToPath(new URL("./web/src", import.meta.url)) } },
  esbuild: { jsx: "automatic" },
  server: {
    proxy: { "/api": "http://127.0.0.1:8080" },
  },
  build: { outDir: "dist", emptyOutDir: true },
})

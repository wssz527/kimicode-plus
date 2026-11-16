import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
export default defineConfig({ plugins: [react()], define: { "process.env.NODE_ENV": JSON.stringify("production") }, build: { minify: true, outDir: "dist-host", emptyOutDir: true, target: "es2022", lib: { entry: "src/host-entry.tsx", formats: ["es"], fileName: "enhancement", cssFileName: "enhancement" } } });

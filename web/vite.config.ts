import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath, URL } from "node:url";

const defaultTarget = "http://127.0.0.1:43125";
const target = process.env.ENHANCE_DEV_TARGET ?? defaultTarget;
const targetOrigin = new URL(target).origin;

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    strictPort: false,
    fs: { allow: [fileURLToPath(new URL("..", import.meta.url))] },
    proxy: {
      "/enhance": {
        target,
        changeOrigin: true,
        // 仅开发代理：浏览器在 5173 发出的写请求会带 Vite Origin，
        // 核心只接受同源 Origin，所以在这里改写为代理目标源。
        // 生产 Guard 不变，仍严格校验 Host/Origin。
        configure: (proxy) => {
          proxy.on("proxyReq", (proxyReq) => {
            const origin = proxyReq.getHeader("origin");
            if (typeof origin === "string" && origin.length > 0) {
              proxyReq.setHeader("origin", targetOrigin);
            }
          });
        },
      },
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    target: "es2022",
    assetsInlineLimit: 4096,
    cssCodeSplit: false,
    sourcemap: false,
    reportCompressedSize: true,
  },
});

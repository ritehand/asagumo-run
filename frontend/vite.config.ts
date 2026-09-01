import { defineConfig } from "vite";
import tailwindcss from "@tailwindcss/vite";

// Proxy API paths to the Go keep-alive server (PORT env, default 8000)
const apiTarget = `http://localhost:${process.env.PORT ?? 8000}`;

export default defineConfig({
  plugins: [tailwindcss()],
  server: {
    proxy: {
      "/health": apiTarget,
      "/webhook": apiTarget,
    },
  },
});

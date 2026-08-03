import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';

// In production the Go server injects the session token by replacing the
// literal `__CONFIG__` token in dist/index.html. The Vite dev server can't do
// that, so fetch the running app's page and reuse the same config.
function devConfig(): Plugin {
  let cfg = JSON.stringify({ token: '', saveDir: '' });
  return {
    name: 'taildrop-dev-config',
    apply: 'serve',
    configureServer(server) {
      server.httpServer?.once('listening', async () => {
        try {
          const res = await fetch('http://127.0.0.1:8976/');
          const html = await res.text();
          const m = html.match(/window\.__CONFIG__\s*=\s*(\{.*?\});/);
          if (m) cfg = m[1];
        } catch {
          // App not running — the UI will show "tailscaled unreachable".
        }
      });
    },
    transformIndexHtml(html) {
      return html.replace('__TAILDROP_CONFIG__', cfg);
    },
  };
}

export default defineConfig({
  plugins: [react(), devConfig()],
  base: '/',
  build: { outDir: 'dist' },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:8976',
      '/events': 'http://127.0.0.1:8976',
    },
  },
});

import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

const repository = fileURLToPath(new URL('../..', import.meta.url));

// 发布 Worker SDK 源文件，供首页等页面在同一站点按需导入（demo/sdk/client.js）。
function publishSDK() {
  return {
    name: 'psdkit-publish-sdk',
    async generateBundle() {
      for (const name of ['client.js', 'client.d.ts', 'samples.js']) {
        const source = await readFile(new URL(`../sdk/${name}`, import.meta.url), 'utf8');
        this.emitFile({ type: 'asset', fileName: `sdk/${name}`, source });
      }
    },
  };
}

export default defineConfig({
  base: process.env.PSDKIT_BASE_PATH || './',
  plugins: [react(), publishSDK()],
  server: {
    proxy: { '/__fixtures': 'http://127.0.0.1:8194' },
    fs: { allow: [repository] },
  },
  build: { target: 'es2022' },
});

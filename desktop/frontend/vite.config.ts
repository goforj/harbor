import { writeFileSync } from 'node:fs'
import path from 'node:path'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vite'

export default defineConfig({
  base: './',
  clearScreen: false,
  plugins: [
    vue(),
    tailwindcss(),
    {
      name: 'retain-wails-embed-directory',
      closeBundle() {
        writeFileSync(
          path.resolve(__dirname, 'dist/.gitkeep'),
          'Harbor keeps the embed root present before the first frontend build.\n',
        )
      },
    },
  ],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    host: '127.0.0.1',
    port: 5173,
    strictPort: true,
  },
})

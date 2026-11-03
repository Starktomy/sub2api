import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'path'

// A separate entry graph: never import the consumer router, App or store barrels.
export default defineConfig({
  plugins: [vue(), {
    name: 'supplier-boundary',
    enforce: 'post',
    generateBundle(_options, bundle) {
      const html = bundle['supplier.html']
      if (html) {
        delete bundle['supplier.html']
        html.fileName = 'index.html'
        bundle['index.html'] = html
      }
      for (const id of this.getModuleIds()) {
        if (/\/src\/(views\/(admin|user|auth)|stores|router\/index|api\/(client|auth|admin)|App\.vue|i18n\/locales\/[^/]+\/(admin|index))/.test(id)) {
          this.error(`Consumer module in supplier build: ${id}`)
        }
      }
    }
  }],
  resolve: { alias: {
    '@': resolve(__dirname, 'src'),
    'vue-i18n': 'vue-i18n/dist/vue-i18n.runtime.esm-bundler.js'
  } },
  build: {
    outDir: 'dist/supplier',
    emptyOutDir: true,
    manifest: true,
    rollupOptions: { input: resolve(__dirname, 'supplier.html') }
  }
})

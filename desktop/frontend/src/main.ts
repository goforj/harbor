import { createPinia } from 'pinia'
import { createApp } from 'vue'
import 'vue-sonner/style.css'
import { applyTheme, watchSystemTheme } from './lib/theme'
import './style.css'
import { bootstrapWailsV3 } from './bridge/wails-v3'

applyTheme()
watchSystemTheme()

bootstrapWailsV3().then(async () => {
  // Bridge selection happens during component imports, after the native transport is installed.
  const [{ default: App }, { default: router }] = await Promise.all([import('./App.vue'), import('./router')])
  createApp(App).use(createPinia()).use(router).mount('#app')
}).catch(error => {
  document.getElementById('app')!.textContent = 'Harbor could not initialize its native desktop connection.'
  console.error(error)
})

import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import i18n from './i18n'
import '@/style.css'

const dark = localStorage.getItem('supplier_theme') === 'dark' || (!localStorage.getItem('supplier_theme') && window.matchMedia('(prefers-color-scheme: dark)').matches)
document.documentElement.classList.toggle('dark', dark)
const app = createApp(App)
app.use(i18n)
document.documentElement.lang = i18n.global.locale.value
document.title = i18n.global.t('supplier.title')
app.use(router)
router.isReady().then(() => app.mount('#app'))

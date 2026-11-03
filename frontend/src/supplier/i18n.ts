import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en/supplier'
import zh from '@/i18n/locales/zh/supplier'

export default createI18n({
  legacy: false,
  locale: navigator.language.toLowerCase().startsWith('zh') ? 'zh' : 'en',
  fallbackLocale: 'en',
  messages: { en: { supplier: en }, zh: { supplier: zh } }
})

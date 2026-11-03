import { createRouter, createWebHistory } from 'vue-router'
import { supplierSession } from './session'
import { isolatedSupplierAPI } from './api'

export const SUPPLIER_ROUTES = ['/supplier', '/supplier/portal', '/login', '/register'] as const
export function supplierReturn(value: unknown): string {
  return value === '/supplier' || value === '/supplier/portal' ? value : '/supplier/portal'
}
const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/supplier', component: () => import('./Entry.vue') },
    { path: '/supplier/portal', component: () => import('./Portal.vue'), meta: { supplierAuth: true } },
    { path: '/login', component: () => import('./Auth.vue') },
    { path: '/register', component: () => import('./Auth.vue') },
    { path: '/:pathMatch(.*)*', redirect: '/supplier' }
  ]
})
let restoring: Promise<void> | null = null
router.beforeEach(async to => {
  if (!supplierSession.ready) await (restoring ??= isolatedSupplierAPI.restore())
  if (to.meta.supplierAuth && !supplierSession.user) return { path: '/login', query: { redirect: '/supplier/portal' } }
  if (supplierSession.user && (to.path === '/login' || to.path === '/register')) return supplierReturn(to.query.redirect)
})
export default router

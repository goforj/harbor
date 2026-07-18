import { createRouter, createWebHashHistory } from 'vue-router'

const OverviewView = () => import('@/views/OverviewView.vue')
const ProjectView = () => import('@/views/ProjectView.vue')
const ServiceView = () => import('@/views/ServiceView.vue')
const SystemView = () => import('@/views/SystemView.vue')

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', redirect: '/overview' },
    { path: '/overview', name: 'overview', component: OverviewView, meta: { title: 'Overview' } },
    { path: '/projects/:projectId?', name: 'project', component: ProjectView, meta: { title: 'Projects' } },
    { path: '/services/:serviceId?', name: 'service', component: ServiceView, meta: { title: 'Services' } },
    { path: '/system/:section?', name: 'system', component: SystemView, meta: { title: 'System' } },
    { path: '/:pathMatch(.*)*', redirect: '/overview' },
  ],
})

router.afterEach((route) => {
  const title = typeof route.meta.title === 'string' ? route.meta.title : 'Harbor'
  document.title = `${title} · GoForj Harbor`
})

export default router

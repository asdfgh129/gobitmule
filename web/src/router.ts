import { createRouter, createWebHashHistory } from 'vue-router'
import DashboardView from '@/views/DashboardView.vue'
import TasksView from '@/views/TasksView.vue'
import NewDownloadView from '@/views/NewDownloadView.vue'
import SettingsView from '@/views/SettingsView.vue'
import OperationsView from '@/views/OperationsView.vue'

export interface RouteMeta {
  title: string
  subtitle: string
  icon: string
}

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', redirect: '/dashboard' },
    {
      path: '/dashboard', name: 'dashboard', component: DashboardView,
      meta: { title: '概览', subtitle: '守护进程与传输状态一目了然', icon: 'dashboard' } satisfies RouteMeta,
    },
    {
      path: '/tasks', name: 'tasks', component: TasksView,
      meta: { title: '任务', subtitle: '管理所有下载与后处理任务', icon: 'tasks' } satisfies RouteMeta,
    },
    {
      path: '/new', name: 'new', component: NewDownloadView,
      meta: { title: '新建下载', subtitle: '解析链接并加入下载队列', icon: 'plus' } satisfies RouteMeta,
    },
    {
      path: '/settings', name: 'settings', component: SettingsView,
      meta: { title: '设置', subtitle: '运行参数、凭据与连接', icon: 'settings' } satisfies RouteMeta,
    },
    {
      path: '/operations', name: 'operations', component: OperationsView,
      meta: { title: '运维', subtitle: '健康检查、事件历史与离线命令', icon: 'tools' } satisfies RouteMeta,
    },
    { path: '/:pathMatch(.*)*', redirect: '/dashboard' },
  ],
})

export default router

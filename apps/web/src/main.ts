import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import './assets/main.css'

const app = createApp(App)

app.use(createPinia())
app.use(router)

app.mount('#app')

// A notification click in an open tab routes here instead of reloading.
if ('serviceWorker' in navigator) {
  navigator.serviceWorker.addEventListener('message', event => {
    const url = event.data?.type === 'mailat:open' ? String(event.data.url || '') : ''
    if (url.startsWith('/') && !url.startsWith('//')) void router.push(url)
  })
}

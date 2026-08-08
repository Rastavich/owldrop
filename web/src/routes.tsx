// Tabs are hash routes (#/inbox, #/send, …) so URLs work from LAN/browser
// use without server-side SPA fallback. "/" is the inbox.
import { createHashHistory } from '@tanstack/history';
import { createRootRoute, createRoute, createRouter } from '@tanstack/react-router';
import App from './App';
import Drops from './views/Drops';
import History from './views/History';
import Inbox from './views/Inbox';
import Send from './views/Send';
import Settings from './views/Settings';
import Sync from './views/Sync';

const rootRoute = createRootRoute({ component: App });

const inboxRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: Inbox });
const sendRoute = createRoute({ getParentRoute: () => rootRoute, path: '/send', component: Send });
const historyRoute = createRoute({ getParentRoute: () => rootRoute, path: '/history', component: History });
const dropsRoute = createRoute({ getParentRoute: () => rootRoute, path: '/drops', component: Drops });
const syncRoute = createRoute({ getParentRoute: () => rootRoute, path: '/sync', component: Sync });
const settingsRoute = createRoute({ getParentRoute: () => rootRoute, path: '/settings', component: Settings });

const routeTree = rootRoute.addChildren([inboxRoute, sendRoute, historyRoute, dropsRoute, syncRoute, settingsRoute]);

export const router = createRouter({ routeTree, history: createHashHistory() });

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router;
  }
}

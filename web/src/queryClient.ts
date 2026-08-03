import { QueryClient } from '@tanstack/react-query';

// Singleton so the SSE handler (outside React) can update the same cache the
// components read.
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: { retry: 1, refetchOnWindowFocus: false },
  },
});

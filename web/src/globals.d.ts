import type { PageConfig } from './types';

// The Go server injects the session config by replacing the `__CONFIG__`
// token in the served HTML (server.go handleIndex).
declare global {
  interface Window {
    __CONFIG__?: PageConfig;
  }
}

export {};

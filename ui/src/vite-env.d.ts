/// <reference types="vite/client" />

// Populated at runtime by /config.js, served by the Go backend.
// Only pre-approved, non-secret values belong here. These are the OIDC
// (Dex) parameters the SPA will use for login once auth is wired up.
interface AppConfig {
  oidcIssuer: string;
  oidcClientId: string;
}

declare global {
  interface Window {
    appConfig: AppConfig;
  }
}

export {};

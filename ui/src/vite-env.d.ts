/// <reference types="vite/client" />

// Populated at runtime by /config.js, served by the Go backend.
// Only pre-approved, non-secret values belong here. These are the OIDC
// (Dex) parameters the SPA will use for login once auth is wired up.
interface AppConfig {
  oidcIssuer: string;
  oidcClientId: string;
  // Whether this server lets users create organizations. A server-level
  // security setting (on by default); when false, users with no organization
  // are told to request an invite instead of being shown a create screen.
  allowOrgCreation: boolean;
}

declare global {
  interface Window {
    appConfig: AppConfig;
  }
}

export {};

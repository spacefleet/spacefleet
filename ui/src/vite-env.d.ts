/// <reference types="vite/client" />

// Populated at runtime by /config.js, served by the Go backend.
// Only pre-approved, non-secret values belong here. These are the OIDC
// (Dex) parameters the SPA will use for login once auth is wired up.
// One selectable sign-in option on the login screen. Mirrors a Dex connector
// (id/name) plus its type; the login page renders a button per method and
// deep-links to that connector via ?connector_id=<id>.
interface LoginMethod {
  id: string;
  name: string;
  type: string;
}

interface AppConfig {
  oidcIssuer: string;
  oidcClientId: string;
  // Sign-in options shown on the login screen, mirroring the operator's Dex
  // connectors. Empty → the login page falls back to a single "Sign in" button.
  loginMethods: LoginMethod[];
  // Whether this server lets users create organizations. A server-level
  // security setting (on by default); when false, users with no organization
  // are told to request an invite instead of being shown a create screen.
  allowOrgCreation: boolean;
  // Whether outbound email (SMTP) is configured. When false, the invite UI
  // tells admins to copy and share the link manually rather than implying an
  // email was sent. Non-secret; sending is decided server-side regardless.
  emailEnabled: boolean;
  // Whether a GitHub App is configured, so the SPA can offer "Connect GitHub"
  // for pulling charts from private Git repositories. Non-secret (the App's
  // private key never leaves the server).
  githubAppEnabled: boolean;
}

declare global {
  interface Window {
    appConfig: AppConfig;
  }
}

export {};

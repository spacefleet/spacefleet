import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router";
import { AuthProvider } from "react-oidc-context";
import { WebStorageStateStore } from "oidc-client-ts";
import { App } from "./App";
import "./index.css";

// OIDC (Dex) settings, derived from window.appConfig (served by the Go
// backend via /config.js). The SPA is a public client doing Authorization
// Code + PKCE; the ID token it receives is sent as the API bearer token
// (see components/ApiAuthBinder.tsx). redirect_uri is origin-relative so the
// same build works behind the Vite dev server (:2424) and the embedded
// binary (:8080) — both are registered in dev/dex/config.yaml.
const oidcConfig = {
  authority: window.appConfig.oidcIssuer,
  client_id: window.appConfig.oidcClientId,
  redirect_uri: `${window.location.origin}/auth/callback`,
  post_logout_redirect_uri: window.location.origin,
  response_type: "code",
  scope: "openid profile email",
  // Persist the session across reloads (default is sessionStorage).
  userStore: new WebStorageStateStore({ store: window.localStorage }),
  // No-op: the /auth/callback route (routes/AuthCallback.tsx) navigates home
  // via React Router after the exchange, which both cleans the ?code/&state
  // from the URL and keeps the router's location in sync. Doing a raw
  // history.replaceState here would desync React Router.
  onSigninCallback: () => {},
};

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <AuthProvider {...oidcConfig}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </AuthProvider>
  </React.StrictMode>,
);

import { PROTOCOL_VERSION } from "@worky/protocol";

// Placeholder until the real UI lands.
const app = document.querySelector<HTMLElement>("#app");
if (app) {
  app.textContent = `Worky (protocol v${PROTOCOL_VERSION})`;
}
